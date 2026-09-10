package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Broad-query mode. Exploratory questions ("what is the overall frontend
// style", "which external APIs does the backend integrate") have no single
// answering chunk: the answer is spread across manifests, configs and many
// implementations, and no chunk resembles the query text. Focused-query
// tuning (tight rerank truncation, 12 hits, full chunk bodies) starves such
// queries, so they get their own regime: LLM sub-query decomposition fused
// into the ranking, a structural prior for well-known manifest/config files,
// a relaxed rerank cutoff, and a wide candidate list of skeletonized
// excerpts the agent follows up with file reads.
//
// Whether broad mode engages is decided by the first-pass rerank head score
// (a weak head = no chunk directly answers the query); the surface heuristic
// below only prefetches the decomposition to hide LLM latency, and serves as
// the fallback classifier when the reranker is unavailable.

const (
	decomposeTTL     = 12 * time.Second
	decomposeMaxSubs = 4 // sub-queries beyond this add rerank calls, not recall

	broadMaxHits    = 24
	broadPerFileCap = 2

	manifestBoostScore = 3.0

	skeletonMinLines  = 28 // hits shorter than this render in full
	skeletonHeadLines = 12
	skeletonTermKeeps = 10
)

// queryLooksBroad guesses by surface shape: a query with no identifier-like
// token (camelCase, snake_case, dotted or slashed names) is LIKELY
// exploratory. This is only a prefetch hint and the reranker-down fallback;
// the authoritative broad/focused decision is the first-pass rerank score.
func queryLooksBroad(query string) bool {
	for _, field := range strings.Fields(query) {
		if looksLikeCodeToken(field) {
			return false
		}
	}
	return true
}

func looksLikeCodeToken(token string) bool {
	token = strings.Trim(token, "\"'`“”‘’()[]{}<>,;:!?，。？！、")
	if token == "" {
		return false
	}
	if strings.ContainsAny(token, "_/\\") {
		return true
	}
	if i := strings.IndexByte(token, '.'); i > 0 && i < len(token)-1 {
		if isWordChar(token[i-1]) && isWordChar(token[i+1]) {
			return true
		}
	}
	if strings.Contains(token, "()") {
		return true
	}
	runes := []rune(token)
	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i-1]) {
			return true
		}
	}
	return false
}

func isWordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// structuralIntentTerms mark queries whose answer lives in manifests, runtime
// configs and entry-point wiring rather than in business code — "how do the
// services communicate", "what is the frontend theme". Broad mode applies the
// manifest prior too, but only engages when the first-pass rerank head is weak
// (<0.50); an architecture question where some Controller happens to score
// well would bypass it entirely, so this surface check lets the prior act in
// the first pass regardless. Bilingual because ACE queries arrive in both.
// Bare "配置"/"config" are deliberately absent: specific locator queries
// ("路由配置在哪", "router config") contain them too, and the blanket
// manifest boost buried their actual answers under pom.xml/Application
// noise. Only architecture-level config phrasings fire the first-pass
// prior; genuinely broad queries still get it in the second pass, which
// applies the manifest prior unconditionally.
var structuralIntentTerms = []string{
	// zh
	"架构", "技术栈", "项目配置", "如何配置", "怎么配置", "配置管理", "依赖", "部署", "通信", "微服务", "框架", "中间件",
	"注册中心", "网关", "构建", "打包", "环境变量", "整体风格", "主题", "样式风格",
	// en (substring match on the lowercased query)
	"architecture", "tech stack", "project config", "config management", "configuration structure", "dependenc", "deploy", "communicat",
	"microservice", "framework", "middleware", "registry", "gateway", "infra",
	"build system", "theme", "styling",
}

func queryWantsStructure(query string) bool {
	q := strings.ToLower(query)
	for _, term := range structuralIntentTerms {
		if strings.Contains(q, term) {
			return true
		}
	}
	return false
}

// manifestBoost is the structural prior itself (applied in broad mode, and in
// the first pass when queryWantsStructure fires): questions about stack, style
// or architecture are answered by project manifests, tool configs and global
// stylesheets, yet those files never resemble the query text on any ranking
// path. The file names below are ecosystem conventions, not project-specific
// knowledge.
var manifestNames = map[string]bool{
	"package.json": true, "go.mod": true, "cargo.toml": true, "pyproject.toml": true,
	"requirements.txt": true, "composer.json": true, "pom.xml": true, "build.gradle": true,
	"build.gradle.kts": true, "settings.gradle": true, "gemfile": true, "mix.exs": true, "makefile": true,
	"dockerfile": true, "docker-compose.yml": true, "compose.yaml": true, "compose.yml": true,
	"readme.md": true, "readme": true,
	// Spring runtime configs and boot entry points: architecture questions
	// (service communication, timeouts, registries) are answered by these,
	// not by any code chunk that resembles the query.
	"application.yml": true, "application.yaml": true, "application.properties": true,
	"bootstrap.yml": true, "bootstrap.yaml": true, "bootstrap.properties": true,
}

func manifestBoost(path string) float64 {
	base := strings.ToLower(filepath.Base(path))
	if manifestNames[base] {
		return manifestBoostScore
	}
	// XxxApplication.java carries the framework wiring annotations
	// (@EnableFeignClients, @EnableDiscoveryClient) that state the
	// architecture outright.
	if strings.HasSuffix(base, "application.java") {
		return manifestBoostScore
	}
	if strings.Contains(base, ".config.") || strings.HasPrefix(base, "tsconfig") {
		return manifestBoostScore
	}
	switch ext := filepath.Ext(base); ext {
	case ".css", ".less", ".scss", ".sass", ".styl":
		switch strings.TrimSuffix(base, ext) {
		case "main", "global", "app", "index", "style", "styles", "theme", "variables":
			return manifestBoostScore
		}
	}
	return 0
}

const decomposeSystemPrompt = `You expand one broad codebase-search query into focused sub-queries for a code retrieval engine.

Rules:
- Output 3 to 4 sub-queries, one per line, nothing else: no numbering, no bullets, no explanations.
- Each sub-query targets ONE concrete aspect of the question — configuration/manifest files, entry points, core implementation, data models, UI pages, external integrations — whichever apply.
- Include likely technical keywords, identifiers or well-known file names in English (e.g. "package.json dependencies ui framework", "global stylesheet theme colors", "router controller endpoint definitions"), even when the input query is in another language.`

// startDecompose asks the (already configured) enhancer chat model for
// sub-queries, concurrently with candidate collection and first-pass scoring.
// Any failure yields nil: broad mode still works on its other levers.
func (s *Service) startDecompose(ctx context.Context, query string) <-chan []string {
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
		dctx, cancel := context.WithTimeout(ctx, decomposeTTL)
		defer cancel()
		body := map[string]any{
			"model":      cfg.Model,
			"messages":   []map[string]string{{"role": "system", "content": decomposeSystemPrompt}, {"role": "user", "content": query}},
			"stream":     false,
			"max_tokens": 300,
			// Qwen3-family hybrid models burn the whole budget on <think>
			// and return empty content without this (which silently killed
			// sub-query decomposition); other providers ignore the field.
			"enable_thinking": false,
		}
		if err := postJSON(dctx, url+"/chat/completions", cfg.apiKey(), body, &response); err != nil || len(response.Choices) == 0 {
			ch <- nil
			return
		}
		ch <- parseSubQueries(response.Choices[0].Message.Content, query)
	}()
	return ch
}

func parseSubQueries(content, original string) []string {
	subs := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.Trim(line, " \t-*•`\"'")
		// strip "1." / "2)" style numbering the model may emit anyway
		if len(line) > 2 && line[0] >= '0' && line[0] <= '9' && (line[1] == '.' || line[1] == ')') {
			line = strings.TrimSpace(line[2:])
		}
		if line == "" || len(line) > 200 || line == original || seen[line] {
			continue
		}
		seen[line] = true
		subs = append(subs, line)
		if len(subs) >= decomposeMaxSubs {
			break
		}
	}
	return subs
}

// rankByValues mirrors rankBy for scores held outside the candidate struct,
// so sub-query rankings never clobber the primary query's per-candidate
// scores (which the final hits report).
func rankByValues(candidates []*candidate, values []float64) []int {
	idx := []int{}
	for i := range values {
		if values[i] > 0 {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		a, b := idx[i], idx[j]
		if values[a] == values[b] {
			return candidates[a].path < candidates[b].path
		}
		return values[a] > values[b]
	})
	if len(idx) > rankDepth {
		idx = idx[:rankDepth]
	}
	return idx
}

// fuseSubQueries scores each sub-query on the lexical and structural paths
// (sub-queries are keyword-rich, so the dense path adds little over these)
// and folds the rankings into the shared RRF map. Files matched by several
// sub-queries accumulate reciprocal-rank mass and rise.
func (s *Service) fuseSubQueries(ctx context.Context, candidates []*candidate, subs []string, fused map[int]float64) {
	for _, sub := range subs {
		lex := s.bm25Values(ctx, candidates, codeTokens(sub))
		terms := tokens(sub)
		structural := make([]float64, len(candidates))
		for i, c := range candidates {
			p, sym := structuralValues(c, terms)
			structural[i] = p + sym
		}
		for _, rank := range [][]int{rankByValues(candidates, lex), rankByValues(candidates, structural)} {
			for r, idx := range rank {
				fused[idx] += 1.0 / float64(rrfK+r+1)
			}
		}
	}
}

// skeletonize compresses a long excerpt for the broad-mode candidate list:
// the head of the span plus lines mentioning query terms survive, elided runs
// collapse into a marker citing the real line range so the agent can follow
// up with a targeted file read. Short excerpts pass through untouched.
func skeletonize(path string, startLine int, content string, terms []string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= skeletonMinLines {
		return content
	}
	keep := make([]bool, len(lines))
	for i := 0; i < skeletonHeadLines && i < len(lines); i++ {
		keep[i] = true
	}
	kept := 0
	for i := skeletonHeadLines; i < len(lines) && kept < skeletonTermKeeps; i++ {
		lower := strings.ToLower(lines[i])
		for _, t := range terms {
			if t != "" && strings.Contains(lower, t) {
				keep[i] = true
				kept++
				break
			}
		}
	}
	var b strings.Builder
	i := 0
	for i < len(lines) {
		if keep[i] {
			b.WriteString(lines[i])
			b.WriteByte('\n')
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		if j-i <= 2 {
			for ; i < j; i++ {
				b.WriteString(lines[i])
				b.WriteByte('\n')
			}
			continue
		}
		fmt.Fprintf(&b, "... (%d lines omitted, read %s:%d-%d)\n", j-i, path, startLine+i, startLine+j-1)
		i = j
	}
	return strings.TrimRight(b.String(), "\n")
}
