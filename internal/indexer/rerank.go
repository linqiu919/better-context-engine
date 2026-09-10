package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reranker integration. Talks to a Jina/Cohere-compatible /rerank endpoint;
// vLLM exposes this shape when serving cross-encoder models such as
// qwen3-reranker-0.6b.

type RerankConfig struct {
	Enabled bool
	URL     string
	Model   string
	// APIKeys mirrors EmbeddingConfig.APIKeys: one provider serves both
	// models, and multi-key values round-robin per request (see pickAPIKey).
	APIKeys []string
	TopK    int
}

func (c RerankConfig) apiKey() string { return pickAPIKey(c.APIKeys) }

func (c RerankConfig) enabled() bool { return c.Enabled && c.URL != "" && c.Model != "" }

// rerankConfig shares the embedding provider's endpoint and credential — one
// platform serves both models — so only the model name and top-K are its own.
func (s *Service) rerankConfig(ctx context.Context) RerankConfig {
	values, _ := s.store.GetSettings(ctx)
	get := func(key, fallback string) string {
		if v := values[key]; v != "" {
			return v
		}
		return fallback
	}
	enabled := true
	if v := values["reranker_enabled"]; v != "" {
		enabled, _ = strconv.ParseBool(v)
	}
	topK := s.defaults.RerankerTopK
	if v, err := strconv.Atoi(values["reranker_top_k"]); err == nil && v > 0 {
		topK = v
	}
	if topK <= 0 {
		topK = 24
	}
	cfg := RerankConfig{
		Enabled: enabled,
		URL:     get("embedding_url", s.defaults.EmbeddingURL),
		Model:   get("reranker_model", s.defaults.RerankerModel),
		APIKeys: splitAPIKeys(get("model_api_key", s.defaults.ModelAPIKey)),
		TopK:    topK,
	}
	// Same credential resolution as the embedding path: the dedicated key(s)
	// of the shared provider win, empty falls back to the global model key.
	if keys := splitAPIKeys(values["embedding_api_key"]); len(keys) > 0 {
		cfg.APIKeys = keys
	}
	return cfg
}

const (
	rerankDocMaxChars = 4000
	// Generous enough for cloud-hosted 8B rerankers over a full top-K batch;
	// a too-tight TTL turns network jitter into repeated 30s cooldowns.
	rerankTTL      = 15 * time.Second
	rerankCooldown = 30 * time.Second
	// Candidates scoring below this fraction of the head score are dropped
	// outright: top-K is a ceiling, not a quota to fill. Lexically-similar
	// noise (every function mentioning "mail") lands here.
	rerankCutoffRatio = 0.35
	// Absolute thresholds on top of the relative cutoff. When the query has no
	// real answer in this codebase the head score itself is low, so the 35%
	// line lets weak noise through; scores are Jina/Cohere-calibrated 0..1, so
	// an absolute floor is meaningful. Below rerankWeakTop the whole result is
	// flagged low-confidence — the caller may be searching for code that lives
	// in another repository.
	rerankFloorScore = 0.10
	rerankWeakTop    = 0.30
	// The cutoff never truncates below this many results: a handful of weak
	// matches plus the low-confidence note beats an answer so short it reads
	// as "nothing else exists". Broad queries keep a wider floor and a gentler
	// ratio — coverage is the point there.
	rerankMinKeep         = 6
	rerankBroadMinKeep    = 8
	rerankBroadCutoff     = 0.25
	rerankBroadFloorScore = 0.05
	// First-pass head score below which retrieval re-runs in broad mode: no
	// chunk directly answers the query, so the answer is either spread across
	// many files (exploratory question) or absent. Sits well above
	// rerankWeakTop — the moderate band prefers wider candidate coverage.
	broadEngageTop = 0.50
)

type rerankResult struct {
	Index          int      `json:"index"`
	RelevanceScore *float64 `json:"relevance_score"`
	Score          *float64 `json:"score"`
}

func rerankDocuments(ctx context.Context, cfg RerankConfig, query string, docs []string) ([]float64, error) {
	url := strings.TrimSuffix(cfg.URL, "/")
	if !strings.HasSuffix(url, "/rerank") {
		url += "/rerank"
	}
	// Jina/Cohere-shaped servers answer in "results", Voyage in "data"; every
	// provider defaults to scoring all documents, so no top_n/top_k is sent —
	// the parameter is even spelled differently between the two families.
	var response struct {
		Results []rerankResult `json:"results"`
		Data    []rerankResult `json:"data"`
	}
	body := map[string]any{"model": cfg.Model, "query": query, "documents": docs}
	if err := postJSON(ctx, url, cfg.apiKey(), body, &response); err != nil {
		return nil, err
	}
	results := response.Results
	if len(results) == 0 {
		results = response.Data
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("rerank server returned no results")
	}
	scores := make([]float64, len(docs))
	for _, r := range results {
		if r.Index < 0 || r.Index >= len(docs) {
			continue
		}
		switch {
		case r.RelevanceScore != nil:
			scores[r.Index] = *r.RelevanceScore
		case r.Score != nil:
			scores[r.Index] = *r.Score
		}
	}
	return scores, nil
}

// applyRerank reorders the head of the fused ranking by cross-encoder
// relevance and returns the (possibly truncated) order plus the head score.
// reranked is false whenever the cross-encoder did not run, in which case
// topScore carries no signal. Failures never break search: the fused order
// stands and the reranker is put on cooldown like the embedding path.
//
// Broad queries score each document against the original query AND every
// sub-query in parallel, keeping the per-document maximum: a manifest that
// looks irrelevant next to "overall frontend style" scores high against the
// "package.json dependencies ui framework" sub-query, and a single-query
// cutoff would silently re-drop exactly the coverage decomposition added.
func (s *Service) applyRerank(ctx context.Context, query string, subQueries []string, candidates []*candidate, order []int, broad bool, budget time.Duration) (kept []int, topScore float64, reranked bool) {
	cfg := s.rerankConfig(ctx)
	if !cfg.enabled() || len(order) == 0 {
		return order, 0, false
	}
	// budget is what the search latency budget can spare for this round; the
	// TTL stays the ceiling. A round with almost no time left is not worth
	// firing — falling back to the fused order beats a guaranteed timeout.
	budget = min(budget, rerankTTL)
	if budget < rerankMinBudget {
		return order, 0, false
	}
	s.embedMu.Lock()
	down := time.Now().Before(s.rerankDownUntil)
	s.embedMu.Unlock()
	if down {
		return order, 0, false
	}
	topK := min(cfg.TopK, len(order))
	docs := make([]string, topK)
	for i := range topK {
		c := candidates[order[i]]
		doc := c.content
		if len(doc) > rerankDocMaxChars {
			doc = doc[:rerankDocMaxChars]
		}
		// The cross-encoder sees the same enrichment the embedding did: path
		// plus the persisted chunk summary, so a stylesheet can score against
		// "overall frontend style" through its description, not its CSS.
		header := logicalPath(c.path)
		if c.chunk.Summary != "" {
			header += " — " + c.chunk.Summary
		}
		docs[i] = header + "\n" + doc
	}
	rctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	queries := append([]string{query}, subQueries...)
	batches := make([][]float64, len(queries))
	var wg sync.WaitGroup
	for qi, q := range queries {
		wg.Add(1)
		go func(qi int, q string) {
			defer wg.Done()
			if scores, err := rerankDocuments(rctx, cfg, q, docs); err == nil && len(scores) == len(docs) {
				batches[qi] = scores
			}
		}(qi, q)
	}
	wg.Wait()
	merged := make([]float64, len(docs))
	anyOK := false
	for _, scores := range batches {
		if scores == nil {
			continue
		}
		anyOK = true
		for i, v := range scores {
			if v > merged[i] {
				merged[i] = v
			}
		}
	}
	if !anyOK {
		// Only a failure the reranker's own full TTL would also have seen arms
		// the cooldown: a genuine API error (context still live) or a timeout
		// with the full TTL granted. A budget-clipped timeout says nothing
		// about server health and must not bench a healthy reranker for 30s.
		if rctx.Err() == nil || budget >= rerankTTL {
			s.embedMu.Lock()
			s.rerankDownUntil = time.Now().Add(rerankCooldown)
			s.embedMu.Unlock()
		}
		return order, 0, false
	}
	head := append([]int(nil), order[:topK]...)
	for i, idx := range head {
		candidates[idx].rerank = merged[i]
	}
	sort.SliceStable(head, func(i, j int) bool {
		return candidates[head[i]].rerank > candidates[head[j]].rerank
	})
	copy(order, head)
	// Dynamic cutoff: when the cross-encoder marks a clear relevance cliff,
	// everything below it goes — including the un-reranked fused tail, which
	// ranked below the dropped entries to begin with. The absolute floor
	// kicks in when even the head is weak: a low top score would otherwise
	// shelter equally-weak noise behind the relative ratio. A minimum keep
	// count stops the truncation from starving the answer either way.
	top := candidates[head[0]].rerank
	if top <= 0 {
		return order, top, true
	}
	ratio, floor, minKeep := rerankCutoffRatio, rerankFloorScore, rerankMinKeep
	if broad {
		ratio, floor, minKeep = rerankBroadCutoff, rerankBroadFloorScore, rerankBroadMinKeep
	}
	// The minimum-keep floor counts distinct contents, not entries: per-module
	// near-copies (see the basename dedup in curate.go) all earn the same
	// score, and counting each copy would let seven identical pom.xml
	// survivors "keep 8" while starving the pool of everything the diversity
	// pass could actually seat. Copies below the cut always go; a distinct
	// chunk below the cut stays once cutting it would leave fewer than minKeep
	// different contents.
	isDup := make([]bool, topK)
	seenBase := map[string][]map[string]bool{}
	distinct := 0
	for i, idx := range head {
		base := strings.ToLower(filepath.Base(logicalPath(candidates[idx].path)))
		lines := lineSet(candidates[idx].content)
		for _, prev := range seenBase[base] {
			if lineJaccard(lines, prev) >= dupLineJaccardMin {
				isDup[i] = true
				break
			}
		}
		seenBase[base] = append(seenBase[base], lines)
		if !isDup[i] {
			distinct++
		}
	}
	cut := max(top*ratio, floor)
	keep := topK
	for keep > 1 && candidates[head[keep-1]].rerank < cut {
		if !isDup[keep-1] {
			if distinct <= minKeep {
				break
			}
			distinct--
		}
		keep--
	}
	if keep < topK {
		return order[:keep], top, true
	}
	return order, top, true
}
