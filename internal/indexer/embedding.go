package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EmbeddingConfig is resolved per call from admin retrieval settings, falling
// back to environment defaults, so the console can switch models at runtime.
type EmbeddingConfig struct {
	Provider string
	URL      string
	Model    string
	// APIKeys holds one or more credentials for the embedding/rerank provider.
	// Multiple keys (newline/comma-separated in settings) are round-robined
	// per HTTP request so heavy backfill embedding and search-time rerank
	// spread across the accounts' separate TPM pools instead of exhausting one.
	APIKeys    []string
	Dimensions int
}

func (c EmbeddingConfig) enabled() bool { return c.URL != "" && c.Model != "" }
func (c EmbeddingConfig) apiKey() string { return pickAPIKey(c.APIKeys) }

// keyCursor is the global round-robin position over multi-key credential
// lists. One counter for both embedding and rerank requests: each Voyage-style
// account meters embed and rerank against the same TPM pool, so interleaving
// the two paths across keys is exactly the point.
var keyCursor atomic.Uint64

func pickAPIKey(keys []string) string {
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return keys[0]
	}
	return keys[keyCursor.Add(1)%uint64(len(keys))]
}

// splitAPIKeys parses a settings value holding one or more API keys separated
// by newlines, commas or whitespace (keys never contain any of those).
func splitAPIKeys(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (s *Service) embeddingConfig(ctx context.Context) EmbeddingConfig {
	values, _ := s.store.GetSettings(ctx)
	get := func(key, fallback string) string {
		if v := values[key]; v != "" {
			return v
		}
		return fallback
	}
	cfg := EmbeddingConfig{
		Provider: get("embedding_provider", s.defaults.EmbeddingProvider),
		URL:      get("embedding_url", s.defaults.EmbeddingURL),
		Model:    get("embedding_model", s.defaults.EmbeddingModel),
		APIKeys:  splitAPIKeys(get("model_api_key", s.defaults.ModelAPIKey)),
	}
	// Dedicated credential(s) for the embedding/rerank provider (both paths
	// share them — see rerankConfig); empty keeps the shared model key.
	if keys := splitAPIKeys(values["embedding_api_key"]); len(keys) > 0 {
		cfg.APIKeys = keys
	}
	cfg.Dimensions = s.defaults.EmbeddingDimensions
	return cfg
}

const (
	embedBatchSize = 16
	embedMaxChars  = 24000
	// Batches within one embedTexts call fly in parallel up to this cap: the
	// cloud embedding provider tolerates far more concurrency than a serial
	// batch chain uses, and the first-search inline backfill was spending its
	// whole budget waiting on 16 sequential round trips.
	embedBatchConcurrency = 8
	// Backfill commit granularity: vectors persist per group of this many
	// chunks, with this many groups embedding in parallel.
	embedCommitSize        = 64
	embedCommitConcurrency = 3
)

// Shared client for all three model paths (embed / rerank / chat). Broad mode
// fans out up to 5 parallel rerank calls plus embedding batches against the
// same model host; the default transport keeps only 2 idle connections per
// host, so every burst paid fresh TLS handshakes to the cloud endpoint.
var embedHTTP = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// embedTexts returns one L2-normalized vector per input, preserving order.
// All bulk paths embed documents; the query side goes through embedQuery.
func embedTexts(ctx context.Context, cfg EmbeddingConfig, inputs []string) ([][]float32, error) {
	return embedTextsKind(ctx, cfg, inputs, "document")
}

func embedTextsKind(ctx context.Context, cfg EmbeddingConfig, inputs []string, kind string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, embedBatchConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel() // one failed batch aborts the rest; the call is all-or-nothing
	}
	for start := 0; start < len(inputs) && cctx.Err() == nil; start += embedBatchSize {
		end := min(start+embedBatchSize, len(inputs))
		batch := make([]string, 0, end-start)
		for _, text := range inputs[start:end] {
			if len(text) > embedMaxChars {
				text = text[:embedMaxChars]
			}
			if strings.TrimSpace(text) == "" {
				text = " "
			}
			batch = append(batch, text)
		}
		select {
		case sem <- struct{}{}:
		case <-cctx.Done():
			continue
		}
		wg.Add(1)
		go func(start int, batch []string) {
			defer wg.Done()
			defer func() { <-sem }()
			vectors, err := embedBatch(cctx, cfg, batch, kind)
			if err == nil && len(vectors) != len(batch) {
				err = fmt.Errorf("embedding server returned %d vectors for %d inputs", len(vectors), len(batch))
			}
			if err != nil {
				fail(err)
				return
			}
			for i, v := range vectors {
				out[start+i] = v
			}
		}(start, batch)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, v := range out {
		normalize(v)
	}
	return out, nil
}

// embedQuery embeds the search query. Instruction-aware embedding models take
// a task prefix on the query side only (documents embed bare), and skipping it
// costs real recall — the Nomic code embedders were trained with it mandatory.
func embedQuery(ctx context.Context, cfg EmbeddingConfig, query string) ([]float32, error) {
	model := strings.ToLower(cfg.Model)
	switch {
	case strings.Contains(model, "qwen3-embedding"):
		query = "Instruct: Given a natural language question about a codebase, retrieve the most relevant source code snippets\nQuery: " + query
	case strings.Contains(model, "nomic-embed-code") || strings.Contains(model, "coderankembed"):
		query = "Represent this query for searching relevant code: " + query
	}
	// Voyage models take no text prefix; the query/document asymmetry rides
	// the input_type field instead (see embedOpenAI).
	vectors, err := embedTextsKind(ctx, cfg, []string{query}, "query")
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func embedBatch(ctx context.Context, cfg EmbeddingConfig, inputs []string, kind string) ([][]float32, error) {
	if strings.EqualFold(cfg.Provider, "ollama") {
		return embedOllama(ctx, cfg, inputs)
	}
	return embedOpenAI(ctx, cfg, inputs, kind)
}

func embedOllama(ctx context.Context, cfg EmbeddingConfig, inputs []string) ([][]float32, error) {
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	url := strings.TrimSuffix(cfg.URL, "/") + "/api/embed"
	if err := postJSON(ctx, url, cfg.apiKey(), map[string]any{"model": cfg.Model, "input": inputs}, &response); err != nil {
		return nil, err
	}
	return response.Embeddings, nil
}

func embedOpenAI(ctx context.Context, cfg EmbeddingConfig, inputs []string, kind string) ([][]float32, error) {
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	url := strings.TrimSuffix(cfg.URL, "/")
	if !strings.HasSuffix(url, "/v1") {
		url += "/v1"
	}
	body := map[string]any{"model": cfg.Model, "input": inputs}
	// Voyage carries the query/document retrieval asymmetry in input_type
	// rather than a text prefix. Gated on the model family: other
	// OpenAI-compatible servers may reject unknown fields.
	if kind != "" && strings.HasPrefix(strings.ToLower(cfg.Model), "voyage") {
		body["input_type"] = kind
	}
	if err := postJSON(ctx, url+"/embeddings", cfg.apiKey(), body, &response); err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(response.Data))
	for _, d := range response.Data {
		out = append(out, d.Embedding)
	}
	return out, nil
}

// postJSON is the shared HTTP client for all model backends (embedding,
// reranker, enhancer). A non-empty apiKey is sent as a Bearer token so cloud
// platforms (SiliconFlow, OpenAI, ...) work; local servers ignore the header.
func postJSON(ctx context.Context, url, apiKey string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := embedHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("model server %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(output)
}

func normalize(v []float32) {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

func dot(a, b []float32) float64 {
	n := min(len(a), len(b))
	var sum float64
	for i := range n {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
