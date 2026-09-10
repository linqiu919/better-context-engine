package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/events"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/store"
)

type FileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type SyncRequest struct {
	Branch       string      `json:"branch"`
	BaseSnapshot string      `json:"base_snapshot"`
	Files        []FileInput `json:"files"`
	DeletedPaths []string    `json:"deleted_paths"`
	Priority     string      `json:"priority"`
}
type SearchHit struct {
	Path          string  `json:"path"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Symbol        string  `json:"symbol,omitempty"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	LexicalScore  float64 `json:"lexical_score"`
	PathScore     float64 `json:"path_score"`
	SymbolScore   float64 `json:"symbol_score"`
	SemanticScore float64 `json:"semantic_score"`
	RerankScore   float64 `json:"rerank_score,omitempty"`
}
type SearchResponse struct {
	FormattedRetrieval string          `json:"formatted_retrieval"`
	Hits               []SearchHit     `json:"hits,omitempty"`
	RelatedSymbols     []RelatedSymbol `json:"related_symbols,omitempty"`
	Confidence         string          `json:"confidence,omitempty"`
	DurationMS         float64         `json:"duration_ms"`
	// InlineEmbedMS is the slice of DurationMS spent embedding candidates that
	// had no stored vector yet (first search after an upload or a model
	// switch). It is one-off indexing debt, not retrieval speed: the overview
	// latency metric subtracts it so cold first queries don't pollute the
	// P95/P99 a steady-state search is judged by. DurationMS stays the honest
	// end-to-end figure the client experienced.
	InlineEmbedMS  float64 `json:"inline_embed_ms,omitempty"`
	CandidateCount int     `json:"candidate_count"`
	TokenEstimate  int     `json:"token_estimate"`
	Degraded       bool    `json:"degraded"`
	// MissingBlobs counts checkpoint blob names with no stored content —
	// ghosts left by uploads the quota rejected while the client cached its
	// manifest as sent. Their files are invisible to retrieval.
	MissingBlobs int `json:"missing_blobs,omitempty"`
}

// ModelDefaults carries environment-level model configuration; admin settings
// stored in the database override these at call time.
type ModelDefaults struct {
	EmbeddingProvider   string
	EmbeddingURL        string
	EmbeddingModel      string
	EmbeddingDimensions int
	// The reranker rides on the embedding provider/URL/key; it only owns its
	// model name and top-K.
	RerankerModel string
	RerankerTopK  int
	EnhancerURL   string
	EnhancerModel string
	SummaryModel  string
	// ModelAPIKey is shared by all three model paths (embedding / reranker /
	// enhancer); local servers such as ollama simply ignore the Bearer header.
	ModelAPIKey string
}

type Service struct {
	store    store.Store
	events   *events.Broker
	defaults ModelDefaults

	embedMu         sync.Mutex
	embedding       map[string]bool
	embedDownUntil  time.Time
	rerankDownUntil time.Time

	tfMu    sync.Mutex
	tfCache map[string]*chunkStats
}

func New(st store.Store, broker *events.Broker, defaults ModelDefaults) *Service {
	return &Service{store: st, events: broker, defaults: defaults, embedding: map[string]bool{}, tfCache: map[string]*chunkStats{}}
}

func BlobName(path, content string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// UploadBlobs stores content-addressed blobs together with their chunks and
// inverted-index postings. Blob names are sha256(path+content), so re-uploads
// of identical content are no-ops and chunk IDs (blobName:seq) stay stable.
// toolDirSkip lists editor/AI-assistant private directories whose contents
// (settings.local.json, caches, rule files) are never useful retrieval
// context yet score deceptively well on config-flavored queries. Their blobs
// still upload (checkpoint accounting needs every name) but are neither
// chunked nor searchable.
var toolDirSkip = map[string]bool{
	".claude": true, ".cursor": true, ".windsurf": true, ".trae": true, ".roo": true,
	".idea": true, ".vscode": true, ".vs": true, ".fleet": true, ".zed": true,
	".git": true, ".svn": true, ".hg": true,
}

func toolPathExcluded(path string) bool {
	for _, seg := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if toolDirSkip[seg] {
			return true
		}
	}
	return false
}

// ingestSem caps how many blobs are being chunk-indexed at once, machine-wide.
// Ingestion (AST chunking, posting tokenization, encryption) is pure CPU: on a
// small host a burst of concurrent batch-upload requests otherwise pins every
// core for tens of seconds, dragging concurrent retrievals from ~2s to >10s.
// Capacity NumCPU-1 (floor 1) always leaves one core serving searches; the
// slot is acquired per blob, so a small upload interleaves with — rather than
// queues behind — a 30s first-index batch of a big project.
var ingestSem = make(chan struct{}, max(1, runtime.NumCPU()-1))

func (s *Service) UploadBlobs(ctx context.Context, inputs []FileInput) ([]string, error) {
	names := make([]string, len(inputs))
	for i, input := range inputs {
		names[i] = BlobName(input.Path, input.Content)
	}
	// Content-addressed dedup up front: an existing name means the identical
	// content already went through encrypt+chunk+tokenize, so the whole
	// pipeline is skipped for it. Client sync loops re-upload unchanged files
	// constantly; without this every re-upload burned the full ingest CPU and
	// then threw the result away at PutChunks' exists-check. A lookup failure
	// degrades to ingesting everything (the old behavior).
	existing, err := s.store.ExistingBlobNames(ctx, names)
	if err != nil {
		existing = map[string]bool{}
	}
	now := time.Now()
	for i, input := range inputs {
		if existing[names[i]] {
			continue
		}
		select {
		case ingestSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		err := func() error {
			defer func() { <-ingestSem }()
			name := names[i]
			if err := s.store.PutBlob(ctx, domain.Blob{Name: name, Path: input.Path, Content: input.Content, CreatedAt: now}); err != nil {
				return err
			}
			if !toolPathExcluded(input.Path) {
				chunks, postings := indexPostings(input.Path, input.Content, ChunkBlob(name, input.Path, input.Content))
				if err := s.store.PutChunks(ctx, name, chunks, postings); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	return names, nil
}

// SyncRepository ingests a file delta into the repository: blobs + chunks +
// postings are written, the file manifest is replaced, and a new immutable
// snapshot becomes the repository's visibility set. The call returns as soon
// as the lexical index is committed; vectors land in a background stage so
// sync latency stays low (searches run degraded until then).
func (s *Service) SyncRepository(ctx context.Context, repo domain.Repository, req SyncRequest) (domain.IndexJob, error) {
	start := time.Now()
	repo.CurrentRevision++
	repo.Status = domain.RepositoryIndexing
	repo.UpdatedAt = start
	if req.Branch != "" {
		repo.Branch = req.Branch
	}
	if req.BaseSnapshot != "" {
		repo.BaseSnapshot = req.BaseSnapshot
	}
	if err := s.store.UpdateRepository(ctx, repo); err != nil {
		return domain.IndexJob{}, err
	}
	job := domain.IndexJob{ID: identity.NewID("job"), RepositoryID: repo.ID, Repository: repo.Name, OwnerID: repo.OwnerID, Revision: repo.CurrentRevision, Status: domain.JobRunning, Priority: req.Priority, Stage: "parse", CreatedAt: start, StartedAt: start, FilesAdded: len(req.Files), FilesDeleted: len(req.DeletedPaths)}
	if job.Priority == "" {
		job.Priority = "realtime"
	}
	if err := s.store.CreateJob(ctx, job); err != nil {
		return domain.IndexJob{}, err
	}
	s.events.Publish("index.progress", job)
	parseStart := time.Now()
	names, err := s.UploadBlobs(ctx, req.Files)
	job.ParseMS = time.Since(parseStart).Milliseconds()
	if err != nil {
		return s.failJob(ctx, repo, job, err)
	}
	files := make([]domain.RepositoryFile, 0, len(req.Files))
	var bytes int64
	for i, input := range req.Files {
		bytes += int64(len(input.Content))
		files = append(files, domain.RepositoryFile{RepositoryID: repo.ID, BlobName: names[i], Path: input.Path, Content: input.Content, ContentHash: names[i], LineCount: lineCount(input.Content), Language: language(input.Path), UpdatedAt: time.Now()})
	}
	writeStart := time.Now()
	if err := s.store.ReplaceRepositoryFiles(ctx, repo.ID, files, req.DeletedPaths); err != nil {
		return s.failJob(ctx, repo, job, err)
	}
	job.WriteMS = time.Since(writeStart).Milliseconds()
	allFiles, err := s.store.RepositoryFiles(ctx, repo.ID)
	if err != nil {
		return s.failJob(ctx, repo, job, err)
	}
	blobNames := make([]string, 0, len(allFiles))
	for _, f := range allFiles {
		blobNames = append(blobNames, f.BlobName)
	}
	snapshot := domain.Snapshot{ID: identity.NewID("snap"), RepositoryID: repo.ID, ParentID: repo.SnapshotID, BlobNames: blobNames, CreatedAt: time.Now()}
	if err := s.store.CreateSnapshot(ctx, snapshot); err != nil {
		return s.failJob(ctx, repo, job, err)
	}
	repo.SnapshotID = snapshot.ID
	repo.FileCount = len(allFiles)
	chunks, _ := s.store.ChunksByBlobNames(ctx, blobNames)
	repo.ChunkCount = len(chunks)
	repo.StorageBytes = 0
	for _, f := range allFiles {
		repo.StorageBytes += int64(len(f.Content))
	}
	if repo.StorageBytes == 0 {
		repo.StorageBytes = bytes
	}
	repo.IndexedRevision = repo.CurrentRevision
	repo.IndexLagMS = 0
	repo.Status = domain.RepositoryHealthy
	repo.LastSyncedAt = time.Now()
	repo.UpdatedAt = repo.LastSyncedAt
	if err := s.store.UpdateRepository(ctx, repo); err != nil {
		return s.failJob(ctx, repo, job, err)
	}
	s.events.Publish("repository.updated", repo)
	// The lexical index is committed at this point and search already works.
	// Vectors are filled in by a background stage so sync latency stays low;
	// a search that arrives first embeds its own top candidates inline
	// (embedCandidatesNow), so results are not degraded while this catches up.
	cfg := s.embeddingConfig(ctx)
	if cfg.enabled() && len(names) > 0 {
		job.Stage = "embedding"
		_ = s.store.UpdateJob(ctx, job)
		s.events.Publish("index.progress", job)
		go s.finishEmbedding(repo.ID, job, names, cfg, start)
		return job, nil
	}
	s.completeJob(ctx, repo.ID, job, start, nil)
	return job, nil
}

func (s *Service) completeJob(ctx context.Context, repoID string, job domain.IndexJob, start time.Time, embedErr error) {
	job.Status = domain.JobCompleted
	job.Stage = "commit"
	if embedErr != nil {
		job.Error = "embedding degraded: " + embedErr.Error()
	}
	job.DurationMS = time.Since(start).Milliseconds()
	job.CompletedAt = time.Now()
	_ = s.store.UpdateJob(ctx, job)
	_ = s.store.RecordMetric(ctx, domain.MetricPoint{Scope: "repository", RepositoryID: repoID, Name: "index_update_ms", Value: float64(job.DurationMS), Timestamp: time.Now()})
	s.events.Publish("index.completed", job)
}

func (s *Service) finishEmbedding(repoID string, job domain.IndexJob, blobNames []string, cfg EmbeddingConfig, start time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	embedStart := time.Now()
	_, err := s.embedMissing(ctx, cfg, blobNames)
	job.EmbeddingMS = time.Since(embedStart).Milliseconds()
	s.completeJob(ctx, repoID, job, start, err)
	if repo, rerr := s.store.RepositoryByID(ctx, repoID); rerr == nil {
		if err != nil {
			repo.Status = domain.RepositoryLagging
		} else {
			repo.Status = domain.RepositoryHealthy
		}
		repo.UpdatedAt = time.Now()
		_ = s.store.UpdateRepository(ctx, repo)
		s.events.Publish("repository.updated", repo)
	}
}

// EnsureEmbeddingsAsync backfills vectors for blobs uploaded outside a sync
// job (the ACE batch-upload path).
func (s *Service) EnsureEmbeddingsAsync(blobNames []string) {
	if len(blobNames) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cfg := s.embeddingConfig(ctx)
		if !cfg.enabled() {
			return
		}
		_, _ = s.embedMissing(ctx, cfg, blobNames)
	}()
}

// StartEmbeddingBackfill resumes vector backfill for chunks that never got
// embeddings — typically because a restart killed the async worker an upload
// had started (upload-triggered workers only ever cover their own blobs, so
// nothing else would retry). Runs shortly after boot, then periodically as a
// safety net; the claim mechanism prevents double work with upload workers.
func (s *Service) StartEmbeddingBackfill() {
	go func() {
		time.Sleep(15 * time.Second)
		for {
			s.drainMissingEmbeddings()
			time.Sleep(10 * time.Minute)
		}
	}()
}

func (s *Service) drainMissingEmbeddings() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		cfg := s.embeddingConfig(ctx)
		if !cfg.enabled() {
			cancel()
			return
		}
		names, err := s.store.BlobsMissingEmbeddings(ctx, cfg.Model, 100)
		if err != nil {
			slog.Warn("embedding backfill: listing missing blobs failed", "error", err)
		}
		if err != nil || len(names) == 0 {
			cancel()
			return
		}
		done, err := s.embedMissing(ctx, cfg, names)
		cancel()
		if err != nil {
			slog.Warn("embedding backfill: batch error", "embedded", done, "error", err)
		} else if done > 0 {
			slog.Info("embedding backfill: batch committed", "embedded", done)
		}
		// Zero progress means the embedder is down (its cooldown handles
		// retries) or another worker holds the claims; either way stop and
		// let the next periodic pass pick it up.
		if err != nil || done == 0 {
			return
		}
	}
}

// embedMissing embeds every chunk of the given blobs that has no vector for
// the configured model yet. Blobs are claimed so concurrent sync jobs and the
// batch-upload path never embed the same blob twice; already-embedded chunks
// are skipped, which makes retries after partial failures cheap.
func (s *Service) embedMissing(ctx context.Context, cfg EmbeddingConfig, blobNames []string) (int, error) {
	claimed := s.claimEmbedding(blobNames)
	defer s.releaseEmbedding(claimed)
	if len(claimed) == 0 {
		return 0, nil
	}
	chunks, err := s.store.ChunksByBlobNames(ctx, claimed)
	if err != nil || len(chunks) == 0 {
		return 0, err
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	existing, err := s.store.EmbeddingsByChunkIDs(ctx, ids, cfg.Model)
	if err != nil {
		return 0, err
	}
	// Vectors are keyed by content hash, so chunks sharing a hash need only
	// one embedding call — keep the first of each (hash-less legacy chunks
	// all pass). This matters during rapid re-uploads, when two versions of
	// a file are pending at once and most chunks are shared between them.
	seenHash := map[string]bool{}
	pending := make([]domain.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if _, ok := existing[c.ID]; ok {
			continue
		}
		if c.ContentHash != "" {
			if seenHash[c.ContentHash] {
				continue
			}
			seenHash[c.ContentHash] = true
		}
		pending = append(pending, c)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	needed := map[string]bool{}
	for _, c := range pending {
		needed[c.BlobName] = true
	}
	uniq := make([]string, 0, len(needed))
	for name := range needed {
		uniq = append(uniq, name)
	}
	blobs, err := s.store.BlobsByNames(ctx, uniq)
	if err != nil {
		return 0, err
	}
	blobContent := make(map[string]string, len(blobs))
	blobPath := make(map[string]string, len(blobs))
	for _, b := range blobs {
		blobContent[b.Name] = b.Content
		blobPath[b.Name] = b.Path
	}
	texts := make([]string, 0, len(pending))
	for _, c := range pending {
		content, ok := blobContent[c.BlobName]
		if !ok {
			return 0, fmt.Errorf("blob %s: %w", c.BlobName, store.ErrNotFound)
		}
		texts = append(texts, chunkText(content, c))
	}
	// Summary enrichment rides the background path only: summaries are
	// generated before embedding so the vector includes them, then persisted
	// for rerank-document enrichment at query time. Chunks the budget (or a
	// missing enhancer configuration) skips embed with path+symbol enrichment
	// alone. Two tiers: deterministic rule descriptions first (free, always
	// on), then the budgeted LLM pass for config-ish files no rule covered.
	applySummaries := func(summaries map[string]string) {
		if len(summaries) == 0 {
			return
		}
		_ = s.store.UpdateChunkSummaries(ctx, summaries)
		for i := range pending {
			if summary, ok := summaries[pending[i].ID]; ok {
				pending[i].Summary = summary
			}
		}
	}
	applySummaries(ruleSummaries(pending, blobPath, blobContent))
	if enh := s.enhanceConfig(ctx); enh.summariesEnabled() {
		applySummaries(s.summarizeChunks(ctx, enh, pending, texts, blobPath))
	}
	for i, c := range pending {
		texts[i] = embedDocText(blobPath[c.BlobName], c, texts[i])
	}
	// Embed and commit in independent concurrent groups: a deadline or API
	// error near the end of a big backlog batch must not discard vectors
	// already computed (the old all-or-nothing commit wedged the backfill on
	// one oversized batch forever), and bounded concurrency keeps a whole
	// project's backlog inside the drain loop's time budget.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		done     int
		firstErr error
	)
	sem := make(chan struct{}, embedCommitConcurrency)
	for start := 0; start < len(pending); start += embedCommitSize {
		end := start + embedCommitSize
		if end > len(pending) {
			end = len(pending)
		}
		group, groupTexts := pending[start:end], texts[start:end]
		wg.Add(1)
		sem <- struct{}{}
		go func(group []domain.Chunk, groupTexts []string) {
			defer wg.Done()
			defer func() { <-sem }()
			vectors, err := embedTexts(ctx, cfg, groupTexts)
			if err == nil {
				puts := make([]domain.ChunkEmbedding, 0, len(group))
				for i, c := range group {
					puts = append(puts, domain.ChunkEmbedding{ChunkID: c.ID, ContentHash: c.ContentHash, ModelID: cfg.Model, Dims: len(vectors[i]), Vector: vectors[i]})
				}
				if err = s.store.PutEmbeddings(ctx, puts); err == nil {
					mu.Lock()
					done += len(puts)
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}(group, groupTexts)
	}
	wg.Wait()
	return done, firstErr
}

func (s *Service) claimEmbedding(names []string) []string {
	s.embedMu.Lock()
	defer s.embedMu.Unlock()
	out := make([]string, 0, len(names))
	for _, n := range names {
		if s.embedding[n] {
			continue
		}
		s.embedding[n] = true
		out = append(out, n)
	}
	return out
}
func (s *Service) releaseEmbedding(names []string) {
	s.embedMu.Lock()
	defer s.embedMu.Unlock()
	for _, n := range names {
		delete(s.embedding, n)
	}
}

func (s *Service) failJob(ctx context.Context, repo domain.Repository, job domain.IndexJob, cause error) (domain.IndexJob, error) {
	job.Status = domain.JobFailed
	job.Stage = "failed"
	job.Error = cause.Error()
	job.DurationMS = time.Since(job.CreatedAt).Milliseconds()
	job.CompletedAt = time.Now()
	_ = s.store.UpdateJob(ctx, job)
	repo.Status = domain.RepositoryFailed
	repo.IndexLagMS = job.DurationMS
	repo.UpdatedAt = time.Now()
	_ = s.store.UpdateRepository(ctx, repo)
	s.events.Publish("index.failed", job)
	return job, cause
}

// ResolveCheckpoint materializes the effective blob set for an ACE search:
// base checkpoint minus deleted blobs plus added blobs. When the set changes a
// new checkpoint snapshot is persisted so the client can send deltas next time.
func (s *Service) ResolveCheckpoint(ctx context.Context, checkpointID string, added, deleted []string) (string, []string, error) {
	set := map[string]bool{}
	if checkpointID != "" {
		snapshot, err := s.store.SnapshotByID(ctx, checkpointID)
		if err != nil {
			// Unknown checkpoints must fail loudly: silently continuing with
			// only the delta blobs would search a fraction of the workspace.
			return "", nil, fmt.Errorf("checkpoint %s: %w", checkpointID, err)
		}
		for _, name := range snapshot.BlobNames {
			set[name] = true
		}
	}
	for _, name := range deleted {
		delete(set, name)
	}
	for _, name := range added {
		set[name] = true
	}
	blobs := make([]string, 0, len(set))
	for name := range set {
		blobs = append(blobs, name)
	}
	sort.Strings(blobs)
	if checkpointID != "" && len(added) == 0 && len(deleted) == 0 {
		return checkpointID, blobs, nil
	}
	// The checkpoint ID is derived from the blob set, so clients that never
	// adopt checkpoints (ace-tool-rs sends the full blob list every search)
	// reuse one snapshot per workspace state instead of persisting a new one
	// per query.
	id := checkpointName(blobs)
	if _, err := s.store.SnapshotByID(ctx, id); err == nil {
		return id, blobs, nil
	}
	if err := s.store.CreateSnapshot(ctx, domain.Snapshot{ID: id, ParentID: checkpointID, BlobNames: blobs, CreatedAt: time.Now()}); err != nil {
		return "", nil, err
	}
	return id, blobs, nil
}

// checkpointName is content-addressed over the sorted visible blob set.
func checkpointName(blobs []string) string {
	h := sha256.New()
	for _, name := range blobs {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{'\n'})
	}
	return "ckpt_" + hex.EncodeToString(h.Sum(nil))[:32]
}

// SearchBlobNames runs retrieval over an explicit blob set — the ACE search
// path, where visibility comes from a resolved checkpoint rather than a
// repository manifest.
func (s *Service) SearchBlobNames(ctx context.Context, query string, names []string, maxOutput int) (SearchResponse, error) {
	// Heavy workspaces first pass through the in-database prefilter
	// (prefilter.go): only the files nominated by the lexical / semantic /
	// structural signals get loaded and ranked, which is what keeps a
	// 12k-file project inside the latency budget. "Heavy" is decided by file
	// count or by stored chunk total (shouldPrefilter) — chunk-dense mid-size
	// projects cost like large ones. The prefilter also settles ghost-blob
	// accounting (missing = referenced but never uploaded), since skipping
	// most blobs is now intentional, and hands over the query-embed channel
	// it started so the pipeline reuses the same embedding call.
	requested := len(names)
	missing := -1
	var embedCh <-chan queryEmbedResult
	if counts, heavy := s.shouldPrefilter(ctx, names); heavy {
		if subset, found, ch := s.prefilterBlobNames(ctx, query, names, counts); ch != nil {
			embedCh = ch
			missing = requested - found
			if len(subset) > 0 {
				names = subset
			}
		}
	}
	// One round trip for the whole workspace: per-name queries would cost a
	// SELECT plus a decrypt handshake per blob, which dominates search latency
	// on workspaces with thousands of files.
	blobs, err := s.store.BlobsByNames(ctx, names)
	if err != nil {
		return SearchResponse{}, err
	}
	byName := make(map[string]domain.Blob, len(blobs))
	for _, b := range blobs {
		byName[b.Name] = b
	}
	files := make([]domain.RepositoryFile, 0, len(blobs))
	for _, name := range names {
		blob, ok := byName[name]
		if !ok {
			continue
		}
		files = append(files, domain.RepositoryFile{BlobName: blob.Name, Path: blob.Path, Content: blob.Content})
	}
	result := s.searchWith(ctx, query, files, maxOutput, embedCh)
	// Ghost files: the checkpoint references blob names whose content never
	// made it into the store (the upload was rejected — quota, size — but the
	// client cached its manifest as sent and won't retry). Without this note
	// retrieval quietly degrades with no signal to the caller.
	if missing < 0 {
		missing = requested - len(files)
	}
	if missing > 0 {
		result.MissingBlobs = missing
		result.FormattedRetrieval = fmt.Sprintf("Note: %d file(s) in your workspace snapshot were never uploaded to the server (an earlier upload was likely rejected by a storage or rate quota), so their content is invisible to this and future searches. Delete the local .ace-tool/index.bin cache to force a full re-upload once the quota allows.\n", missing) + result.FormattedRetrieval
	}
	// ACE searches are the only search traffic in MCP-only deployments;
	// without this sample the overview latency profile sits at zero forever.
	// The sample is retrieval-stage time only: inline embedding of vectors a
	// fresh upload hasn't backfilled yet is one-off indexing debt, and
	// counting it made every "first search after upload" a fake P99 outlier.
	_ = s.store.RecordMetric(ctx, domain.MetricPoint{Scope: "ace", Name: "search_latency_ms", Value: max(0, result.DurationMS-result.InlineEmbedMS), Timestamp: time.Now()})
	// Companion samples for the admin analytics page: a 0/1 per search feeds
	// the hourly degraded-rate curve, and the inline-embedding debt (recorded
	// only when paid) feeds the first-query cost curve.
	degradedVal := 0.0
	if result.Degraded {
		degradedVal = 1
	}
	_ = s.store.RecordMetric(ctx, domain.MetricPoint{Scope: "ace", Name: "search_degraded", Value: degradedVal, Timestamp: time.Now()})
	if result.InlineEmbedMS > 0 {
		_ = s.store.RecordMetric(ctx, domain.MetricPoint{Scope: "ace", Name: "inline_embed_ms", Value: result.InlineEmbedMS, Timestamp: time.Now()})
	}
	s.events.Publish("latency.updated", map[string]any{"duration_ms": result.DurationMS})
	return result, nil
}

// SearchRepository runs retrieval over the repository's current file manifest
// (the console path) and records the observed latency as a metric.
func (s *Service) SearchRepository(ctx context.Context, repoID, query string, maxOutput int) (SearchResponse, error) {
	files, err := s.store.RepositoryFiles(ctx, repoID)
	if err != nil {
		return SearchResponse{}, err
	}
	start := time.Now()
	result := s.search(ctx, query, files, maxOutput)
	_ = s.store.RecordMetric(ctx, domain.MetricPoint{Scope: "repository", RepositoryID: repoID, Name: "search_latency_ms", Value: max(0, result.DurationMS-result.InlineEmbedMS), Timestamp: time.Now()})
	if repo, err := s.store.RepositoryByID(ctx, repoID); err == nil {
		repo.SearchP95MS = result.DurationMS
		repo.UpdatedAt = time.Now()
		_ = s.store.UpdateRepository(ctx, repo)
	}
	s.events.Publish("latency.updated", map[string]any{"repository_id": repoID, "duration_ms": time.Since(start).Seconds() * 1000})
	return result, nil
}

type candidate struct {
	chunk     domain.Chunk
	path      string
	content   string
	lex       float64
	pathScore float64
	symbol    float64
	semantic  float64
	rerank    float64
}

func (c *candidate) structuralTotal() float64 { return c.pathScore + c.symbol }

const (
	rrfK          = 60
	rankDepth     = 100
	maxHits       = 12
	minCosine     = 0.05
	queryEmbedTTL = 8 * time.Second
)

// DefaultMaxOutputTokens is the retrieval output budget applied when neither
// the user preference nor the request specifies one. Kept moderate so a few
// retrievals don't dominate an agent's context window.
const DefaultMaxOutputTokens = 6400

// Latency budget. One search answers within searchBudget of wall clock: every
// external wait below draws on what remains of it instead of spending its own
// full TTL. In-flight calls keep their generous per-call TTLs and their
// cooldown semantics — the pipeline merely stops waiting and continues on the
// signals already in hand, with the existing degraded / low-confidence notes
// telling the caller which paths sat out. When the platform is healthy every
// stage runs exactly as before; only the tail-latency searches that used to
// blow past the budget get trimmed.
const (
	searchBudget    = 5 * time.Second
	queryEmbedWait  = 2 * time.Second         // cap on waiting for the query vector
	expandTermWait  = 1500 * time.Millisecond // cap on waiting for query term expansion
	rerankMinBudget = 800 * time.Millisecond  // below this a rerank round is not worth firing
	rerankRoom      = 1800 * time.Millisecond // time other stages leave for a rerank round
	finishReserve   = 200 * time.Millisecond  // local post-processing tail
)

// search is the retrieval pipeline: chunk-level candidates → three ranking
// paths (BM25 lexical, path/symbol structural, dense semantic) fused with
// reciprocal rank fusion → cross-encoder rerank of the head → curation
// (per-file diversity, adjacent-span merge, small-span padding) → token
// budget → XML context block. Every path degrades independently; a search
// never fails outright because a model server is down.
func (s *Service) search(ctx context.Context, query string, files []domain.RepositoryFile, maxOutput int) SearchResponse {
	return s.searchWith(ctx, query, files, maxOutput, nil)
}

// searchWith is search with an optionally pre-started query embed: the
// large-workspace prefilter already needed the query vector for its semantic
// signal, and handing the channel over means one embedding API call per
// search instead of two.
func (s *Service) searchWith(ctx context.Context, query string, files []domain.RepositoryFile, maxOutput int, embedCh <-chan queryEmbedResult) SearchResponse {
	started := time.Now()
	deadline := started.Add(searchBudget)
	// The query embedding is a network round trip (hundreds of ms against a
	// cloud model), so it runs concurrently with candidate collection and the
	// lexical/structural scoring instead of serializing after them.
	if embedCh == nil {
		embedCh = s.startQueryEmbed(ctx, query)
	}
	// Speculative prefetch only: identifier-free queries are LIKELY broad, so
	// the LLM decomposition starts now to hide its latency. Whether broad mode
	// actually engages is decided by the first-pass rerank score below, not by
	// the query's surface shape.
	var decomposeCh <-chan []string
	if queryLooksBroad(query) {
		decomposeCh = s.startDecompose(ctx, query)
	}
	// Queries with segments in spaceless scripts tokenize into opaque blobs
	// the lexical/structural paths can't use; the term expansion (English
	// identifiers the code would contain) starts now, concurrently with
	// everything below, and folds in before the first rerank.
	var expandCh <-chan []string
	if needsTermExpansion(query) {
		expandCh = s.startTermExpand(ctx, query)
	}
	candidates := s.collectCandidates(ctx, files)
	// Three retrieval paths fused with reciprocal rank fusion: BM25 over
	// code-aware tokens, path/symbol structure, and dense vectors.
	s.bm25Score(ctx, candidates, codeTokens(query))
	structuralTerms := tokens(query)
	for _, cand := range candidates {
		scoreStructural(cand, structuralTerms)
	}
	lexRank := rankBy(candidates, func(c *candidate) float64 { return c.lex })
	structRank := rankBy(candidates, func(c *candidate) float64 { return c.structuralTotal() })
	// The query vector earns a bounded wait, not an open-ended one: past
	// queryEmbedWait the pipeline continues without the semantic path (the
	// in-flight embed still lands in the buffered channel and is discarded;
	// its own TTL and cooldown are untouched).
	var embedRes queryEmbedResult
	select {
	case embedRes = <-embedCh:
	case <-time.After(queryEmbedWait):
	}
	semRank, degraded, inlineEmbedMS := s.semanticRank(ctx, embedRes, candidates, deadline)

	fused := map[int]float64{}
	for _, rank := range [][]int{lexRank, structRank, semRank} {
		for r, idx := range rank {
			fused[idx] += 1.0 / float64(rrfK+r+1)
		}
	}
	// Expanded terms fold in before the first rerank. For a spaceless-script
	// query the lexical/structural paths are dead without them (the tokenizer
	// bigrams only cover same-script comment text), so the expansion earns a
	// bounded wait like the query embed does, instead of a zero-blocking
	// drain: the LLM started before candidate collection, so most of
	// expandTermWait has usually already elapsed. Past the cap (or the
	// remaining global budget) the pipeline continues; a late arrival still
	// folds in at the broad-mode stage below.
	var expandTerms []string
	if expandCh != nil {
		wait := min(expandTermWait, time.Until(deadline)-finishReserve)
		select {
		case expandTerms = <-expandCh:
			expandCh = nil // single write; a second receive would hang
			if len(expandTerms) > 0 {
				s.fuseExpandedTerms(ctx, candidates, expandTerms, fused)
			}
		case <-time.After(max(wait, 0)):
		}
	}
	// Architecture/config-intent queries get the manifest prior in the first
	// pass: waiting for broad mode would lose it whenever some code chunk
	// scores ≥ the broad threshold on rerank and keeps the pipeline focused.
	structuralIntent := queryWantsStructure(query)
	// Structural intent or a degraded semantic path (e.g. vectors still
	// backfilling after an upload) makes broad mode likely, so the
	// decomposition prefetch widens to those cases: it runs behind the
	// first-pass rerank instead of serializing after it. Clearly focused
	// queries outside these gates still skip the speculative LLM call.
	if decomposeCh == nil && (structuralIntent || degraded) {
		decomposeCh = s.startDecompose(ctx, query)
	}
	applyManifestPrior := func() {
		boost := make([]float64, len(candidates))
		for i, c := range candidates {
			boost[i] = manifestBoost(logicalPath(c.path))
		}
		for r, idx := range rankByValues(candidates, boost) {
			fused[idx] += 1.0 / float64(rrfK+r+1)
		}
	}
	if structuralIntent {
		applyManifestPrior()
	}
	order := buildOrder(candidates, fused)
	// The cross-encoder shares the bi-encoder's cross-lingual blind spot: a
	// spaceless-script query against English-identifier code can score near
	// zero on exactly the right chunks while literal English fragments
	// ("database" in a comment) score high. Whenever the expansion terms are
	// already in hand they join every rerank as one extra parallel query under
	// the existing per-document maximum — vocabulary in the documents' own
	// language, at zero added wait.
	expandQueries := func(base []string) []string {
		if len(expandTerms) == 0 {
			return base
		}
		return append(append([]string(nil), base...), strings.Join(expandTerms, " "))
	}
	// First pass: rerank against the original query (plus expansion terms when
	// available). A strong head score means a chunk directly answers the query
	// — the focused pipeline stands unchanged. A weak head means the answer is
	// spread across the codebase (or absent), so retrieval re-runs in broad
	// mode. Without a rerank signal the surface heuristic is the fallback
	// classifier.
	// A live decompose prefetch means broad mode is anticipated, so the first
	// pass holds room for the second rerank round; clearly focused queries get
	// everything that is left — reserving for a stage that will not run would
	// clip healthy first-pass rounds in the common case.
	firstBudget := time.Until(deadline) - finishReserve
	if decomposeCh != nil {
		firstBudget -= rerankRoom
	}
	order, rerankTop, reranked := s.applyRerank(ctx, query, expandQueries(nil), candidates, order, false, firstBudget)
	broad := rerankTop < broadEngageTop
	if !reranked {
		broad = queryLooksBroad(query)
	}
	var subQueries []string
	if broad {
		// Second pass, coverage-oriented: sub-query decomposition and the
		// manifest-file prior fold into the fusion map, the order is rebuilt
		// (first-pass truncation is void), and the rerank re-runs against the
		// original query plus every sub-query in parallel, keeping the
		// per-document maximum.
		if decomposeCh == nil {
			decomposeCh = s.startDecompose(ctx, query)
		}
		// Bounded wait, keeping at least a minimal rerank round in reserve:
		// the decomposition was usually prefetched and is ready by now; when it
		// is not, going wide on the manifest prior and expansion terms alone
		// beats spending the rest of the budget on an LLM round trip.
		select {
		case subQueries = <-decomposeCh:
		case <-time.After(time.Until(deadline) - rerankMinBudget - finishReserve):
		}
		// The decompose wait above covered an LLM round trip, so the term
		// expansion (launched earlier, smaller output) is normally ready now;
		// still never worth blocking on if it isn't.
		if expandCh != nil {
			select {
			case expandTerms = <-expandCh:
				expandCh = nil
				if len(expandTerms) > 0 {
					s.fuseExpandedTerms(ctx, candidates, expandTerms, fused)
				}
			default:
			}
		}
		if !structuralIntent { // already folded into fused in the first pass
			applyManifestPrior()
		}
		s.fuseSubQueries(ctx, candidates, subQueries, fused)
		order = buildOrder(candidates, fused)
		if o, top, ok := s.applyRerank(ctx, query, expandQueries(subQueries), candidates, order, true, time.Until(deadline)-finishReserve); ok {
			order, rerankTop, reranked = o, top, ok
		}
	}
	// Curation: per-file diversity cap, adjacent-span merging and small-span
	// padding shape the final context window.
	limit, fileCap := maxHits, perFileCap
	if broad {
		limit, fileCap = broadMaxHits, broadPerFileCap
	}
	order = diversifyOrder(candidates, order, limit, fileCap)
	// One-hop structural expansion rides along at the tail: referenced
	// type/behavioral definitions and cross-file callers rarely rank on
	// their own relevance, but complete the call chain.
	order = expandRelations(candidates, order, fused)
	contentByBlob := map[string]string{}
	for _, f := range files {
		if _, ok := contentByBlob[f.BlobName]; !ok {
			contentByBlob[f.BlobName] = f.Content
		}
	}
	// Split oversized files ("path#chunkNofM" pseudo blobs) are stitched back
	// together so hits merge across part boundaries and report real paths
	// with true line numbers.
	pseudo, logicalContent := buildPseudoIndex(files)
	hits := curateHits(candidates, order, fused, contentByBlob, pseudo, logicalContent)
	if broad {
		// Broad answers are candidate lists, not deep dives: long excerpts
		// shrink to skeletons (head + term-matching lines, elided runs cite
		// their line range) so the token budget spreads across many files and
		// the agent reads the cited ranges for full bodies.
		skelTerms := structuralTerms
		for _, sub := range subQueries {
			skelTerms = append(skelTerms, tokens(sub)...)
		}
		if len(expandTerms) > 0 {
			skelTerms = append(skelTerms, tokens(strings.Join(expandTerms, " "))...)
		}
		for i := range hits {
			hits[i].Content = skeletonize(hits[i].Path, hits[i].StartLine, hits[i].Content, skelTerms)
		}
	}
	budget := maxOutput
	if budget <= 0 {
		budget = DefaultMaxOutputTokens
	}
	selected := make([]SearchHit, 0, len(hits))
	used := 0
	for _, hit := range hits {
		estimate := len(hit.Content) / 4
		if used+estimate > budget && len(selected) > 0 {
			continue
		}
		used += estimate
		selected = append(selected, hit)
	}
	related := relatedSymbolHints(candidates, selected, pseudo)
	// A reranked head this weak means the query has no real answer in this
	// codebase — surface that instead of letting padded weak matches imply
	// the implementation was found.
	confidence := ""
	if reranked && rerankTop < rerankWeakTop {
		confidence = "low"
	}
	formatted := format(selected, related, confidence == "low", broad, degraded)
	return SearchResponse{FormattedRetrieval: formatted, Hits: selected, RelatedSymbols: related, Confidence: confidence, DurationMS: math.Round(time.Since(started).Seconds()*100000) / 100, InlineEmbedMS: math.Round(inlineEmbedMS*100) / 100, CandidateCount: len(candidates), TokenEstimate: used, Degraded: degraded}
}

// collectCandidates expands the visible files into chunk-level candidates.
// Chunks normally come from the store (written at upload time); blobs indexed
// before the chunk layer existed are chunked on the fly, which leaves
// TokenCount at zero and routes them through the BM25 fallback path.
func (s *Service) collectCandidates(ctx context.Context, files []domain.RepositoryFile) []*candidate {
	fileByBlob := map[string]domain.RepositoryFile{}
	blobNames := make([]string, 0, len(files))
	for _, f := range files {
		// Tool-dir blobs indexed before the exclusion existed must not
		// resurface via the chunk-on-the-fly fallback below.
		if toolPathExcluded(f.Path) {
			continue
		}
		if _, ok := fileByBlob[f.BlobName]; ok {
			continue
		}
		fileByBlob[f.BlobName] = f
		blobNames = append(blobNames, f.BlobName)
	}
	stored, _ := s.store.ChunksByBlobNames(ctx, blobNames)
	chunksByBlob := map[string][]domain.Chunk{}
	for _, c := range stored {
		chunksByBlob[c.BlobName] = append(chunksByBlob[c.BlobName], c)
	}
	candidates := []*candidate{}
	for _, name := range blobNames {
		f := fileByBlob[name]
		chunks := chunksByBlob[name]
		if len(chunks) == 0 {
			// Blobs indexed before the chunk layer existed: chunk on the fly.
			chunks = ChunkBlob(name, f.Path, f.Content)
		}
		lines := strings.Split(f.Content, "\n")
		for _, c := range chunks {
			start := c.StartLine - 1
			end := min(c.EndLine, len(lines))
			if start < 0 || start >= len(lines) {
				continue
			}
			candidates = append(candidates, &candidate{chunk: c, path: f.Path, content: strings.Join(lines[start:end], "\n")})
		}
	}
	return candidates
}

// queryEmbedResult carries the embedded query from the background goroutine
// into semanticRank; ok is false when the semantic path cannot contribute
// (embeddings disabled, server on cooldown, or the embed call failed).
type queryEmbedResult struct {
	cfg EmbeddingConfig
	vec []float32
	ok  bool
}

// startQueryEmbed launches the query embedding concurrently with candidate
// collection and lexical scoring. The channel is buffered so the goroutine
// never blocks, and failures arm the same cooldown as before.
func (s *Service) startQueryEmbed(ctx context.Context, query string) <-chan queryEmbedResult {
	ch := make(chan queryEmbedResult, 1)
	go func() {
		cfg := s.embeddingConfig(ctx)
		if !cfg.enabled() {
			ch <- queryEmbedResult{}
			return
		}
		// A dead embedding server must not add its timeout to every search:
		// after one failure the semantic path is skipped for a cooldown window.
		s.embedMu.Lock()
		down := time.Now().Before(s.embedDownUntil)
		s.embedMu.Unlock()
		if down {
			ch <- queryEmbedResult{}
			return
		}
		qctx, cancel := context.WithTimeout(ctx, queryEmbedTTL)
		defer cancel()
		vec, err := embedQuery(qctx, cfg, query)
		if err != nil {
			s.embedMu.Lock()
			s.embedDownUntil = time.Now().Add(30 * time.Second)
			s.embedMu.Unlock()
			ch <- queryEmbedResult{}
			return
		}
		ch <- queryEmbedResult{cfg: cfg, vec: vec, ok: true}
	}()
	return ch
}

// semanticRank scores candidates against the embedded query and returns
// their ranking plus a degraded flag. Degraded means the semantic path did
// not contribute: embeddings disabled, the server on cooldown, the query
// embed failed, or no candidate vectors could be produced in time.
//
// Scoring happens inside the database (pgvector inner product): one query
// vector goes in, scalars come out, so the cost per search is bounded by ID
// lookups instead of shipping megabytes of float32 to Go — which is also why
// no application-side ANN structure exists anymore.
func (s *Service) semanticRank(ctx context.Context, q queryEmbedResult, candidates []*candidate, deadline time.Time) (rank []int, degraded bool, inlineMS float64) {
	if !q.ok || len(candidates) == 0 {
		return nil, true, 0
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.chunk.ID)
	}
	scores, err := s.store.VectorScores(ctx, ids, q.cfg.Model, q.vec)
	if err != nil {
		return nil, true, 0
	}
	// Vectors can be missing right after an upload (backfill still running),
	// after an embedding-model switch, or — transiently — while the pgvector
	// migration is still copying legacy rows. The legacy fetch covers the
	// migration window (it reads the old bytea tables and costs nothing once
	// they are dropped); whatever remains is embedded inline, head first, so
	// even the first search answers with the semantic path.
	missing := make([]string, 0)
	for _, id := range ids {
		if _, ok := scores[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		// Everything inside this block is indexing debt paid inline, not
		// retrieval work; its duration is reported separately so the latency
		// metric can subtract it (cold first queries would otherwise dominate
		// the overview P95/P99).
		inlineStart := time.Now()
		vectors, err := s.store.EmbeddingsByChunkIDs(ctx, missing, q.cfg.Model)
		if err != nil {
			vectors = map[string][]float32{}
		}
		if len(vectors) < len(missing) {
			vectors = s.embedCandidatesNow(ctx, q.cfg, candidates, missing, vectors, deadline)
		}
		for id, v := range vectors {
			scores[id] = dot(q.vec, v)
		}
		inlineMS = time.Since(inlineStart).Seconds() * 1000
	}
	if len(scores) == 0 {
		return nil, true, inlineMS
	}
	for _, c := range candidates {
		if v, ok := scores[c.chunk.ID]; ok {
			c.semantic = v
		}
	}
	rank = rankBy(candidates, func(c *candidate) float64 {
		if c.semantic < minCosine {
			return 0
		}
		return c.semantic
	})
	return rank, len(rank) == 0, inlineMS
}

// Bounds for the inline candidate backfill: enough chunks to cover the RRF
// fusion depth (100) with headroom, and a deadline that keeps the first
// search after an upload at seconds, not minutes.
const (
	syncEmbedLimit = 256
	syncEmbedSlice = 64
	syncEmbedTTL   = 20 * time.Second
)

// embedCandidatesNow embeds candidates that have no vector yet so the first
// search after an upload (or a model switch) already gets the semantic path
// instead of a degraded lexical-only answer. The strongest lexical/structural
// candidates go first and work proceeds in slices, so a deadline mid-way
// still leaves the head embedded. Chunk texts come from the candidates
// themselves (no store round trips), PutEmbeddings upserts, and the async
// blob-level backfill is queued regardless, so racing the background worker
// is harmless and the tail still fills in later.
func (s *Service) embedCandidatesNow(ctx context.Context, cfg EmbeddingConfig, candidates []*candidate, ids []string, vectors map[string][]float32, deadline time.Time) map[string][]float32 {
	byID := make(map[string]*candidate, len(candidates))
	for _, c := range candidates {
		byID[c.chunk.ID] = c
	}
	missing := make([]*candidate, 0, len(ids)-len(vectors))
	pendingBlobs := map[string]bool{}
	for _, id := range ids {
		if _, ok := vectors[id]; ok {
			continue
		}
		if c := byID[id]; c != nil {
			missing = append(missing, c)
			pendingBlobs[c.chunk.BlobName] = true
		}
	}
	if len(missing) == 0 {
		return vectors
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i].lex+missing[i].structuralTotal() > missing[j].lex+missing[j].structuralTotal()
	})
	inline := missing
	if len(inline) > syncEmbedLimit {
		inline = inline[:syncEmbedLimit]
	}
	// The inline backfill spends only what the search budget can spare after
	// holding room for a rerank round; a non-positive allowance yields an
	// already-expired context, the loop no-ops, and everything stays with the
	// async worker below. First-search-after-upload coverage bows to the
	// latency target, not the other way around.
	ectx, cancel := context.WithTimeout(ctx, min(syncEmbedTTL, time.Until(deadline)-rerankRoom-finishReserve))
	defer cancel()
	puts := make([]domain.ChunkEmbedding, 0, len(inline))
	for start := 0; start < len(inline); start += syncEmbedSlice {
		if ectx.Err() != nil {
			break
		}
		end := min(start+syncEmbedSlice, len(inline))
		texts := make([]string, 0, end-start)
		for _, c := range inline[start:end] {
			// Inline embeds cannot wait for the summarizer, but the free
			// path+symbol enrichment (plus any summary already persisted)
			// keeps them in the same document shape as background vectors.
			texts = append(texts, embedDocText(c.path, c.chunk, c.content))
		}
		embedded, err := embedTexts(ectx, cfg, texts)
		if err != nil {
			break
		}
		for i, c := range inline[start:end] {
			vectors[c.chunk.ID] = embedded[i]
			puts = append(puts, domain.ChunkEmbedding{ChunkID: c.chunk.ID, ContentHash: c.chunk.ContentHash, ModelID: cfg.Model, Dims: len(embedded[i]), Vector: embedded[i]})
		}
	}
	if len(puts) > 0 {
		_ = s.store.PutEmbeddings(ctx, puts)
	}
	// Everything not embedded inline (overflow, deadline, or failure) still
	// backfills asynchronously together with the rest of each touched blob
	// (claimEmbedding dedupes, embedMissing skips chunks that just landed).
	blobs := make([]string, 0, len(pendingBlobs))
	for name := range pendingBlobs {
		blobs = append(blobs, name)
	}
	s.EnsureEmbeddingsAsync(blobs)
	return vectors
}

// buildOrder sorts the fused RRF map into a deterministic ranking; it runs
// once for focused queries and again after broad-mode fusion widens the map.
func buildOrder(candidates []*candidate, fused map[int]float64) []int {
	order := make([]int, 0, len(fused))
	for idx := range fused {
		order = append(order, idx)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if fused[a] == fused[b] {
			if candidates[a].path == candidates[b].path {
				return candidates[a].chunk.StartLine < candidates[b].chunk.StartLine
			}
			return candidates[a].path < candidates[b].path
		}
		return fused[a] > fused[b]
	})
	return order
}

// rankBy returns candidate indices with score > 0, best first, capped at
// rankDepth for reciprocal rank fusion.
func rankBy(candidates []*candidate, score func(*candidate) float64) []int {
	idx := []int{}
	for i, c := range candidates {
		if score(c) > 0 {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		a, b := idx[i], idx[j]
		if score(candidates[a]) == score(candidates[b]) {
			return candidates[a].path < candidates[b].path
		}
		return score(candidates[a]) > score(candidates[b])
	})
	if len(idx) > rankDepth {
		idx = idx[:rankDepth]
	}
	return idx
}

// scoreStructural fills the structural path: substring matches of query terms
// against the file path and prefix/exact matches against the chunk symbol.
// Exact symbol hits dominate so "where is X defined" queries surface the
// declaration above mere mentions. The value-returning core serves sub-query
// scoring without touching the primary scores.
func scoreStructural(c *candidate, terms []string) {
	c.pathScore, c.symbol = structuralValues(c, terms)
}

func structuralValues(c *candidate, terms []string) (pathScore, symbol float64) {
	pathLower := strings.ToLower(c.path)
	symbolLower := strings.ToLower(c.chunk.Symbol)
	for _, term := range terms {
		if term == "" {
			continue
		}
		if strings.Contains(pathLower, term) {
			pathScore += 2.5
		}
		if symbolLower != "" {
			if symbolLower == term {
				symbol += 6
			} else if strings.HasPrefix(term, symbolLower) || strings.HasPrefix(symbolLower, term) {
				symbol += 3
			}
		}
	}
	return pathScore, symbol
}

func tokens(value string) []string {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			return unicode.ToLower(r)
		}
		return ' '
	}, value)
	parts := strings.Fields(normalized)
	seen := map[string]bool{}
	out := []string{}
	for _, part := range parts {
		for _, piece := range strings.FieldsFunc(part, func(r rune) bool { return r == '_' || r == '-' }) {
			if len([]rune(piece)) > 1 && !seen[piece] {
				seen[piece] = true
				out = append(out, piece)
			}
		}
	}
	return out
}
func format(hits []SearchHit, related []RelatedSymbol, weak, broad, degraded bool) string {
	if len(hits) == 0 {
		return "No relevant code context found for your query."
	}
	var b strings.Builder
	if degraded {
		// The semantic path silently dropping out (embedding server slow or
		// down, vectors still backfilling) looks identical to bad retrieval
		// from the outside; the note lets the caller tell platform trouble
		// from a genuinely poor match and retry later if the query depended
		// on meaning rather than keywords.
		b.WriteString("Note: the semantic index did not participate in this search (embedding backend unavailable or still indexing) — results are keyword/structure matches only and may miss conceptually related code. Retrying later may give better results.\n")
	}
	if weak {
		// Without this note, weak matches padded into the window read as "the
		// implementation was found" — misleading when the real code lives in
		// another repository.
		b.WriteString("Note: no strongly matching code was found for this query. The fragments below are weak matches — the functionality you are looking for may not be implemented in this codebase (it could live in a separate repository or service).\n")
	}
	if broad {
		// Broad mode trades depth for coverage; without this note the elided
		// skeletons read as if the shown lines were all there is.
		b.WriteString("Note: exploratory query — the excerpts below are trimmed candidates from across the codebase. Long sections are elided; read the cited file paths and line ranges for full detail.\n")
	}
	b.WriteString("<codebase_context>\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "  <file path=\"%s\" start_line=\"%d\" end_line=\"%d\">\n    <![CDATA[%s]]>\n  </file>\n", html.EscapeString(h.Path), h.StartLine, h.EndLine, strings.ReplaceAll(h.Content, "]]>", "]]]]><![CDATA[>"))
	}
	b.WriteString("</codebase_context>")
	if len(related) > 0 {
		// Grep leads for the calling agent: symbols the context references
		// whose definitions did not fit the window.
		b.WriteString("\n<related_symbols hint=\"referenced by the context above and defined in this codebase, but not shown; search for them to explore further\">\n")
		for _, r := range related {
			fmt.Fprintf(&b, "  <symbol name=\"%s\" kind=\"%s\" path=\"%s\"/>\n", html.EscapeString(r.Name), html.EscapeString(r.Kind), html.EscapeString(r.Path))
		}
		b.WriteString("</related_symbols>")
	}
	return b.String()
}
func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}
func language(path string) string {
	// ACE clients (ace-tool-rs, augment.mjs) upload oversized files as
	// "name.go#chunk1of3" pseudo paths; strip the fragment so extension
	// detection sees the real file name and structure-aware chunking works.
	if i := strings.IndexByte(path, '#'); i >= 0 {
		path = path[:i]
	}
	ext := strings.ToLower(filepath.Ext(path))
	languages := map[string]string{".go": "Go", ".ts": "TypeScript", ".tsx": "TSX", ".js": "JavaScript", ".jsx": "JavaScript", ".py": "Python", ".java": "Java", ".rs": "Rust", ".c": "C", ".h": "C", ".cpp": "C++", ".hpp": "C++", ".md": "Markdown", ".yaml": "YAML", ".yml": "YAML", ".json": "JSON", ".sql": "SQL", ".sh": "Shell", ".css": "CSS", ".html": "HTML", ".toml": "TOML", ".vue": "Vue", ".xml": "XML", ".properties": "Properties"}
	if v, ok := languages[ext]; ok {
		return v
	}
	return "Text"
}
