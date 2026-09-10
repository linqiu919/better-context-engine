# Better Context Engine

English | [简体中文](README.zh-CN.md)

An ACE-compatible code context retrieval engine written in Go. It indexes codebases in real time and serves low-latency, high-relevance context to AI coding assistants. The only dependencies are PostgreSQL and one model endpoint — no vector database, no message queue.

## Features

**Retrieval**

- Three-way recall fused with RRF: BM25 lexical search (persistent inverted index with camelCase/snake_case splitting) + path/symbol structural scoring + semantic vectors
- Cross-encoder reranking + context curation (per-file caps, adjacent-fragment merging, small-fragment expansion)
- Binary-quantized in-memory ANN for large candidate sets; each model path degrades independently with cooldowns, so lexical/structural retrieval always stays available

**Indexing**

- tree-sitter AST structural chunking (8 language grammars, method-level chunks; regex and sliding-window fallbacks)
- Content-addressed blobs (`sha256(path+content)`) + immutable snapshots + deterministic checkpoint IDs, deduplicated across users
- Vectors stored per model ID; switching models automatically backfills missing embeddings

**Integration & security**

- ACE endpoints: `/batch-upload`, `/agents/codebase-retrieval` (returns `410` for unknown checkpoints), `/prompt-enhancer` — compatible with ace-tool-rs-style clients
- Global token + personal tokens (stored as SHA-256), per-user daily usage accounting
- Argon2id passwords, CSRF protection, RBAC, audit log; source blobs encrypted at rest with AES-256-GCM

**Console**

- Workspace: overview / my projects / retrieval debugger (fragment attribution breakdown, eval cases) / account settings
- Admin: user management (incl. MCP usage) / all projects / system settings (models & registration) / announcements / audit log
- English & Chinese UI, light & dark themes; frontend embedded via `go:embed` for single-binary deployment

## Default models

| Path | Default model | Notes |
| --- | --- | --- |
| Embedding | `Qwen/Qwen3-Embedding-8B` | 4096 dimensions |
| Reranker | `Qwen/Qwen3-Reranker-8B` | Jina/Cohere-compatible `/rerank` |
| Prompt enhancer | `Qwen/Qwen3.5-9B` | OpenAI chat protocol |

The default endpoint is SiliconFlow (`api.siliconflow.cn/v1`); a single `MODEL_API_KEY` covers all three paths. Everything can be switched to local services (ollama/vLLM) via environment variables or the admin console. Without any model configured, retrieval runs on the lexical/structural paths only.

## Quick start

### Docker Compose (recommended)

```bash
cp .env.example .env    # replace the secrets and API keys
docker compose up -d
```

Open [http://localhost:18181](http://localhost:18181) and sign in as `admin` (password is `BOOTSTRAP_ADMIN_PASSWORD` from your `.env`).

For a local ollama alongside: `docker compose --profile models up -d`.

### Local development

```bash
npm install --prefix ui
npm run build --prefix ui

BOOTSTRAP_ADMIN_PASSWORD=development-admin-password \
DEMO_DATA=true \
go run ./cmd/server
```

Open [http://localhost:18181](http://localhost:18181) and sign in as `admin`. Without `BCE_DATABASE_URL` the server uses an in-memory store.

## Connecting a client

Configure `ace-tool-rs` / `bce-tool` in your MCP client (Cursor, Claude Code, ...) with the base URL pointing at your BCE deployment and the token set to `ACE_TOKEN`. The first retrieval uploads and indexes the project automatically — no manual import needed.

Both the global token and personal tokens (`bce_...`, generated on the account settings page) work; personal tokens attribute usage and enhancement context to the calling user.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `BCE_ADDR` | `:18181` | HTTP listen address |
| `BCE_DATABASE_URL` | empty | PostgreSQL connection URL; empty = in-memory store |
| `ACE_TOKEN` | `development-token` | Global bearer token for the ACE endpoints |
| `BCE_ENCRYPTION_KEY` | empty | AES-256-GCM key for source encryption; required with PostgreSQL |
| `BOOTSTRAP_ADMIN_USERNAME` | `admin` | First administrator username |
| `BOOTSTRAP_ADMIN_PASSWORD` | generated | First administrator password; if omitted, a generated value is printed to the log |
| `COOKIE_SECURE` | `false` | Set to `true` when deployed behind HTTPS |
| `SESSION_TTL` | `24h` | Local session lifetime |
| `DEMO_DATA` | `true` | Seed a demo repository and developer account |
| `MODEL_API_KEY` | empty | Bearer key shared by all three model paths; local servers ignore it |
| `EMBEDDING_PROVIDER` | `openai-compatible` | `ollama` / `openai-compatible` |
| `EMBEDDING_URL` | `https://api.siliconflow.cn/v1` | Embedding service endpoint |
| `EMBEDDING_MODEL` | `Qwen/Qwen3-Embedding-8B` | Code/query embedding model |
| `EMBEDDING_DIMENSIONS` | `4096` | Vector dimensions |
| `RERANKER_MODEL` | `Qwen/Qwen3-Reranker-8B` | Cross-encoder rerank model (reuses the embedding provider's endpoint and key) |
| `RERANKER_TOP_K` | `24` | Number of fused candidates sent to the reranker |
| `ENHANCER_URL` | `https://api.siliconflow.cn/v1` | Prompt-enhancer LLM endpoint |
| `ENHANCER_MODEL` | `Qwen/Qwen3.5-9B` | Prompt-enhancer model; empty makes `/prompt-enhancer` return 501 |

## Development

```bash
go test ./...
npm run build --prefix ui
```

The production container builds the frontend first and embeds it into the server; it also ships `bce-agent`. `bce-server` is compiled with CGO because of the tree-sitter AST chunker.
