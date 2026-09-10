package indexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Prompt enhancer backing the ACE /prompt-enhancer endpoint (the ace-tool-rs
// enhance_prompt tool). The client sends only the raw prompt; the server is
// responsible for the enhancement instruction and the codebase context, which
// here comes from BCE's own retrieval over the most recent snapshot and a
// configurable OpenAI-compatible chat model (ollama serves this protocol).

type EnhanceConfig struct {
	URL   string
	Model string
	// APIKeys mirrors EmbeddingConfig.APIKeys: dedicated credential(s) for
	// the enhancer provider, multi-key values round-robin per request. Falls
	// back to the legacy shared model_api_key when unset.
	APIKeys []string
	// SummaryModel serves the high-volume chunk-summary path: summaries send
	// every chunk's code through the LLM, so a small free-tier model here cuts
	// most of the enhancer bill. Empty falls back to Model.
	SummaryModel string
	// SummaryBudget caps LLM summary calls per embedding round (batches of
	// summarizeBatch chunks, best-scored first). 0 disables the LLM tier
	// entirely — rule-based file descriptions still apply. enhanceConfig
	// resolves it, so the value here is always concrete.
	SummaryBudget int
}

// DefaultSummaryBudget is the per-round LLM call cap when summary_budget is
// not configured: enough for every manifest/config file of a typical project
// while keeping worst-case cost at ~10 chat calls.
const DefaultSummaryBudget = 10

func (c EnhanceConfig) enabled() bool { return c.URL != "" && c.Model != "" }

func (c EnhanceConfig) apiKey() string { return pickAPIKey(c.APIKeys) }

func (c EnhanceConfig) summaryModel() string {
	if c.SummaryModel != "" {
		return c.SummaryModel
	}
	return c.Model
}

// summariesEnabled gates the LLM summary tier: the configured value "none"
// or a zero summary_budget switches it off. Rule-based file descriptions are
// independent of this gate and always apply.
func (c EnhanceConfig) summariesEnabled() bool {
	return c.enabled() && !strings.EqualFold(c.summaryModel(), "none") && c.SummaryBudget > 0
}

func (s *Service) enhanceConfig(ctx context.Context) EnhanceConfig {
	values, _ := s.store.GetSettings(ctx)
	get := func(key, fallback string) string {
		if v := values[key]; v != "" {
			return v
		}
		return fallback
	}
	budget := DefaultSummaryBudget
	if v := strings.TrimSpace(values["summary_budget"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			budget = n
		}
	}
	cfg := EnhanceConfig{
		URL:           get("enhancer_url", s.defaults.EnhancerURL),
		Model:         get("enhancer_model", s.defaults.EnhancerModel),
		APIKeys:       splitAPIKeys(get("model_api_key", s.defaults.ModelAPIKey)),
		SummaryModel:  get("summary_model", s.defaults.SummaryModel),
		SummaryBudget: budget,
	}
	// Same credential resolution as the embedding/rerank paths: dedicated
	// key(s) win, the legacy shared model_api_key is only a fallback.
	if keys := splitAPIKeys(values["enhancer_api_key"]); len(keys) > 0 {
		cfg.APIKeys = keys
	}
	return cfg
}

// contextSnapshot picks the snapshot the enhancer grounds its context in: the
// user's own last-resolved checkpoint when the caller used a personal ACE
// token, otherwise the globally most recent snapshot (shared-token /
// single-tenant behavior).
func (s *Service) contextSnapshot(ctx context.Context, userID string) (domain.Snapshot, error) {
	if userID != "" {
		if id, err := s.store.UserCheckpoint(ctx, userID); err == nil {
			if snapshot, err := s.store.SnapshotByID(ctx, id); err == nil {
				return snapshot, nil
			}
		}
	}
	return s.store.LatestSnapshot(ctx)
}

// ChatTurn mirrors the chat_history entries of the ACE prompt-enhancer
// request.
type ChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const (
	enhanceContextBudget = 8000 // token budget for the retrieval context block
	enhanceHistoryTurns  = 6
	enhanceTTL           = 90 * time.Second
)

const enhanceSystemPrompt = `You are a prompt enhancement assistant for a software engineering AI agent. Rewrite the user's prompt into a clear, specific, immediately actionable request.

Rules:
- Preserve the user's intent and language (answer in the same language as the original prompt).
- Use the codebase context, when provided, to reference concrete files, symbols and behaviors instead of vague descriptions.
- Add missing but inferable details: affected files, expected behavior, constraints.
- Keep it concise; do not invent requirements that are not implied.
- Respond with ONLY the enhanced prompt text, no preamble, no explanations, no markdown fences.`

// EnhancePrompt rewrites a raw user prompt via the configured chat model,
// grounding it in retrieval results from the caller's own last checkpoint
// (personal ACE tokens record one per retrieval call); callers without an
// attributed checkpoint fall back to the globally most recent snapshot.
// Retrieval failures degrade to enhancement without context; only a missing
// configuration or a failing chat model returns an error.
func (s *Service) EnhancePrompt(ctx context.Context, userID, prompt string, history []ChatTurn) (string, error) {
	cfg := s.enhanceConfig(ctx)
	if !cfg.enabled() {
		return "", fmt.Errorf("prompt enhancer is not configured: set enhancer_url and enhancer_model")
	}
	contextBlock := ""
	if snapshot, err := s.contextSnapshot(ctx, userID); err == nil && len(snapshot.BlobNames) > 0 {
		if result, err := s.SearchBlobNames(ctx, prompt, snapshot.BlobNames, enhanceContextBudget); err == nil && len(result.Hits) > 0 {
			contextBlock = result.FormattedRetrieval
		}
	}
	messages := []map[string]string{{"role": "system", "content": enhanceSystemPrompt}}
	if len(history) > enhanceHistoryTurns {
		history = history[len(history)-enhanceHistoryTurns:]
	}
	for _, turn := range history {
		role := turn.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		if strings.TrimSpace(turn.Content) == "" {
			continue
		}
		messages = append(messages, map[string]string{"role": role, "content": turn.Content})
	}
	userContent := prompt
	if contextBlock != "" {
		userContent = "Relevant codebase context:\n" + contextBlock + "\n\nOriginal prompt to enhance:\n" + prompt
	} else {
		userContent = "Original prompt to enhance:\n" + prompt
	}
	messages = append(messages, map[string]string{"role": "user", "content": userContent})

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
	cctx, cancel := context.WithTimeout(ctx, enhanceTTL)
	defer cancel()
	// enable_thinking=false: prompt rewriting needs no chain-of-thought, and
	// Qwen3-family hybrid models otherwise bill/emit reasoning tokens first.
	body := map[string]any{"model": cfg.Model, "messages": messages, "stream": false, "enable_thinking": false}
	if err := postJSON(cctx, url+"/chat/completions", cfg.apiKey(), body, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("enhancer model returned no content")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}
