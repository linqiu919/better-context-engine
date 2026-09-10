package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"unicode"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// BM25 lexical path over code-aware tokens. Identifiers are split on
// camelCase/snake_case boundaries and each long token also emits a 6-char
// prefix term ("authen*"), so morphological variants (authenticate vs.
// authentication) meet at a shared term without a stemmer.

const (
	bm25K1          = 1.2
	bm25B           = 0.75
	prefixTermMin   = 7
	prefixTermChars = 6
	tfCacheLimit    = 100_000
)

type chunkStats struct {
	counts map[string]int
	length int
}

// literalTokens extracts route/URL-like literals as whole terms: runs of
// path characters containing at least one '/' and one letter become a single
// lowercased token ("/api/v1/overview" -> "api/v1/overview"), so an exact
// path in the query lands on its handler with full-term precision instead of
// dissolving into the generic api/overview word soup. Both index and query
// sides pass through codeTokens, so the normalization stays symmetric.
func literalTokens(value string) []string {
	out := []string{}
	var run []rune
	hasSlash, hasLetter := false, false
	flush := func() {
		if hasSlash && hasLetter && len(run) >= 4 {
			tok := strings.Trim(strings.ToLower(string(run)), "/.:*")
			if strings.Contains(tok, "/") {
				out = append(out, tok)
			}
		}
		run = run[:0]
		hasSlash, hasLetter = false, false
	}
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-.{}:*", r):
			if unicode.IsLetter(r) {
				hasLetter = true
			}
			run = append(run, r)
		case r == '/':
			hasSlash = true
			run = append(run, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// codeTokens lowercases and splits text into identifier-aware terms: the full
// identifier, its camel/snake subwords, a prefix term for long tokens, and
// whole-path literal terms for route/URL-like sequences. Spaceless-script
// spans (Han/kana/Thai — see spacelessScripts) carry no word boundaries, so
// they additionally emit overlapping bigrams (the classic CJK-analyzer move):
// that is what lets a Chinese query meet a Chinese comment at a shared term
// instead of both collapsing into distinct opaque whole-run tokens.
func codeTokens(value string) []string {
	out := []string{}
	emit := func(token string) {
		if len([]rune(token)) < 2 {
			return
		}
		out = append(out, token)
		runes := []rune(token)
		if len(runes) >= prefixTermMin {
			out = append(out, string(runes[:prefixTermChars])+"*")
		}
	}
	// emitWord: identifier segment — whole term + camelCase/digit subwords.
	emitWord := func(word []rune) {
		whole := strings.ToLower(string(word))
		emit(whole)
		start := 0
		for i := 1; i <= len(word); i++ {
			boundary := i == len(word) ||
				(unicode.IsUpper(word[i]) && !unicode.IsUpper(word[i-1])) ||
				(unicode.IsDigit(word[i]) != unicode.IsDigit(word[i-1]))
			if !boundary {
				continue
			}
			sub := strings.ToLower(string(word[start:i]))
			if sub != whole {
				emit(sub)
			}
			start = i
		}
	}
	// emitSpaceless: bigrams do the matching; the whole run is kept (no
	// prefix term) so exact whole-run terms in pre-bigram postings still hit.
	emitSpaceless := func(word []rune) {
		if len(word) < 2 {
			return
		}
		if len(word) > 2 {
			out = append(out, string(word))
		}
		for i := 0; i+2 <= len(word); i++ {
			out = append(out, string(word[i:i+2]))
		}
	}
	spaceless := func(r rune) bool { return unicode.In(r, spacelessScripts...) }
	var word []rune
	flush := func() {
		if len(word) == 0 {
			return
		}
		// Split the run at script boundaries: "处理login" is one letter run
		// but two vocabularies.
		segStart := 0
		segSpaceless := spaceless(word[0])
		for i := 1; i <= len(word); i++ {
			if i < len(word) && spaceless(word[i]) == segSpaceless {
				continue
			}
			if seg := word[segStart:i]; segSpaceless {
				emitSpaceless(seg)
			} else {
				emitWord(seg)
			}
			if i < len(word) {
				segStart = i
				segSpaceless = spaceless(word[i])
			}
		}
		word = word[:0]
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
			continue
		}
		flush()
	}
	flush()
	return append(out, literalTokens(value)...)
}

func newChunkStats(text string) *chunkStats {
	tokens := codeTokens(text)
	counts := make(map[string]int, len(tokens))
	for _, t := range tokens {
		counts[t]++
	}
	return &chunkStats{counts: counts, length: len(tokens)}
}

// chunkStatsFor tokenizes a candidate on the fly, memoized per chunk ID. This
// is the fallback for chunks without persisted postings; the cache is dropped
// wholesale past tfCacheLimit rather than evicted (chunk IDs are content-
// addressed, so stale entries can only waste memory, never lie).
func (s *Service) chunkStatsFor(c *candidate) *chunkStats {
	s.tfMu.Lock()
	if s.tfCache == nil || len(s.tfCache) > tfCacheLimit {
		s.tfCache = map[string]*chunkStats{}
	}
	stats, ok := s.tfCache[c.chunk.ID]
	s.tfMu.Unlock()
	if ok {
		return stats
	}
	stats = newChunkStats(c.content)
	s.tfMu.Lock()
	s.tfCache[c.chunk.ID] = stats
	s.tfMu.Unlock()
	return stats
}

// indexPostings computes the persisted inverted-index rows for freshly
// chunked blob content, stamps each chunk's TokenCount, and fingerprints each
// chunk with its content hash (path + chunk text) — the key that lets edits
// which leave a chunk untouched reuse its embedding vector instead of paying
// the embedding API again for every chunk of the file.
func indexPostings(path, content string, chunks []domain.Chunk) ([]domain.Chunk, []domain.ChunkPosting) {
	postings := []domain.ChunkPosting{}
	for i := range chunks {
		text := chunkText(content, chunks[i])
		chunks[i].ContentHash = chunkContentHash(path, text)
		stats := newChunkStats(text)
		chunks[i].TokenCount = stats.length
		for term, tf := range stats.counts {
			postings = append(postings, domain.ChunkPosting{ChunkID: chunks[i].ID, Term: term, TF: tf})
		}
	}
	return chunks, postings
}

// chunkContentHash fingerprints (path, chunk text). The path is part of the
// key because the embedding document embeds the logical path; the same text
// under a different path is a different embedding input. Line numbers and
// blob names deliberately are not part of the key: moving a chunk within a
// file or editing elsewhere in the file must not invalidate its vector.
func chunkContentHash(path, text string) string {
	sum := sha256.Sum256([]byte(path + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// bm25Score fills each candidate's lexical score for the query terms; the
// value-returning core below serves sub-query scoring without clobbering the
// primary query's per-candidate scores.
func (s *Service) bm25Score(ctx context.Context, candidates []*candidate, queryTerms []string) {
	for i, v := range s.bm25Values(ctx, candidates, queryTerms) {
		candidates[i].lex = v
	}
}

// bm25Values computes BM25 scores for the query terms. The corpus is the
// candidate set itself, so idf adapts to the searched snapshot. Term
// frequencies come from the persisted inverted index; chunks that predate it
// (TokenCount == 0) fall back to on-the-fly tokenization.
func (s *Service) bm25Values(ctx context.Context, candidates []*candidate, queryTerms []string) []float64 {
	values := make([]float64, len(candidates))
	if len(candidates) == 0 {
		return values
	}
	if len(queryTerms) == 0 {
		for i := range values {
			values[i] = .1
		}
		return values
	}
	terms := map[string]bool{}
	termList := []string{}
	for _, t := range queryTerms {
		if !terms[t] {
			terms[t] = true
			termList = append(termList, t)
		}
	}
	indexedIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.chunk.TokenCount > 0 {
			indexedIDs = append(indexedIDs, c.chunk.ID)
		}
	}
	postings := map[string]map[string]int{}
	usePostings := false
	if len(indexedIDs) > 0 {
		if fetched, err := s.store.ChunkPostings(ctx, termList, indexedIDs); err == nil {
			postings, usePostings = fetched, true
		}
	}
	type termSource struct {
		counts map[string]int // nil when served by the inverted index
		length int
	}
	sources := make([]termSource, len(candidates))
	totalLength := 0
	for i, c := range candidates {
		if usePostings && c.chunk.TokenCount > 0 {
			sources[i] = termSource{length: c.chunk.TokenCount}
		} else {
			st := s.chunkStatsFor(c)
			sources[i] = termSource{counts: st.counts, length: st.length}
		}
		totalLength += sources[i].length
	}
	tfOf := func(i int, term string) int {
		if sources[i].counts != nil {
			return sources[i].counts[term]
		}
		return postings[term][candidates[i].chunk.ID]
	}
	avgLength := float64(totalLength) / float64(len(candidates))
	if avgLength == 0 {
		avgLength = 1
	}
	n := float64(len(candidates))
	for _, term := range termList {
		df := 0
		for i := range candidates {
			if tfOf(i, term) > 0 {
				df++
			}
		}
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (n-float64(df)+0.5)/(float64(df)+0.5))
		for i := range candidates {
			tf := float64(tfOf(i, term))
			if tf == 0 {
				continue
			}
			norm := tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(sources[i].length)/avgLength))
			values[i] += idf * norm
		}
	}
	return values
}
