package domain

import "time"

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	Disabled bool   `json:"disabled"`
	// ACETokenHash is the sha256 of the user's personal ACE/MCP token;
	// authentication only ever compares this hash.
	ACETokenHash string `json:"-"`
	// ACEToken is the plaintext personal token kept so the console can
	// re-display it; the postgres store encrypts it at rest.
	ACEToken     string `json:"-"`
	PasswordHash string `json:"-"`
	// Email is set by self-registration (verified via emailed code); empty for
	// bootstrap and OAuth-created accounts until they bind one.
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	// LinuxDoID is the immutable forum user id from LinuxDo Connect; non-zero
	// marks an OAuth-linked account. Such accounts may have an empty
	// PasswordHash, which makes password login impossible by construction.
	LinuxDoID       int64  `json:"linuxdo_id,omitempty"`
	LinuxDoUsername string `json:"linuxdo_username,omitempty"`
	TrustLevel      int    `json:"trust_level,omitempty"`
	// MaxOutputTokens caps the token budget of a single retrieval response for
	// this user; 0 means the server default applies. User-editable from the
	// account settings page.
	MaxOutputTokens int       `json:"max_output_tokens"`
	CreatedAt       time.Time `json:"created_at"`
	LastActiveAt    time.Time `json:"last_active_at"`
}

type Session struct {
	TokenHash string
	CSRFToken string
	UserID    string
	ExpiresAt time.Time
}

type RepositoryStatus string

const (
	RepositoryHealthy  RepositoryStatus = "healthy"
	RepositoryIndexing RepositoryStatus = "indexing"
	RepositoryLagging  RepositoryStatus = "lagging"
	RepositoryFailed   RepositoryStatus = "failed"
	RepositoryPaused   RepositoryStatus = "paused"
)

type Repository struct {
	ID              string           `json:"id"`
	OwnerID         string           `json:"owner_id"`
	OwnerUsername   string           `json:"owner_username,omitempty"`
	Name            string           `json:"name"`
	RootPath        string           `json:"root_path"`
	Branch          string           `json:"branch"`
	BaseSnapshot    string           `json:"base_snapshot"`
	SnapshotID      string           `json:"snapshot_id"`
	Status          RepositoryStatus `json:"status"`
	FileCount       int              `json:"file_count"`
	ChunkCount      int              `json:"chunk_count"`
	StorageBytes    int64            `json:"storage_bytes"`
	CurrentRevision int64            `json:"current_revision"`
	IndexedRevision int64            `json:"indexed_revision"`
	IndexLagMS      int64            `json:"index_lag_ms"`
	SearchP95MS     float64          `json:"search_p95_ms"`
	EmbeddingModel  string           `json:"embedding_model"`
	LastSyncedAt    time.Time        `json:"last_synced_at"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	Paused          bool             `json:"paused"`
}

type Blob struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Content   string    `json:"content,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Chunk is a structure-aware slice of a blob. Chunks are content-addressed
// through their blob: the same blob always yields the same chunk IDs, so
// embeddings computed for one snapshot remain valid for every other snapshot
// that references the blob.
type Chunk struct {
	ID         string `json:"id"`
	BlobName   string `json:"blob_name"`
	Seq        int    `json:"seq"`
	Symbol     string `json:"symbol,omitempty"`
	SymbolKind string `json:"symbol_kind,omitempty"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Language   string `json:"language,omitempty"`
	// TokenCount is the lexical token count persisted at index time; zero means
	// the chunk predates the inverted index and BM25 falls back to on-the-fly
	// tokenization.
	TokenCount int `json:"token_count,omitempty"`
	// ContentHash fingerprints (path, chunk text) at index time. Embedding
	// vectors are keyed by it, so an edit that leaves a chunk's text in place
	// reuses the existing vector instead of re-embedding: chunk IDs contain
	// the blob name and change wholesale with every file edit, the hash does
	// not. Empty means the chunk predates the hash column and its vectors
	// live under the legacy per-chunk-ID key.
	ContentHash string `json:"content_hash,omitempty"`
	// Summary is a one-sentence LLM description written by the embedding
	// pipeline; it enriches the embedding text and rerank documents so
	// natural-language queries can meet code they do not textually resemble.
	// Empty until the summarizer has run (or when it is not configured).
	Summary string `json:"summary,omitempty"`
}

// ChunkPosting is one inverted-index entry: term frequency of a lexical term
// inside a chunk, written when the chunk is first indexed.
type ChunkPosting struct {
	ChunkID string `json:"chunk_id"`
	Term    string `json:"term"`
	TF      int    `json:"tf"`
}

type ChunkEmbedding struct {
	ChunkID string
	// ContentHash routes the write: non-empty stores the vector under the
	// content-addressed key (shared across identical chunks), empty falls
	// back to the legacy per-chunk-ID row.
	ContentHash string
	ModelID     string
	Dims        int
	Vector      []float32
}

// Snapshot is a logical visibility set: an immutable list of blob names.
// Repository syncs and ACE checkpoints both materialize as snapshots, which
// keeps workspace correctness decoupled from index freshness.
type Snapshot struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id,omitempty"`
	ParentID     string    `json:"parent_id,omitempty"`
	BlobNames    []string  `json:"blob_names,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type RepositoryFile struct {
	RepositoryID string    `json:"repository_id"`
	BlobName     string    `json:"blob_name"`
	Path         string    `json:"path"`
	Content      string    `json:"content,omitempty"`
	ContentHash  string    `json:"content_hash"`
	LineCount    int       `json:"line_count"`
	Language     string    `json:"language"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type IndexJob struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Repository   string    `json:"repository"`
	OwnerID      string    `json:"owner_id"`
	Revision     int64     `json:"revision"`
	Status       JobStatus `json:"status"`
	Priority     string    `json:"priority"`
	Stage        string    `json:"stage"`
	FilesAdded   int       `json:"files_added"`
	FilesUpdated int       `json:"files_updated"`
	FilesDeleted int       `json:"files_deleted"`
	DurationMS   int64     `json:"duration_ms"`
	QueueMS      int64     `json:"queue_ms"`
	ParseMS      int64     `json:"parse_ms"`
	EmbeddingMS  int64     `json:"embedding_ms"`
	WriteMS      int64     `json:"write_ms"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
}

type MetricPoint struct {
	Scope        string    `json:"scope"`
	RepositoryID string    `json:"repository_id,omitempty"`
	Name         string    `json:"name"`
	Value        float64   `json:"value"`
	Timestamp    time.Time `json:"timestamp"`
}

type AuditEvent struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actor_id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Result     string         `json:"result"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Overview aggregates the caller's ACE projects: counts and storage come from
// user_ace_projects snapshots, health from embedding coverage, latency from
// recorded search metrics.
type Overview struct {
	Repositories   int     `json:"repositories"`
	Files          int     `json:"files"`
	Chunks         int     `json:"chunks"`
	EmbeddedChunks int     `json:"embedded_chunks"`
	StorageBytes   int64   `json:"storage_bytes"`
	Healthy        int     `json:"healthy"`
	Lagging        int     `json:"lagging"`
	FailedJobs     int     `json:"failed_jobs"`
	ActiveUsers    int     `json:"active_users"`
	SearchP50MS    float64 `json:"search_p50_ms"`
	SearchP95MS    float64 `json:"search_p95_ms"`
	SearchP99MS    float64 `json:"search_p99_ms"`
	// LatencyPoints are the most recent raw search latency samples (time
	// order) backing the console's latency chart.
	LatencyPoints []MetricPoint `json:"latency_points"`
}

// EvalCase is one entry of the retrieval benchmark: a real question about the
// repository plus the files/keywords a good answer must surface.
type EvalCase struct {
	ID               string    `json:"id"`
	RepositoryID     string    `json:"repository_id"`
	Query            string    `json:"query"`
	ExpectedPaths    []string  `json:"expected_paths"`
	ExpectedKeywords []string  `json:"expected_keywords"`
	CreatedAt        time.Time `json:"created_at"`
}

// ACEUsage is a per-user, per-endpoint aggregate of MCP/ACE calls. Units are
// endpoint-specific: retrieval = estimated context tokens served, upload =
// blobs uploaded, enhance = estimated tokens generated.
type ACEUsage struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	Endpoint string    `json:"endpoint"`
	Calls    int64     `json:"calls"`
	Units    int64     `json:"units"`
	LastDay  time.Time `json:"last_day"`
}

// ACEUsageDay is one day of MCP/ACE traffic for one endpoint; the console
// renders these as usage trend series (per user, or deployment-wide when
// aggregated without a user filter).
type ACEUsageDay struct {
	Day      time.Time `json:"day"`
	Endpoint string    `json:"endpoint"`
	Calls    int64     `json:"calls"`
	Units    int64     `json:"units"`
}

// ACEProject is a pseudo-repository derived from an ACE client checkpoint.
// The ACE protocol carries no project identity (clients send root-relative
// paths only), so the name is inferred from manifest blobs and stats are
// computed from the archived snapshot.
type ACEProject struct {
	Name          string    `json:"name"`
	OwnerID       string    `json:"owner_id,omitempty"`
	OwnerUsername string    `json:"owner_username,omitempty"`
	SnapshotID    string    `json:"snapshot_id"`
	FileCount     int       `json:"file_count"`
	ChunkCount    int       `json:"chunk_count"`
	EmbeddedCount int       `json:"embedded_count"`
	StorageBytes  int64     `json:"storage_bytes"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Announcement is one admin-published notice. The console shows the latest
// announcement to every signed-in user as a dismissable popup; content is
// Markdown, rendered client-side.
type Announcement struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	AuthorID string `json:"author_id"`
	// Author is the publishing admin's username, denormalized at publish time
	// so the record survives account changes.
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// QuotaSettings are the deployment-wide allowances applied to every non-admin
// user. Admin accounts and the shared deployment token are exempt; counts
// reset daily, storage is a standing ceiling.
type QuotaSettings struct {
	StorageLimitMB int `json:"storage_limit_mb"`
	DailyRetrieval int `json:"daily_retrieval"`
	DailyEnhance   int `json:"daily_enhance"`
	DailyUpload    int `json:"daily_upload"`
	RPMRetrieval   int `json:"rpm_retrieval"`
	RPMUpload      int `json:"rpm_upload"`
	RPMEnhance     int `json:"rpm_enhance"`
}

type RetrievalSettings struct {
	EmbeddingProvider   string `json:"embedding_provider"`
	EmbeddingURL        string `json:"embedding_url"`
	EmbeddingModel      string `json:"embedding_model"`
	EmbeddingDimensions int    `json:"embedding_dimensions"`
	// The reranker shares the embedding provider/URL/key; it only carries its
	// own model name and top-K.
	RerankerEnabled bool   `json:"reranker_enabled"`
	RerankerModel   string `json:"reranker_model"`
	RerankerTopK    int    `json:"reranker_top_k"`
	// The enhancer/summary path mirrors the embedding block: its own
	// provider, endpoint and dedicated key(s). Provider is informational for
	// now (every option speaks the OpenAI chat protocol, ollama included).
	EnhancerProvider string `json:"enhancer_provider"`
	EnhancerURL      string `json:"enhancer_url"`
	EnhancerModel    string `json:"enhancer_model"`
	SummaryModel     string `json:"summary_model"`
	// SummaryBudget caps LLM summary calls per embedding round; 0 keeps only
	// the free rule-based file descriptions.
	SummaryBudget int `json:"summary_budget"`
	// Per-provider dedicated keys; either may hold several keys
	// (newline/comma-separated) that are round-robined per request to pool
	// the accounts' separate rate limits. The legacy shared model_api_key
	// (env MODEL_API_KEY / stored setting) remains a code-level fallback but
	// is no longer exposed or editable through the API.
	EmbeddingAPIKey string    `json:"embedding_api_key"`
	EnhancerAPIKey  string    `json:"enhancer_api_key"`
	UpdatedAt       time.Time `json:"updated_at"`
}
