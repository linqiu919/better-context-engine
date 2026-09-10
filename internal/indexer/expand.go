package indexer

import (
	"context"
	"strings"
	"time"
	"unicode"
)

// Query term expansion. The tokenizer splits on word boundaries, but some
// writing systems don't mark them: a query segment in such a script comes out
// as one opaque token, so the BM25 and path/symbol paths score zero on it and
// retrieval rides on the semantic path alone — which drifts on abstract
// vocabulary ("database model" landing on connection pools). Code itself is
// overwhelmingly written in English identifiers regardless of the query
// language, so the enhancer LLM translates the query into the English
// technical terms the answering files would actually contain, and their
// lexical+structural ranking folds into the fusion as an extra path. This is
// a property of the script (no word separators), not of any one language.

const (
	expandTTL      = 8 * time.Second
	expandMaxTerms = 12
)

// spacelessScripts are writing systems that do not separate words with
// spaces. Hangul is absent deliberately: Korean text is space-delimited and
// tokenizes fine.
var spacelessScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai,
	unicode.Lao, unicode.Khmer, unicode.Myanmar, unicode.Tibetan,
}

func needsTermExpansion(query string) bool {
	n := 0
	for _, r := range query {
		if unicode.In(r, spacelessScripts...) {
			if n++; n >= 2 {
				return true
			}
		}
	}
	return false
}

const expandSystemPrompt = `You translate one code-search query into English search terms for a code retrieval engine.

Rules:
- Output 6 to 12 terms on a single line, space separated, nothing else: no explanations, no numbering, no punctuation between terms.
- Terms are what a developer would grep for to answer the query: framework vocabulary, likely class/file/symbol names, annotations, config keys. Example: for a query about database models output terms like "entity model mapper repository BaseEntity schema table orm".
- Single words or CamelCase identifiers only, always in English, even when the query is in another language.`

// startTermExpand asks the enhancer chat model for English search terms,
// concurrently with candidate collection and the query embedding. Any failure
// yields nil: the query just runs on the original three paths.
func (s *Service) startTermExpand(ctx context.Context, query string) <-chan []string {
	ch := make(chan []string, 1)
	go func() {
		cfg := s.enhanceConfig(ctx)
		if !cfg.enabled() {
			ch <- nil
			return
		}
		url := strings.TrimSuffix(cfg.URL, "/")
		if !strings.HasSuffix(url, "/v1") {
			url += "/v1"
		}
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		ectx, cancel := context.WithTimeout(ctx, expandTTL)
		defer cancel()
		body := map[string]any{
			"model":      cfg.Model,
			"messages":   []map[string]string{{"role": "system", "content": expandSystemPrompt}, {"role": "user", "content": query}},
			"stream":     false,
			"max_tokens": 120,
			// same Qwen3 hybrid-model guard as startDecompose: without this the
			// budget goes to <think> and content comes back empty.
			"enable_thinking": false,
		}
		if err := postJSON(ectx, url+"/chat/completions", cfg.apiKey(), body, &response); err != nil || len(response.Choices) == 0 {
			ch <- nil
			return
		}
		ch <- parseExpandedTerms(response.Choices[0].Message.Content)
	}()
	return ch
}

// parseExpandedTerms keeps deduplicated ASCII-bearing terms: a term the model
// echoes back in the query's own script would tokenize into the same opaque
// blob the expansion exists to work around.
func parseExpandedTerms(content string) []string {
	terms := []string{}
	seen := map[string]bool{}
	for _, field := range strings.Fields(content) {
		field = strings.Trim(field, "\"'`,.;:!?()[]{}<>-*•")
		if field == "" || len(field) > 48 || !hasASCIILetter(field) {
			continue
		}
		key := strings.ToLower(field)
		if seen[key] {
			continue
		}
		seen[key] = true
		terms = append(terms, field)
		if len(terms) >= expandMaxTerms {
			break
		}
	}
	return terms
}

func hasASCIILetter(value string) bool {
	for i := 0; i < len(value); i++ {
		if b := value[i]; b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
			return true
		}
	}
	return false
}

// fuseExpandedTerms scores the expanded term list on the lexical and
// structural paths and folds both rankings into the shared RRF map — the same
// weight fuseSubQueries gives one sub-query. For a query whose own tokens are
// opaque these become the only word-level signal, standing in for the two
// paths the original query cannot use.
func (s *Service) fuseExpandedTerms(ctx context.Context, candidates []*candidate, terms []string, fused map[int]float64) {
	if len(terms) == 0 {
		return
	}
	joined := strings.Join(terms, " ")
	lex := s.bm25Values(ctx, candidates, codeTokens(joined))
	structuralTerms := tokens(joined)
	structural := make([]float64, len(candidates))
	for i, c := range candidates {
		p, sym := structuralValues(c, structuralTerms)
		structural[i] = p + sym
		// Write-back is display-only: no ranking stage reads these fields
		// after this point. Without it a spaceless-script query shows 0.00
		// on all three word-level paths even when the expansion terms are
		// what actually matched.
		c.lex = max(c.lex, lex[i])
		c.pathScore = max(c.pathScore, p)
		c.symbol = max(c.symbol, sym)
	}
	for _, rank := range [][]int{rankByValues(candidates, lex), rankByValues(candidates, structural)} {
		for r, idx := range rank {
			fused[idx] += 1.0 / float64(rrfK+r+1)
		}
	}
}
