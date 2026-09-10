# Better Context Engine

[English](README.md) | 简体中文

ACE 兼容的代码上下文检索引擎（Go）。实时索引代码库，为 AI 编程助手提供低延迟、高相关的上下文。只依赖 PostgreSQL 与一个模型端点，无需向量数据库和消息队列。

## 特性

**检索**

- 三路召回：BM25 词法（持久化倒排索引，camelCase/snake_case 拆分）+ 路径/符号结构打分 + 语义向量，RRF 融合
- 交叉编码器重排 + 上下文精选（每文件上限、相邻片段合并、小片段扩展）
- 大候选集走二值量化内存 ANN；模型故障各自独立冷却降级，词法/结构两路始终可用

**索引**

- tree-sitter AST 结构分块（8 种语言 grammar，方法级切块；正则、滑窗两级兜底）
- 内容寻址 blob（`sha256(path+content)`）+ 不可变快照 + 确定性 checkpoint ID，跨用户去重
- 向量按模型 ID 隔离存储，切换模型后缺失向量自动补嵌

**接入与安全**

- ACE 端点：`/batch-upload`、`/agents/codebase-retrieval`（未知 checkpoint 返回 `410`）、`/prompt-enhancer`，兼容 ace-tool-rs 类客户端
- 全局令牌 + 个人令牌（SHA-256 存储），按用户统计每日用量
- Argon2id 密码、CSRF 防护、RBAC、审计日志；源码 blob AES-256-GCM 加密落库

**控制台**

- 工作区：概览 / 我的项目 / 检索调试（片段归因拆解、评测用例）/ 账户设置
- 管理后台：用户管理（含 MCP 用量）/ 全部项目 / 系统设置（模型与注册开关）/ 公告管理 / 审计日志
- 中英双语、明暗主题；前端经 `go:embed` 嵌入，单二进制部署

## 默认模型

| 通路 | 默认模型 | 说明 |
| --- | --- | --- |
| Embedding | `Qwen/Qwen3-Embedding-8B` | 4096 维 |
| 重排 | `Qwen/Qwen3-Reranker-8B` | Jina/Cohere 兼容 `/rerank` |
| 提示增强 | `Qwen/Qwen3.5-9B` | OpenAI chat 协议 |

默认端点为硅基流动（`api.siliconflow.cn/v1`），一个 `MODEL_API_KEY` 覆盖三条通路；均可经环境变量或管理员控制台改为本地服务（ollama/vLLM）。不配模型时检索仅走词法/结构两路。

## 快速开始

### Docker Compose（推荐）

```bash
cp .env.example .env    # 替换其中的密钥与 API Key
docker compose up -d
```

打开 [http://localhost:18181](http://localhost:18181)，用 `admin` 登录（密码为 `.env` 中的 `BOOTSTRAP_ADMIN_PASSWORD`）。

需要本地 ollama 时：`docker compose --profile models up -d`。

### 本地开发

```bash
npm install --prefix ui
npm run build --prefix ui

BOOTSTRAP_ADMIN_PASSWORD=development-admin-password \
DEMO_DATA=true \
go run ./cmd/server
```

打开 [http://localhost:18181](http://localhost:18181)，用 `admin` 登录。未设置 `BCE_DATABASE_URL` 时使用内存存储。

## 接入方式

在 MCP 客户端（Cursor、Claude Code 等）中配置 `ace-tool-rs` / `bce-tool`，将服务地址指向 BCE 部署地址、令牌填 `ACE_TOKEN` 即可。首次检索会自动上传并索引项目，无需手动导入。

令牌既可用全局令牌，也可用账户设置页生成的个人令牌（`bce_...`）——个人令牌会把用量与增强上下文归属到调用用户。

## 配置项

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BCE_ADDR` | `:18181` | HTTP 监听地址 |
| `BCE_DATABASE_URL` | 空 | PostgreSQL 连接 URL；留空使用内存存储 |
| `ACE_TOKEN` | `development-token` | ACE 端点的全局 Bearer Token |
| `BCE_ENCRYPTION_KEY` | 空 | AES-256-GCM 源码加密密钥；使用 PostgreSQL 时必填 |
| `BOOTSTRAP_ADMIN_USERNAME` | `admin` | 首个管理员用户名 |
| `BOOTSTRAP_ADMIN_PASSWORD` | 自动生成 | 首个管理员密码；省略时生成值打印到日志 |
| `COOKIE_SECURE` | `false` | 部署在 HTTPS 之后时设为 `true` |
| `SESSION_TTL` | `24h` | 本地会话有效期 |
| `DEMO_DATA` | `true` | 播种演示仓库与开发者账户 |
| `MODEL_API_KEY` | 空 | 三条模型通路共享的 Bearer Key；本地服务忽略 |
| `EMBEDDING_PROVIDER` | `openai-compatible` | 可选 `ollama` / `openai-compatible` |
| `EMBEDDING_URL` | `https://api.siliconflow.cn/v1` | Embedding 服务端点 |
| `EMBEDDING_MODEL` | `Qwen/Qwen3-Embedding-8B` | 代码/查询嵌入模型 |
| `EMBEDDING_DIMENSIONS` | `4096` | 向量维度 |
| `RERANKER_MODEL` | `Qwen/Qwen3-Reranker-8B` | 交叉编码器重排模型（复用嵌入提供商的端点与 Key） |
| `RERANKER_TOP_K` | `24` | 送入重排的融合候选数量 |
| `ENHANCER_URL` | `https://api.siliconflow.cn/v1` | 提示增强 LLM 端点 |
| `ENHANCER_MODEL` | `Qwen/Qwen3.5-9B` | 提示增强模型；留空则 `/prompt-enhancer` 返回 501 |

## 开发

```bash
go test ./...
npm run build --prefix ui
```

生产容器先构建前端再嵌入服务端，同时包含 `bce-agent`；`bce-server` 因 tree-sitter AST 分块器以 CGO 编译。
