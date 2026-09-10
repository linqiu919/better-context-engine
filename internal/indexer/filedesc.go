package indexer

import (
	"path/filepath"
	"strings"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Rule-based file descriptions: the zero-cost tier of summary enrichment.
// LLM summaries exist to bridge "overall frontend style" to a stylesheet, but
// for enumerable ecosystem file types that bridge sentence is fully
// determined by the file name — no model needed. Descriptions land in
// chunks.summary through the same pipeline as LLM summaries (embedDocText
// header + rerank document), so they work identically at query time and keep
// working when summary_model is "none" or the budget is 0.

// fileDescByName maps well-known base names (lowercased) to their bridge
// sentence. Kept in English to match the embedding/rerank document language.
var fileDescByName = map[string]string{
	"package.json":       "npm package manifest: project metadata, dependencies and scripts",
	"package-lock.json":  "npm lockfile: resolved dependency versions",
	"go.mod":             "Go module definition: module path and dependency requirements",
	"go.sum":             "Go module checksums for dependency verification",
	"cargo.toml":         "Rust crate manifest: package metadata and dependencies",
	"pyproject.toml":     "Python project manifest: build system, dependencies and tool configuration",
	"requirements.txt":   "Python dependency list",
	"composer.json":      "PHP Composer manifest: package metadata and dependencies",
	"pom.xml":            "Maven project manifest: modules, dependencies and build plugins",
	"build.gradle":       "Gradle build script: dependencies and build configuration",
	"build.gradle.kts":   "Gradle Kotlin build script: dependencies and build configuration",
	"settings.gradle":    "Gradle settings: project modules",
	"gemfile":            "Ruby gem dependency manifest",
	"mix.exs":            "Elixir Mix project manifest",
	"makefile":           "Make build and task automation targets",
	"justfile":           "Just task runner recipes",
	"dockerfile":         "container image build instructions",
	"docker-compose.yml": "multi-container orchestration configuration",
	"compose.yaml":       "multi-container orchestration configuration",
	"compose.yml":        "multi-container orchestration configuration",
	".env.example":       "environment variable template listing required configuration",
	"nginx.conf":         "Nginx web server configuration",
	"caddyfile":          "Caddy web server configuration",
	"application.yml":    "application runtime configuration (profiles, service wiring, timeouts)",
	"application.yaml":   "application runtime configuration (profiles, service wiring, timeouts)",
	"application.properties": "application runtime configuration (profiles, service wiring, timeouts)",
	"bootstrap.yml":        "application bootstrap configuration (config server, registry)",
	"bootstrap.yaml":       "application bootstrap configuration (config server, registry)",
	"bootstrap.properties": "application bootstrap configuration (config server, registry)",
}

// fileDescByConfigStem recognizes "<tool>.config.<ext>" build/tool configs.
var fileDescByConfigStem = map[string]string{
	"vite":       "Vite build and dev server configuration",
	"webpack":    "Webpack bundler configuration",
	"rollup":     "Rollup bundler configuration",
	"babel":      "Babel transpiler configuration",
	"jest":       "Jest test runner configuration",
	"vitest":     "Vitest test runner configuration",
	"tailwind":   "Tailwind CSS design token and theme configuration",
	"postcss":    "PostCSS plugin configuration",
	"eslint":     "ESLint lint rule configuration",
	"prettier":   "Prettier code formatting configuration",
	"next":       "Next.js framework configuration",
	"nuxt":       "Nuxt framework configuration",
	"svelte":     "Svelte framework configuration",
	"playwright": "Playwright end-to-end test configuration",
}

// fileDescription returns the deterministic description for a file, or ""
// when no rule recognizes it (the adaptive LLM tier may then pick it up).
// content is only consulted for README-style files, whose first paragraph is
// an author-written summary already.
func fileDescription(path, content string) string {
	logical := logicalPath(path)
	base := strings.ToLower(filepath.Base(logical))

	if desc, ok := fileDescByName[base]; ok {
		return desc
	}
	if strings.HasPrefix(base, "readme") {
		if p := firstParagraph(content); p != "" {
			return p
		}
		return "project README: overview and usage documentation"
	}
	if strings.HasPrefix(base, "changelog") {
		return "project changelog: release history"
	}
	if strings.HasPrefix(base, "tsconfig") {
		return "TypeScript compiler configuration"
	}
	if strings.Contains(logical, ".github/workflows/") {
		return "CI workflow definition (GitHub Actions)"
	}
	if base == ".gitlab-ci.yml" {
		return "CI pipeline definition (GitLab CI)"
	}
	if i := strings.Index(base, ".config."); i > 0 {
		if desc, ok := fileDescByConfigStem[base[:i]]; ok {
			return desc
		}
		return "build or tool configuration file"
	}
	ext := filepath.Ext(base)
	switch ext {
	case ".css", ".less", ".scss", ".sass", ".styl":
		switch strings.TrimSuffix(base, ext) {
		case "main", "global", "app", "index", "style", "styles", "theme", "variables":
			return "global stylesheet: layout, theme colors and typography rules"
		}
	case ".sql":
		if strings.Contains(base, "schema") || strings.Contains(base, "migration") || strings.Contains(base, "init") {
			return "database schema or migration script"
		}
	}
	// Entry points answer "where does the app start" queries; main.* is an
	// ecosystem-wide convention, unlike index.* which is ubiquitous in JS.
	switch base {
	case "main.go", "main.py", "main.rs", "main.ts", "main.tsx", "main.js", "main.jsx", "manage.py", "app.py":
		return "application entry point: startup and wiring"
	}
	return ""
}

// firstParagraph extracts the first prose paragraph of a markdown document:
// headings, badges, images and HTML lines are skipped, consecutive text lines
// are joined, and the result is capped at summaryMaxChars.
func firstParagraph(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") ||
			strings.HasPrefix(line, "![") || strings.HasPrefix(line, "[!") ||
			strings.HasPrefix(line, "---") || strings.HasPrefix(line, "```") ||
			strings.HasPrefix(line, ">") {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, line)
	}
	p := strings.Join(lines, " ")
	if len(p) > summaryMaxChars {
		p = p[:summaryMaxChars]
	}
	return strings.TrimSpace(p)
}

// ruleSummaries computes deterministic descriptions (chunk ID -> text) for
// the chunks that have no summary yet. Every chunk of a described file gets
// the file's description: config/style files chunk into blocks and each block
// benefits from the bridge sentence in its embedding text.
func ruleSummaries(pending []domain.Chunk, blobPath, blobContent map[string]string) map[string]string {
	out := map[string]string{}
	descCache := map[string]string{}
	for _, c := range pending {
		if c.Summary != "" {
			continue
		}
		desc, ok := descCache[c.BlobName]
		if !ok {
			desc = fileDescription(blobPath[c.BlobName], blobContent[c.BlobName])
			descCache[c.BlobName] = desc
		}
		if desc != "" {
			out[c.ID] = desc
		}
	}
	return out
}
