package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	// Providers are OpenRouter provider slugs preferred in order (see
	// applyChatOptions); ignored for any other endpoint.
	Providers []string
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
		Providers:     ParseProviderList(values["enhancer_providers"]),
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
func (s *Service) contextSnapshot(ctx context.Context, userID string, ws WorkspaceMeta) (domain.Snapshot, error) {
	// A git-aware client names the workspace it is enhancing from; use that
	// project's own checkpoint so a sibling worktree's last search does not
	// supply the context. Falls through to the user's latest checkpoint when
	// the row is unknown or still uploading.
	if userID != "" && ws.Name != "" {
		branch := ""
		if ws.Git {
			branch = clipName(ws.Branch)
		}
		if ref, err := s.ACEProjectRef(ctx, userID, clipName(ws.Name), branch); err == nil && ref.SnapshotID != "" {
			if snapshot, err := s.store.SnapshotByID(ctx, ref.SnapshotID); err == nil {
				return snapshot, nil
			}
		}
	}
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
- Respond with ONLY the enhanced prompt text, no preamble, no explanations.

Output format — plain text, because it is pasted into input boxes that do not render Markdown:
- No Markdown syntax: no # headings, no **bold** or *italics*, no backticks, no code fences, no tables, no > quotes.
- Begin directly with the task itself, with no label or prefix such as "Goal:" in front of it.
- There is no fixed template: organize the answer the way that best fits this particular request. A simple request can stay a short paragraph; a multi-step task may use numbered steps ("1.", "2.", one per line); split into sections only when the request spans several files or constraints, with short headings of your own on their own line. Separate paragraphs or sections with one blank line.
- The whole answer, any headings included, is written in the language of the original prompt — not the language of these instructions or of the codebase context.
- Write file paths, symbols and commands as-is, without quotes or backticks.`

// minEnhanceChars is the fewest letters/digits a prompt needs before it is
// worth enhancing: below it ("1", "a", "ok", "?") retrieval has nothing to
// match and the model can only guess, so the prompt is returned unchanged.
const minEnhanceChars = 3

// PromptTooVague reports whether a prompt carries too little content to
// enhance. Counts letters and digits of any script, so it treats every
// language alike.
func PromptTooVague(prompt string) bool {
	n := 0
	for _, r := range prompt {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if n++; n >= minEnhanceChars {
				return false
			}
		}
	}
	return true
}

type skipQueryLLMKey struct{}

// withoutQueryLLM marks a search whose caller runs its own LLM pass over the
// results (the enhancer), so the query-side LLM helpers — term expansion and
// broad-mode decomposition — are skipped instead of adding a second model
// round trip in front of it.
func withoutQueryLLM(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipQueryLLMKey{}, true)
}

func skipQueryLLM(ctx context.Context) bool {
	skip, _ := ctx.Value(skipQueryLLMKey{}).(bool)
	return skip
}

// EnhancePrompt rewrites a raw user prompt via the configured chat model,
// grounding it in retrieval results from the caller's own last checkpoint
// (personal ACE tokens record one per retrieval call); callers without an
// attributed checkpoint fall back to the globally most recent snapshot.
// Retrieval failures degrade to enhancement without context; only a missing
// configuration or a failing chat model returns an error.
func (s *Service) EnhancePrompt(ctx context.Context, userID string, ws WorkspaceMeta, prompt string, history []ChatTurn) (string, error) {
	cfg := s.enhanceConfig(ctx)
	if !cfg.enabled() {
		return "", fmt.Errorf("prompt enhancer is not configured: set enhancer_url and enhancer_model")
	}
	started := time.Now()
	contextBlock := ""
	if snapshot, err := s.contextSnapshot(ctx, userID, ws); err == nil && len(snapshot.BlobNames) > 0 {
		if result, err := s.SearchBlobNames(withoutQueryLLM(ctx), prompt, snapshot.BlobNames, enhanceContextBudget); err == nil && len(result.Hits) > 0 {
			contextBlock = result.FormattedRetrieval
		}
	}
	contextMS := time.Since(started).Milliseconds()
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
		Provider string `json:"provider"`
		Usage    struct {
			PromptTokens            int `json:"prompt_tokens"`
			CompletionTokens        int `json:"completion_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	cctx, cancel := context.WithTimeout(ctx, enhanceTTL)
	defer cancel()
	// Thinking off: prompt rewriting needs no chain-of-thought, and hybrid
	// models otherwise bill/emit reasoning tokens before any content.
	body := map[string]any{"model": cfg.Model, "messages": messages, "stream": false}
	modelStart := time.Now()
	err := cfg.postChat(cctx, url, body, &response)
	// Stage split for latency triage: context search vs the model call, and
	// how many of the completion tokens were reasoning.
	slog.Info("prompt enhance", "context_ms", contextMS, "context_chars", len(contextBlock),
		"model_ms", time.Since(modelStart).Milliseconds(), "model", cfg.Model, "provider", response.Provider,
		"prompt_tokens", response.Usage.PromptTokens, "completion_tokens", response.Usage.CompletionTokens,
		"reasoning_tokens", response.Usage.CompletionTokensDetails.ReasoningTokens, "failed", err != nil)
	if err != nil {
		return "", err
	}
	if len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("enhancer model returned no content")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}
