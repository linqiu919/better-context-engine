package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Model connectivity probes behind the console "Test" buttons. They take the
// admin's unsaved form values, resolve credentials exactly like the runtime
// config getters, and go through the same clients the live paths use, so a
// pass means indexing/search/enhancement will actually work with these
// values. Nothing is persisted and no cooldown state is touched.

// ModelCheck is one probed capability (embedding, reranker, enhancer, ...).
type ModelCheck struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	OK     bool   `json:"ok"`
	Ms     int64  `json:"ms"`
	Detail string `json:"detail"`
}

const modelTestTTL = 20 * time.Second

// probeKeys mirrors the runtime credential order: dedicated key(s) from the
// form win, the legacy shared model_api_key (stored or env) is the fallback.
func (s *Service) probeKeys(ctx context.Context, dedicated string) []string {
	if keys := splitAPIKeys(dedicated); len(keys) > 0 {
		return keys
	}
	values, _ := s.store.GetSettings(ctx)
	if v := values["model_api_key"]; v != "" {
		return splitAPIKeys(v)
	}
	return splitAPIKeys(s.defaults.ModelAPIKey)
}

func runCheck(ctx context.Context, name, model string, probe func(context.Context) (string, error)) ModelCheck {
	check := ModelCheck{Name: name, Model: model}
	if strings.TrimSpace(model) == "" {
		check.Detail = "model is not configured"
		return check
	}
	cctx, cancel := context.WithTimeout(ctx, modelTestTTL)
	defer cancel()
	started := time.Now()
	detail, err := probe(cctx)
	check.Ms = time.Since(started).Milliseconds()
	if err != nil {
		check.Detail = err.Error()
		return check
	}
	check.OK, check.Detail = true, detail
	return check
}

// TestSemanticModels probes the embedding model and, when enabled, the
// reranker sharing its endpoint and key.
func (s *Service) TestSemanticModels(ctx context.Context, rs domain.RetrievalSettings) []ModelCheck {
	keys := s.probeKeys(ctx, rs.EmbeddingAPIKey)
	if strings.TrimSpace(rs.EmbeddingURL) == "" {
		return []ModelCheck{{Name: "embedding", Model: rs.EmbeddingModel, Detail: "service URL is not configured"}}
	}
	ecfg := EmbeddingConfig{Provider: rs.EmbeddingProvider, URL: rs.EmbeddingURL, Model: rs.EmbeddingModel, APIKeys: keys, Dimensions: rs.EmbeddingDimensions}
	checks := []ModelCheck{runCheck(ctx, "embedding", rs.EmbeddingModel, func(ctx context.Context) (string, error) {
		vector, err := embedQuery(ctx, ecfg, "where is the http request handler")
		if err != nil {
			return "", err
		}
		// Vectors land in a fixed-width pgvector column, so a dimension
		// mismatch is a broken config even though the API call succeeded.
		if rs.EmbeddingDimensions > 0 && len(vector) != rs.EmbeddingDimensions {
			return "", fmt.Errorf("model returned %d dimensions, configured %d", len(vector), rs.EmbeddingDimensions)
		}
		return fmt.Sprintf("%d dimensions", len(vector)), nil
	})}
	if rs.RerankerEnabled {
		rcfg := RerankConfig{Enabled: true, URL: rs.EmbeddingURL, Model: rs.RerankerModel, APIKeys: keys}
		checks = append(checks, runCheck(ctx, "reranker", rs.RerankerModel, func(ctx context.Context) (string, error) {
			scores, err := rerankDocuments(ctx, rcfg, "parse json request body", []string{
				"func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }",
				"body { margin: 0; font-family: sans-serif; }",
			})
			if err != nil {
				return "", err
			}
			if len(scores) != 2 {
				return "", fmt.Errorf("expected 2 scores, got %d", len(scores))
			}
			return fmt.Sprintf("scores %.3f / %.3f", scores[0], scores[1]), nil
		}))
	}
	return checks
}

// TestLanguageModels sends one tiny chat completion to the enhancer model and
// the summary model (when distinct and not "none"), with the same thinking
// and OpenRouter provider options the live calls carry.
func (s *Service) TestLanguageModels(ctx context.Context, rs domain.RetrievalSettings) []ModelCheck {
	if strings.TrimSpace(rs.EnhancerURL) == "" {
		return []ModelCheck{{Name: "enhancer", Model: rs.EnhancerModel, Detail: "service URL is not configured"}}
	}
	cfg := EnhanceConfig{URL: rs.EnhancerURL, Model: rs.EnhancerModel, APIKeys: s.probeKeys(ctx, rs.EnhancerAPIKey), Providers: ParseProviderList(strings.Join(rs.EnhancerProviders, ","))}
	chat := func(model string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			url := strings.TrimSuffix(cfg.URL, "/")
			if !strings.HasSuffix(url, "/v1") {
				url += "/v1"
			}
			var response struct {
				Model   string `json:"model"`
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			body := map[string]any{
				"model":      model,
				"messages":   []map[string]string{{"role": "user", "content": "Reply with the single word: ok"}},
				"stream":     false,
				"max_tokens": 16,
			}
			cfg.applyChatOptions(body, url)
			if err := postJSON(ctx, url+"/chat/completions", cfg.apiKey(), body, &response); err != nil {
				return "", err
			}
			if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
				return "", fmt.Errorf("model returned no content (is reasoning/thinking still on?)")
			}
			reply := strings.TrimSpace(response.Choices[0].Message.Content)
			if len(reply) > 40 {
				reply = reply[:40] + "…"
			}
			return fmt.Sprintf("replied %q", reply), nil
		}
	}
	checks := []ModelCheck{runCheck(ctx, "enhancer", rs.EnhancerModel, chat(rs.EnhancerModel))}
	summary := strings.TrimSpace(rs.SummaryModel)
	if summary != "" && !strings.EqualFold(summary, "none") && summary != rs.EnhancerModel {
		checks = append(checks, runCheck(ctx, "summary", summary, chat(summary)))
	}
	return checks
}
