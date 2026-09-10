package store

import (
	"context"
	"errors"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Store interface {
	Close() error
	CreateUser(context.Context, domain.User) error
	UpdateUser(context.Context, domain.User) error
	UserByUsername(context.Context, string) (domain.User, error)
	UserByID(context.Context, string) (domain.User, error)
	// UserByACETokenHash resolves a personal ACE/MCP token (by sha256 hash)
	// to its owner; empty hashes never match.
	UserByACETokenHash(context.Context, string) (domain.User, error)
	// UserByEmail matches the verified registration email; empty email never
	// matches (bootstrap/OAuth accounts may not have one).
	UserByEmail(context.Context, string) (domain.User, error)
	// UserByLinuxDoID resolves a LinuxDo Connect account link; zero never
	// matches.
	UserByLinuxDoID(context.Context, int64) (domain.User, error)
	ListUsers(context.Context) ([]domain.User, error)
	CreateSession(context.Context, domain.Session) error
	SessionByTokenHash(context.Context, string) (domain.Session, error)
	DeleteSession(context.Context, string) error
	DeleteUserSessions(context.Context, string) error
	// DeleteExpiredSessions drops session rows past their expiry; reads
	// already filter them out, this is periodic hygiene only.
	DeleteExpiredSessions(context.Context) error

	CreateRepository(context.Context, domain.Repository) error
	UpdateRepository(context.Context, domain.Repository) error
	RepositoryByID(context.Context, string) (domain.Repository, error)
	ListRepositories(context.Context, string, bool) ([]domain.Repository, error)
	PutBlob(context.Context, domain.Blob) error
	// BlobsByNames fetches a blob set in one round trip; the ACE search path
	// resolves whole workspaces at once, where per-name queries dominate
	// latency. Missing names are silently absent from the result.
	BlobsByNames(context.Context, []string) ([]domain.Blob, error)
	// ExistingBlobNames reports which of the names are already stored. Blob
	// names are content-addressed, so an existing name means the identical
	// content was fully ingested before and the whole encrypt/chunk/tokenize
	// pipeline can be skipped for it — client sync loops re-upload unchanged
	// files constantly.
	ExistingBlobNames(context.Context, []string) (map[string]bool, error)
	ReplaceRepositoryFiles(context.Context, string, []domain.RepositoryFile, []string) error
	RepositoryFiles(context.Context, string) ([]domain.RepositoryFile, error)

	PutChunks(context.Context, string, []domain.Chunk, []domain.ChunkPosting) error
	ChunksByBlobNames(context.Context, []string) ([]domain.Chunk, error)
	// UpdateChunkSummaries persists LLM summaries (chunk ID -> summary) written
	// by the embedding pipeline after the chunks themselves were indexed.
	UpdateChunkSummaries(context.Context, map[string]string) error
	// ChunkPostings returns term -> chunkID -> tf for the given query terms,
	// restricted to the given chunk IDs (the visible candidate set).
	ChunkPostings(context.Context, []string, []string) (map[string]map[string]int, error)
	PutEmbeddings(context.Context, []domain.ChunkEmbedding) error
	// EmbeddingsByChunkIDs returns raw vectors. The scoring hot path is
	// VectorScores (in-database); this remains for existence checks in the
	// embedding pipeline and as the legacy-table fallback while the pgvector
	// migration is still copying old rows.
	EmbeddingsByChunkIDs(context.Context, []string, string) (map[string][]float32, error)
	// VectorScores computes inner-product similarity between the query vector
	// and each candidate chunk's stored vector inside the database (pgvector),
	// so a search ships one query vector in and gets scalars back instead of
	// pulling megabytes of float32 out to score in Go. Chunks without a vector
	// for the model (or with mismatched dims) are simply absent from the map.
	VectorScores(ctx context.Context, chunkIDs []string, modelID string, query []float32) (map[string]float64, error)
	// BlobPaths returns name -> path for the given blob names without loading
	// or decrypting content: the cheap manifest view the large-workspace
	// search prefilter works from.
	BlobPaths(ctx context.Context, names []string) (map[string]string, error)
	// BlobChunkCounts returns blob name -> stored chunk count for the given
	// blobs: the prefilter's cost model. File count alone is a bad budget —
	// a documentation warehouse averages 5-20 chunks per file (large markdown
	// files reach 165), so 800 nominated files can drag 20k+ chunks into the
	// pipeline and reproduce the exhaustive-path latency the prefilter exists
	// to avoid.
	BlobChunkCounts(ctx context.Context, names []string) (map[string]int, error)
	// BlobSymbols returns blob name -> distinct chunk symbols for the given
	// blobs: the prefilter's identifier signal. (The inverted index cannot
	// serve this role: chunk_postings has no term-leading index, so a
	// term-driven query over its tens of millions of rows seq-scans, and
	// probing it per chunk costs ~200k index probes for a 12k-file project —
	// both measured in seconds. Symbols are a narrow indexed read.)
	BlobSymbols(ctx context.Context, names []string) (map[string][]string, error)
	// TopVectorBlobs returns up to k blob names (among names) ranked by their
	// best head chunk's inner-product similarity to the query vector, computed
	// in-database — the prefilter's semantic signal. Only each file's head
	// chunks (seq < 4) participate: this is file selection, not chunk ranking,
	// and a 5x smaller scan is worth the approximation because the surviving
	// subset gets full per-chunk scoring in the pipeline anyway.
	TopVectorBlobs(ctx context.Context, names []string, modelID string, query []float32, k int) ([]string, error)
	// BlobsMissingEmbeddings lists up to limit blob names that still have
	// chunks without a vector for the given model — the restart-recovery
	// backfill's work queue.
	BlobsMissingEmbeddings(context.Context, string, int) ([]string, error)
	CreateSnapshot(context.Context, domain.Snapshot) error
	SnapshotByID(context.Context, string) (domain.Snapshot, error)
	LatestSnapshot(context.Context) (domain.Snapshot, error)
	// SetUserCheckpoint records the checkpoint a user's ACE client last
	// resolved; UserCheckpoint reads it back. Content-addressed checkpoints
	// are shared between users, so ownership lives in this pointer, not on
	// the snapshot itself.
	SetUserCheckpoint(context.Context, string, string) error
	UserCheckpoint(context.Context, string) (string, error)
	// UserOwnsSnapshot reports whether the snapshot is referenced by one of
	// the user's archived projects or their latest checkpoint pointer — the
	// retrieval-side ownership check for client-claimed checkpoint IDs.
	UserOwnsSnapshot(context.Context, string, string) (bool, error)
	// SaveACEProject archives (user, project name) -> snapshot so
	// the console can list every project an ACE client has indexed;
	// ListACEProjects returns them newest-first with stats computed against
	// the given embedding model ID. ACEProjectRefs is the lightweight
	// name -> snapshot map used by archival dedupe, and DeleteACEProject
	// removes a row superseded by a rename/merge.
	SaveACEProject(context.Context, string, string, string) error
	// EnsureACEProject inserts a placeholder project row (empty snapshot) so
	// the console shows the project while its first upload is still running;
	// it never touches an existing row. Returns whether a row was created.
	EnsureACEProject(context.Context, string, string) (bool, error)
	ACEProjectRefs(context.Context, string) (map[string]string, error)
	DeleteACEProject(context.Context, string, string) error
	// PurgeACEProject physically deletes a user's archived project: the
	// pointer row, its ACE index-activity jobs, every checkpoint snapshot of
	// the same workspace (blob overlap >= 0.5 with the project's snapshot,
	// mirroring archival dedupe) that no surviving project row or other
	// user's checkpoint still references, and any blobs left unreferenced
	// afterwards (chunks/postings/embeddings go with them). Repository sync
	// snapshots (repository_id set) are never touched. Returns ErrNotFound
	// when the project row does not exist.
	PurgeACEProject(context.Context, string, string) error
	ListACEProjects(context.Context, string, string) ([]domain.ACEProject, error)
	// ListAllACEProjects is the admin view: every user's archived projects,
	// owner attribution included.
	ListAllACEProjects(context.Context, string) ([]domain.ACEProject, error)

	CreateEvalCase(context.Context, domain.EvalCase) error
	ListEvalCases(context.Context, string) ([]domain.EvalCase, error)
	DeleteEvalCase(context.Context, string) error
	EvalCaseByID(context.Context, string) (domain.EvalCase, error)

	CreateJob(context.Context, domain.IndexJob) error
	UpdateJob(context.Context, domain.IndexJob) error
	JobByID(context.Context, string) (domain.IndexJob, error)
	ListJobs(context.Context, string, bool, int) ([]domain.IndexJob, error)
	RecordMetric(context.Context, domain.MetricPoint) error
	Metrics(context.Context, string, string, time.Time, int) ([]domain.MetricPoint, error)
	// RecordACEUsage increments today's (user, endpoint) aggregate by one call
	// and the given units; empty user IDs bucket shared-token traffic.
	RecordACEUsage(context.Context, string, string, int64) error
	// ListACEUsage aggregates per (user, endpoint) over the trailing N days.
	ListACEUsage(context.Context, int) ([]domain.ACEUsage, error)
	// ACEUsageDaily returns the per-day, per-endpoint series over the trailing
	// N days; an empty userID aggregates across all users (admin trend view).
	ACEUsageDaily(context.Context, string, int) ([]domain.ACEUsageDay, error)
	// Quota counters are kept separate from ace_usage on purpose: an admin
	// reset clears today's allowance without erasing usage statistics.
	// IncQuotaUsage adds one to today's (user, kind) counter; kinds are
	// retrieval/enhance/upload.
	IncQuotaUsage(context.Context, string, string) error
	// AddQuotaUsage adds n to today's (user, kind) counter — used by the
	// upload_bytes volume counter, the persistent backstop of the storage
	// ceiling (pre-archival uploads are invisible to snapshot aggregates).
	AddQuotaUsage(context.Context, string, string, int64) error
	// ClearQuotaUsageKind zeroes one of today's counters (project deletion
	// clears upload_bytes so freed space can be re-uploaded the same day).
	ClearQuotaUsageKind(context.Context, string, string) error
	// QuotaUsageToday returns kind -> used for the user's current day.
	QuotaUsageToday(context.Context, string) (map[string]int64, error)
	// QuotaUsageTodayAll returns userID -> kind -> used for the current day
	// (the admin user list view).
	QuotaUsageTodayAll(context.Context) (map[string]map[string]int64, error)
	// ResetQuotaUsage zeroes the user's counters for the current day.
	ResetQuotaUsage(context.Context, string) error
	CreateAudit(context.Context, domain.AuditEvent) error
	ListAudit(context.Context, int) ([]domain.AuditEvent, error)
	// Announcements are admin-published console notices, newest first.
	// LatestAnnouncement returns ErrNotFound when none exist. A dismissal
	// records the announcement a user closed, so the login popup stays away
	// until a newer announcement is published; DismissedAnnouncement returns
	// an empty ID (no error) when the user never dismissed one.
	CreateAnnouncement(context.Context, domain.Announcement) error
	ListAnnouncements(context.Context, int) ([]domain.Announcement, error)
	LatestAnnouncement(context.Context) (domain.Announcement, error)
	DeleteAnnouncement(context.Context, string) error
	DismissAnnouncement(context.Context, string, string) error
	DismissedAnnouncement(context.Context, string) (string, error)
	GetSettings(context.Context) (map[string]string, error)
	SetSettings(context.Context, map[string]string) error
}
