package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Chunk summarization: generic embedding models miss context that is not
// textually similar — a global stylesheet never resembles the query "overall
// frontend style". A one-sentence LLM summary written at embedding time
// bridges that gap: the embedding text and rerank documents carry
// path + symbol + summary + code, so natural-language queries meet code
// through the summary even when the code itself shares no vocabulary. This
// moves the text↔code gap from inside the model (which would need training)
// into the data — the closest practical stand-in for a custom-trained
// retrieval model.

const (
	summarizeBatch       = 8
	summarizeConcurrency = 3
	summarizeCallTTL     = 60 * time.Second
	// The summarizer shares embedMissing's deadline; past this budget the
	// remaining chunks embed without a summary rather than starving the
	// embedding stage itself.
	summarizeBudget     = 4 * time.Minute
	summarizeSnippetMax = 1200
	summaryMaxChars     = 240
)

var summaryLineNumRE = regexp.MustCompile(`\d+`)

const summarizeSystemPrompt = `You annotate code fragments for a code search index. For each numbered fragment, output exactly one line in the form "<number>: <summary>" — a single English sentence stating what the code does and its role in the project (feature logic, configuration, UI page or styling, data model, external integration, build tooling...). Name the concrete technologies, endpoints and domain concepts involved so natural-language searches can find the fragment. Output nothing but those lines.`

// llmSummaryWorthy gates the paid tier: only config / manifest / style /
// docs files that the free layers could not describe qualify. Code chunks
// never go to the LLM — path+symbol enrichment plus attached doc comments
// already carry their semantics — and rule-described files are filtered out
// earlier via c.Summary != "", so the budget is spent purely on the
// unrecognized config-ish long tail.
func llmSummaryWorthy(path string) bool {
	logical := logicalPath(path)
	if manifestBoost(logical) > 0 {
		return true
	}
	switch strings.ToLower(filepath.Ext(logical)) {
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".properties",
		".xml", ".env", ".css", ".less", ".scss", ".sass", ".styl",
		".md", ".markdown", ".rst":
		return true
	}
	return false
}

// summaryScore ranks eligible chunks by expected retrieval payoff so a small
// budget lands on the most architecture-relevant files first: known manifest
// family, then root-proximate files (a top-level config outranks a deeply
// nested fixture), with anonymous/structural chunks ahead of keyed ones.
func summaryScore(path string, c domain.Chunk) float64 {
	logical := logicalPath(path)
	score := 0.0
	if manifestBoost(logical) > 0 {
		score += 4
	}
	if c.Symbol == "" || c.SymbolKind == "preamble" {
		score += 1
	}
	depth := float64(strings.Count(logical, "/"))
	if depth < 3 {
		score += 3 - depth
	}
	return score
}

// summarizeChunks generates summaries for the chunks that have none, keyed by
// chunk ID. Batches run with bounded concurrency against the enhancer model;
// every failure path just leaves summaries empty — enrichment is an upgrade,
// never a dependency.
func (s *Service) summarizeChunks(ctx context.Context, cfg EnhanceConfig, chunks []domain.Chunk, texts []string, pathOf map[string]string) map[string]string {
	type item struct {
		id    string
		path  string
		text  string
		score float64
	}
	items := []item{}
	for i, c := range chunks {
		if c.Summary != "" || !llmSummaryWorthy(pathOf[c.BlobName]) {
			continue
		}
		text := texts[i]
		if len(text) > summarizeSnippetMax {
			text = text[:summarizeSnippetMax]
		}
		items = append(items, item{id: c.ID, path: logicalPath(pathOf[c.BlobName]), text: text, score: summaryScore(pathOf[c.BlobName], c)})
	}
	if len(items) == 0 {
		return nil
	}
	// The budget caps LLM calls per embedding round; highest-payoff chunks go
	// first, and because summaries persist (content-addressed, checked via
	// c.Summary above) later rounds only ever pay for what earlier rounds
	// could not fit.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].path < items[j].path
	})
	if limit := cfg.SummaryBudget * summarizeBatch; len(items) > limit {
		items = items[:limit]
	}
	url := strings.TrimSuffix(cfg.URL, "/")
	if !strings.HasSuffix(url, "/v1") {
		url += "/v1"
	}
	bctx, cancel := context.WithTimeout(ctx, summarizeBudget)
	defer cancel()

	out := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, summarizeConcurrency)
	for start := 0; start < len(items); start += summarizeBatch {
		if bctx.Err() != nil {
			break
		}
		batch := items[start:min(start+summarizeBatch, len(items))]
		wg.Add(1)
		sem <- struct{}{}
		go func(batch []item) {
			defer wg.Done()
			defer func() { <-sem }()
			var prompt strings.Builder
			for i, it := range batch {
				fmt.Fprintf(&prompt, "### %d — %s\n%s\n\n", i+1, it.path, it.text)
			}
			var response struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			cctx, cancel := context.WithTimeout(bctx, summarizeCallTTL)
			defer cancel()
			body := map[string]any{
				"model":      cfg.summaryModel(),
				"messages":   []map[string]string{{"role": "system", "content": summarizeSystemPrompt}, {"role": "user", "content": prompt.String()}},
				"stream":     false,
				"max_tokens": 80 * len(batch),
				// Qwen3-family hybrid models spend the whole max_tokens budget
				// on <think> reasoning and return empty content without this;
				// providers without the parameter ignore it.
				"enable_thinking": false,
			}
			if err := postJSON(cctx, url+"/chat/completions", cfg.apiKey(), body, &response); err != nil || len(response.Choices) == 0 {
				return
			}
			parsed := parseSummaries(response.Choices[0].Message.Content, len(batch))
			mu.Lock()
			for i, summary := range parsed {
				if summary != "" {
					out[batch[i].id] = summary
				}
			}
			mu.Unlock()
		}(batch)
	}
	wg.Wait()
	return out
}

// parseSummaries maps "N: summary" lines back to batch positions; malformed
// or missing lines leave that position empty. Smaller instruct models drift
// from the exact "N:" format ("For 1 — path: ..."), so the position is the
// first integer anywhere before the separator, not a strict line prefix.
func parseSummaries(content string, n int) []string {
	out := make([]string, n)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexAny(line, ":.)")
		if i <= 0 {
			continue
		}
		num, err := strconv.Atoi(strings.TrimSpace(line[:i]))
		if err != nil {
			if m := summaryLineNumRE.FindString(line[:i]); m != "" {
				num, err = strconv.Atoi(m)
			}
		}
		if err != nil || num < 1 || num > n {
			continue
		}
		summary := strings.TrimSpace(line[i+1:])
		if summary == "" {
			continue
		}
		if len(summary) > summaryMaxChars {
			summary = summary[:summaryMaxChars]
		}
		if out[num-1] == "" {
			out[num-1] = summary
		}
	}
	return out
}

// embedDocText is the enriched embedding document: path and symbol always
// (free, deterministic), the LLM summary when present. The query side embeds
// raw — asymmetry is fine for bi-encoders, and the header is what lets
// "overall frontend style" land near a stylesheet.
func embedDocText(path string, c domain.Chunk, code string) string {
	var b strings.Builder
	b.WriteString(logicalPath(path))
	if c.Symbol != "" {
		b.WriteString("\n")
		if c.SymbolKind != "" {
			b.WriteString(c.SymbolKind)
			b.WriteString(" ")
		}
		b.WriteString(c.Symbol)
	}
	if c.Summary != "" {
		b.WriteString("\n")
		b.WriteString(c.Summary)
	}
	b.WriteString("\n\n")
	b.WriteString(code)
	return b.String()
}
