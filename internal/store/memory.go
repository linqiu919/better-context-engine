package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

type Memory struct {
	mu         sync.RWMutex
	users      map[string]domain.User
	sessions   map[string]domain.Session
	repos      map[string]domain.Repository
	blobs      map[string]domain.Blob
	files      map[string]map[string]domain.RepositoryFile
	chunks   map[string][]domain.Chunk
	postings map[string]map[string]int
	// embeddings is the legacy keyspace (modelID -> chunkID -> vector) for
	// hash-less chunks; hashEmb (modelID -> contentHash -> vector) is the
	// content-addressed keyspace shared across identical chunks. chunkHash
	// mirrors chunks' ID -> ContentHash for vector lookups by chunk ID.
	embeddings map[string]map[string][]float32
	hashEmb    map[string]map[string][]float32
	chunkHash  map[string]string
	snapshots  map[string]domain.Snapshot
	evalCases  map[string]domain.EvalCase
	jobs       map[string]domain.IndexJob
	metrics    []domain.MetricPoint
	audit      []domain.AuditEvent
	settings   map[string]string
	announces  map[string]domain.Announcement
	annDismiss map[string]string // userID -> dismissed announcement ID
	userCkpts  map[string]string
	aceUsage   map[string]*domain.ACEUsage             // key: userID|day|endpoint
	aceProjs   map[string]map[string]domain.ACEProject // userID -> project name -> archive
	quotaUsage map[string]int64                        // key: userID|day|kind
}

func NewMemory() *Memory {
	return &Memory{
		users: map[string]domain.User{}, sessions: map[string]domain.Session{},
		repos: map[string]domain.Repository{}, blobs: map[string]domain.Blob{},
		files: map[string]map[string]domain.RepositoryFile{}, jobs: map[string]domain.IndexJob{}, settings: map[string]string{},
		chunks: map[string][]domain.Chunk{}, postings: map[string]map[string]int{}, embeddings: map[string]map[string][]float32{}, snapshots: map[string]domain.Snapshot{},
		hashEmb: map[string]map[string][]float32{}, chunkHash: map[string]string{},
		evalCases: map[string]domain.EvalCase{}, userCkpts: map[string]string{}, aceUsage: map[string]*domain.ACEUsage{},
		aceProjs: map[string]map[string]domain.ACEProject{}, quotaUsage: map[string]int64{},
		announces: map[string]domain.Announcement{}, annDismiss: map[string]string{},
	}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) CreateUser(_ context.Context, user domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.users {
		if existing.Username == user.Username {
			return ErrConflict
		}
	}
	m.users[user.ID] = user
	return nil
}
func (m *Memory) UpdateUser(_ context.Context, user domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[user.ID]; !ok {
		return ErrNotFound
	}
	m.users[user.ID] = user
	return nil
}
func (m *Memory) UserByUsername(_ context.Context, username string) (domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.Username == username {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}
func (m *Memory) UserByACETokenHash(_ context.Context, hash string) (domain.User, error) {
	if hash == "" {
		return domain.User{}, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.ACETokenHash == hash {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}
func (m *Memory) UserByEmail(_ context.Context, email string) (domain.User, error) {
	if email == "" {
		return domain.User{}, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.Email == email {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}
func (m *Memory) UserByLinuxDoID(_ context.Context, id int64) (domain.User, error) {
	if id == 0 {
		return domain.User{}, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.LinuxDoID == id {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}
func (m *Memory) UserByID(_ context.Context, id string) (domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[id]
	if !ok {
		return domain.User{}, ErrNotFound
	}
	return user, nil
}
func (m *Memory) ListUsers(_ context.Context) ([]domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.User, 0, len(m.users))
	for _, v := range m.users {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (m *Memory) CreateSession(_ context.Context, s domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.TokenHash] = s
	return nil
}
func (m *Memory) SessionByTokenHash(_ context.Context, hash string) (domain.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[hash]
	if !ok || s.ExpiresAt.Before(time.Now()) {
		return domain.Session{}, ErrNotFound
	}
	return s, nil
}
func (m *Memory) DeleteSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}
func (m *Memory) DeleteUserSessions(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.sessions {
		if v.UserID == userID {
			delete(m.sessions, k)
		}
	}
	return nil
}
func (m *Memory) DeleteExpiredSessions(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, v := range m.sessions {
		if v.ExpiresAt.Before(now) {
			delete(m.sessions, k)
		}
	}
	return nil
}

func (m *Memory) CreateRepository(_ context.Context, repo domain.Repository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[repo.ID]; ok {
		return ErrConflict
	}
	m.repos[repo.ID] = repo
	m.files[repo.ID] = map[string]domain.RepositoryFile{}
	return nil
}
func (m *Memory) UpdateRepository(_ context.Context, repo domain.Repository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[repo.ID]; !ok {
		return ErrNotFound
	}
	m.repos[repo.ID] = repo
	return nil
}
func (m *Memory) RepositoryByID(_ context.Context, id string) (domain.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.repos[id]
	if !ok {
		return domain.Repository{}, ErrNotFound
	}
	if u, ok := m.users[v.OwnerID]; ok {
		v.OwnerUsername = u.Username
	}
	return v, nil
}
func (m *Memory) ListRepositories(_ context.Context, ownerID string, all bool) ([]domain.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.Repository{}
	for _, v := range m.repos {
		if all || v.OwnerID == ownerID {
			if u, ok := m.users[v.OwnerID]; ok {
				v.OwnerUsername = u.Username
			}
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (m *Memory) PutBlob(_ context.Context, b domain.Blob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[b.Name]; !ok {
		m.blobs[b.Name] = b
	}
	return nil
}
func (m *Memory) ExistingBlobNames(_ context.Context, names []string) (map[string]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]bool{}
	for _, name := range names {
		if _, ok := m.blobs[name]; ok {
			out[name] = true
		}
	}
	return out, nil
}
func (m *Memory) BlobsByNames(_ context.Context, names []string) ([]domain.Blob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Blob, 0, len(names))
	for _, name := range names {
		if b, ok := m.blobs[name]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}
func (m *Memory) ReplaceRepositoryFiles(_ context.Context, repoID string, upserts []domain.RepositoryFile, deleted []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[repoID]; !ok {
		m.files[repoID] = map[string]domain.RepositoryFile{}
	}
	for _, p := range deleted {
		delete(m.files[repoID], p)
	}
	for _, f := range upserts {
		m.files[repoID][f.Path] = f
	}
	return nil
}
func (m *Memory) RepositoryFiles(_ context.Context, repoID string) ([]domain.RepositoryFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items, ok := m.files[repoID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]domain.RepositoryFile, 0, len(items))
	for _, v := range items {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (m *Memory) PutChunks(_ context.Context, blobName string, chunks []domain.Chunk, postings []domain.ChunkPosting) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chunks[blobName]; ok {
		return nil
	}
	m.chunks[blobName] = append([]domain.Chunk(nil), chunks...)
	for _, c := range chunks {
		if c.ContentHash != "" {
			m.chunkHash[c.ID] = c.ContentHash
		}
	}
	for _, p := range postings {
		if m.postings[p.Term] == nil {
			m.postings[p.Term] = map[string]int{}
		}
		m.postings[p.Term][p.ChunkID] = p.TF
	}
	return nil
}
func (m *Memory) ChunkPostings(_ context.Context, terms []string, chunkIDs []string) (map[string]map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	allowed := make(map[string]bool, len(chunkIDs))
	for _, id := range chunkIDs {
		allowed[id] = true
	}
	out := map[string]map[string]int{}
	for _, term := range terms {
		for chunkID, tf := range m.postings[term] {
			if !allowed[chunkID] {
				continue
			}
			if out[term] == nil {
				out[term] = map[string]int{}
			}
			out[term][chunkID] = tf
		}
	}
	return out, nil
}
func (m *Memory) ChunksByBlobNames(_ context.Context, names []string) ([]domain.Chunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.Chunk{}
	for _, name := range names {
		out = append(out, m.chunks[name]...)
	}
	return out, nil
}
func (m *Memory) UpdateChunkSummaries(_ context.Context, summaries map[string]string) error {
	if len(summaries) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, chunks := range m.chunks {
		for i := range chunks {
			if summary, ok := summaries[chunks[i].ID]; ok {
				chunks[i].Summary = summary
			}
		}
		m.chunks[name] = chunks
	}
	return nil
}
func (m *Memory) PutEmbeddings(_ context.Context, embeddings []domain.ChunkEmbedding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range embeddings {
		if e.ContentHash != "" {
			if m.hashEmb[e.ModelID] == nil {
				m.hashEmb[e.ModelID] = map[string][]float32{}
			}
			m.hashEmb[e.ModelID][e.ContentHash] = append([]float32(nil), e.Vector...)
			continue
		}
		if m.embeddings[e.ModelID] == nil {
			m.embeddings[e.ModelID] = map[string][]float32{}
		}
		m.embeddings[e.ModelID][e.ChunkID] = append([]float32(nil), e.Vector...)
	}
	return nil
}

// vectorForChunk resolves a chunk's vector dual-track (callers hold m.mu).
func (m *Memory) vectorForChunk(chunkID, modelID string) ([]float32, bool) {
	if hash := m.chunkHash[chunkID]; hash != "" {
		v, ok := m.hashEmb[modelID][hash]
		return v, ok
	}
	v, ok := m.embeddings[modelID][chunkID]
	return v, ok
}
func (m *Memory) EmbeddingsByChunkIDs(_ context.Context, chunkIDs []string, modelID string) (map[string][]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string][]float32{}
	for _, id := range chunkIDs {
		if v, ok := m.vectorForChunk(id, modelID); ok {
			out[id] = v
		}
	}
	return out, nil
}
// VectorScores mirrors the in-database pgvector scoring with a plain Go inner
// product; the memory store has no separation between storage and compute.
func (m *Memory) VectorScores(_ context.Context, chunkIDs []string, modelID string, query []float32) (map[string]float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]float64{}
	for _, id := range chunkIDs {
		v, ok := m.vectorForChunk(id, modelID)
		if !ok || len(v) != len(query) {
			continue
		}
		var sum float64
		for i := range v {
			sum += float64(v[i]) * float64(query[i])
		}
		out[id] = sum
	}
	return out, nil
}
func (m *Memory) BlobPaths(_ context.Context, names []string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(names))
	for _, n := range names {
		if b, ok := m.blobs[n]; ok {
			out[n] = b.Path
		}
	}
	return out, nil
}
func (m *Memory) BlobChunkCounts(_ context.Context, names []string) (map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]int{}
	for _, n := range names {
		if chunks := m.chunks[n]; len(chunks) > 0 {
			out[n] = len(chunks)
		}
	}
	return out, nil
}
func (m *Memory) BlobSymbols(_ context.Context, names []string) (map[string][]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string][]string{}
	for _, n := range names {
		seen := map[string]bool{}
		for _, c := range m.chunks[n] {
			if c.Symbol != "" && !seen[c.Symbol] {
				seen[c.Symbol] = true
				out[n] = append(out[n], c.Symbol)
			}
		}
	}
	return out, nil
}
func (m *Memory) TopVectorBlobs(_ context.Context, names []string, modelID string, query []float32, k int) ([]string, error) {
	if len(names) == 0 || len(query) == 0 || k <= 0 {
		return []string{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	best := map[string]float64{}
	for _, n := range names {
		for _, c := range m.chunks[n] {
			if c.Seq >= 4 {
				continue
			}
			v, ok := m.vectorForChunk(c.ID, modelID)
			if !ok || len(v) != len(query) {
				continue
			}
			var sum float64
			for i := range v {
				sum += float64(v[i]) * float64(query[i])
			}
			if cur, seen := best[n]; !seen || sum > cur {
				best[n] = sum
			}
		}
	}
	return topKeysByScore(best, k), nil
}

// topKeysByScore returns the k keys with the highest scores, descending.
func topKeysByScore[V int | float64](scores map[string]V, k int) []string {
	keys := make([]string, 0, len(scores))
	for key := range scores {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return scores[keys[i]] > scores[keys[j]] })
	if len(keys) > k {
		keys = keys[:k]
	}
	return keys
}
func (m *Memory) BlobsMissingEmbeddings(_ context.Context, modelID string, limit int) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := []string{}
	for blobName, chunks := range m.chunks {
		for _, c := range chunks {
			if _, ok := m.vectorForChunk(c.ID, modelID); !ok {
				names = append(names, blobName)
				break
			}
		}
		if len(names) >= limit {
			break
		}
	}
	return names, nil
}
func (m *Memory) CreateEvalCase(_ context.Context, c domain.EvalCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evalCases[c.ID] = c
	return nil
}
func (m *Memory) ListEvalCases(_ context.Context, repoID string) ([]domain.EvalCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.EvalCase{}
	for _, c := range m.evalCases {
		if c.RepositoryID == repoID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (m *Memory) DeleteEvalCase(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.evalCases, id)
	return nil
}
func (m *Memory) EvalCaseByID(_ context.Context, id string) (domain.EvalCase, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.evalCases[id]
	if !ok {
		return domain.EvalCase{}, ErrNotFound
	}
	return c, nil
}
func (m *Memory) CreateSnapshot(_ context.Context, s domain.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.BlobNames = append([]string(nil), s.BlobNames...)
	m.snapshots[s.ID] = s
	return nil
}
func (m *Memory) SnapshotByID(_ context.Context, id string) (domain.Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.snapshots[id]
	if !ok {
		return domain.Snapshot{}, ErrNotFound
	}
	s.BlobNames = append([]string(nil), s.BlobNames...)
	return s, nil
}
func (m *Memory) LatestSnapshot(_ context.Context) (domain.Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest domain.Snapshot
	found := false
	for _, s := range m.snapshots {
		if !found || s.CreatedAt.After(latest.CreatedAt) {
			latest = s
			found = true
		}
	}
	if !found {
		return domain.Snapshot{}, ErrNotFound
	}
	latest.BlobNames = append([]string(nil), latest.BlobNames...)
	return latest, nil
}
func (m *Memory) SetUserCheckpoint(_ context.Context, userID, snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.userCkpts[userID] = snapshotID
	return nil
}
func (m *Memory) UserCheckpoint(_ context.Context, userID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id, ok := m.userCkpts[userID]; ok {
		return id, nil
	}
	return "", ErrNotFound
}
func (m *Memory) UserOwnsSnapshot(_ context.Context, userID, snapshotID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.userCkpts[userID] == snapshotID {
		return true, nil
	}
	for _, pr := range m.aceProjs[userID] {
		if pr.SnapshotID == snapshotID {
			return true, nil
		}
	}
	return false, nil
}
func (m *Memory) ACEProjectRefs(_ context.Context, userID string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	refs := map[string]string{}
	for name, pr := range m.aceProjs[userID] {
		refs[name] = pr.SnapshotID
	}
	return refs, nil
}
func (m *Memory) DeleteACEProject(_ context.Context, userID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.aceProjs[userID], name)
	return nil
}
func (m *Memory) PurgeACEProject(_ context.Context, userID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pr, ok := m.aceProjs[userID][name]
	if !ok {
		return ErrNotFound
	}
	delete(m.aceProjs[userID], name)
	for id, j := range m.jobs {
		if j.OwnerID == userID && j.Repository == name && j.RepositoryID == "" {
			delete(m.jobs, id)
		}
	}
	target := map[string]bool{}
	for _, n := range m.snapshots[pr.SnapshotID].BlobNames {
		target[n] = true
	}
	// Snapshots something else still needs: surviving project rows (ours is
	// already gone) and other users' checkpoint pointers.
	keep := map[string]bool{}
	for _, projects := range m.aceProjs {
		for _, p := range projects {
			keep[p.SnapshotID] = true
		}
	}
	for uid, snap := range m.userCkpts {
		if uid != userID {
			keep[snap] = true
		}
	}
	doomed := map[string]bool{}
	for id, s := range m.snapshots {
		if s.RepositoryID != "" || keep[id] {
			continue
		}
		if id == pr.SnapshotID {
			doomed[id] = true
			continue
		}
		shared := 0
		for _, n := range s.BlobNames {
			if target[n] {
				shared++
			}
		}
		smaller := min(len(s.BlobNames), len(target))
		if shared > 0 && smaller > 0 && float64(shared)/float64(smaller) >= 0.5 {
			doomed[id] = true
		}
	}
	candidates := map[string]bool{}
	for id := range doomed {
		for _, n := range m.snapshots[id].BlobNames {
			candidates[n] = true
		}
		delete(m.snapshots, id)
	}
	if doomed[m.userCkpts[userID]] {
		delete(m.userCkpts, userID)
	}
	for _, s := range m.snapshots {
		for _, n := range s.BlobNames {
			delete(candidates, n)
		}
	}
	for _, files := range m.files {
		for _, f := range files {
			delete(candidates, f.BlobName)
		}
	}
	for blobName := range candidates {
		for _, c := range m.chunks[blobName] {
			for _, byChunk := range m.postings {
				delete(byChunk, c.ID)
			}
			for _, byModel := range m.embeddings {
				delete(byModel, c.ID)
			}
			// Hash-keyed vectors stay: they are shared across identical
			// chunks and a later re-upload of the same content reuses them.
			delete(m.chunkHash, c.ID)
		}
		delete(m.chunks, blobName)
		delete(m.blobs, blobName)
	}
	return nil
}
func (m *Memory) SaveACEProject(_ context.Context, userID, name, snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aceProjs[userID] == nil {
		m.aceProjs[userID] = map[string]domain.ACEProject{}
	}
	m.aceProjs[userID][name] = domain.ACEProject{Name: name, SnapshotID: snapshotID, UpdatedAt: time.Now()}
	return nil
}
func (m *Memory) EnsureACEProject(_ context.Context, userID, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aceProjs[userID] == nil {
		m.aceProjs[userID] = map[string]domain.ACEProject{}
	}
	if _, ok := m.aceProjs[userID][name]; ok {
		return false, nil
	}
	m.aceProjs[userID][name] = domain.ACEProject{Name: name, UpdatedAt: time.Now()}
	return true, nil
}
func (m *Memory) ListACEProjects(_ context.Context, userID, modelID string) ([]domain.ACEProject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.ACEProject{}
	for _, pr := range m.aceProjs[userID] {
		out = append(out, m.aceProjectStats(pr, userID, modelID))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
func (m *Memory) ListAllACEProjects(_ context.Context, modelID string) ([]domain.ACEProject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.ACEProject{}
	for userID, projects := range m.aceProjs {
		for _, pr := range projects {
			out = append(out, m.aceProjectStats(pr, userID, modelID))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// aceProjectStats computes snapshot-derived stats; callers hold m.mu.
func (m *Memory) aceProjectStats(pr domain.ACEProject, userID, modelID string) domain.ACEProject {
	pr.OwnerID = userID
	pr.OwnerUsername = m.users[userID].Username
	s := m.snapshots[pr.SnapshotID]
	files := map[string]bool{}
	for _, name := range s.BlobNames {
		b, ok := m.blobs[name]
		if !ok {
			continue
		}
		path := b.Path
		// Large files arrive as path#chunkNofM pseudo-path blobs.
		if i := strings.IndexByte(path, '#'); i >= 0 {
			path = path[:i]
		}
		files[path] = true
		pr.StorageBytes += int64(len(b.Content))
		for _, c := range m.chunks[name] {
			pr.ChunkCount++
			if _, ok := m.vectorForChunk(c.ID, modelID); ok {
				pr.EmbeddedCount++
			}
		}
	}
	pr.FileCount = len(files)
	return pr
}
func (m *Memory) RecordACEUsage(_ context.Context, userID, endpoint string, units int64) error {
	day := time.Now().Truncate(24 * time.Hour)
	key := userID + "|" + day.Format("2006-01-02") + "|" + endpoint
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.aceUsage[key]
	if !ok {
		row = &domain.ACEUsage{UserID: userID, Endpoint: endpoint, LastDay: day}
		m.aceUsage[key] = row
	}
	row.Calls++
	row.Units += units
	return nil
}
func (m *Memory) ListACEUsage(_ context.Context, days int) ([]domain.ACEUsage, error) {
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	m.mu.RLock()
	defer m.mu.RUnlock()
	agg := map[string]*domain.ACEUsage{}
	for _, row := range m.aceUsage {
		if row.LastDay.Before(cutoff) {
			continue
		}
		key := row.UserID + "|" + row.Endpoint
		out, ok := agg[key]
		if !ok {
			out = &domain.ACEUsage{UserID: row.UserID, Endpoint: row.Endpoint}
			agg[key] = out
		}
		out.Calls += row.Calls
		out.Units += row.Units
		if row.LastDay.After(out.LastDay) {
			out.LastDay = row.LastDay
		}
	}
	result := make([]domain.ACEUsage, 0, len(agg))
	for _, row := range agg {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Calls > result[j].Calls })
	return result, nil
}
func (m *Memory) ACEUsageDaily(_ context.Context, userID string, days int) ([]domain.ACEUsageDay, error) {
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	m.mu.RLock()
	defer m.mu.RUnlock()
	agg := map[string]*domain.ACEUsageDay{}
	for _, row := range m.aceUsage {
		if row.LastDay.Before(cutoff) || (userID != "" && row.UserID != userID) {
			continue
		}
		key := row.LastDay.Format("2006-01-02") + "|" + row.Endpoint
		out, ok := agg[key]
		if !ok {
			out = &domain.ACEUsageDay{Day: row.LastDay, Endpoint: row.Endpoint}
			agg[key] = out
		}
		out.Calls += row.Calls
		out.Units += row.Units
	}
	result := make([]domain.ACEUsageDay, 0, len(agg))
	for _, row := range agg {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Day.Equal(result[j].Day) {
			return result[i].Day.Before(result[j].Day)
		}
		return result[i].Endpoint < result[j].Endpoint
	})
	return result, nil
}
func quotaDayPrefix(userID string) string {
	return userID + "|" + time.Now().Format("2006-01-02") + "|"
}
func (m *Memory) IncQuotaUsage(ctx context.Context, userID, kind string) error {
	return m.AddQuotaUsage(ctx, userID, kind, 1)
}
func (m *Memory) AddQuotaUsage(_ context.Context, userID, kind string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotaUsage[quotaDayPrefix(userID)+kind] += n
	return nil
}
func (m *Memory) ClearQuotaUsageKind(_ context.Context, userID, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.quotaUsage, quotaDayPrefix(userID)+kind)
	return nil
}
func (m *Memory) QuotaUsageToday(_ context.Context, userID string) (map[string]int64, error) {
	prefix := quotaDayPrefix(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]int64{}
	for key, used := range m.quotaUsage {
		if kind, ok := strings.CutPrefix(key, prefix); ok {
			out[kind] = used
		}
	}
	return out, nil
}
func (m *Memory) QuotaUsageTodayAll(_ context.Context) (map[string]map[string]int64, error) {
	day := time.Now().Format("2006-01-02")
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]map[string]int64{}
	for key, used := range m.quotaUsage {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 || parts[1] != day {
			continue
		}
		if out[parts[0]] == nil {
			out[parts[0]] = map[string]int64{}
		}
		out[parts[0]][parts[2]] = used
	}
	return out, nil
}
func (m *Memory) ResetQuotaUsage(_ context.Context, userID string) error {
	prefix := quotaDayPrefix(userID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.quotaUsage {
		if strings.HasPrefix(key, prefix) {
			delete(m.quotaUsage, key)
		}
	}
	return nil
}
func (m *Memory) CreateJob(_ context.Context, j domain.IndexJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
	return nil
}
func (m *Memory) UpdateJob(_ context.Context, j domain.IndexJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return ErrNotFound
	}
	m.jobs[j.ID] = j
	return nil
}
func (m *Memory) JobByID(_ context.Context, id string) (domain.IndexJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return domain.IndexJob{}, ErrNotFound
	}
	return j, nil
}
func (m *Memory) ListJobs(_ context.Context, ownerID string, all bool, limit int) ([]domain.IndexJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.IndexJob{}
	for _, v := range m.jobs {
		if all || v.OwnerID == ownerID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *Memory) RecordMetric(_ context.Context, p domain.MetricPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = append(m.metrics, p)
	if len(m.metrics) > 10000 {
		m.metrics = m.metrics[len(m.metrics)-10000:]
	}
	return nil
}
func (m *Memory) Metrics(_ context.Context, repoID, name string, since time.Time, limit int) ([]domain.MetricPoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []domain.MetricPoint{}
	for _, v := range m.metrics {
		if !v.Timestamp.Before(since) && (repoID == "" || v.RepositoryID == repoID) && (name == "" || v.Name == name) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
func (m *Memory) CreateAudit(_ context.Context, e domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, e)
	return nil
}
func (m *Memory) ListAudit(_ context.Context, limit int) ([]domain.AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]domain.AuditEvent(nil), m.audit...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *Memory) CreateAnnouncement(_ context.Context, a domain.Announcement) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.announces[a.ID] = a
	return nil
}
func (m *Memory) ListAnnouncements(_ context.Context, limit int) ([]domain.Announcement, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Announcement, 0, len(m.announces))
	for _, a := range m.announces {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *Memory) LatestAnnouncement(ctx context.Context) (domain.Announcement, error) {
	all, _ := m.ListAnnouncements(ctx, 1)
	if len(all) == 0 {
		return domain.Announcement{}, ErrNotFound
	}
	return all[0], nil
}
func (m *Memory) DeleteAnnouncement(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.announces[id]; !ok {
		return ErrNotFound
	}
	delete(m.announces, id)
	return nil
}
func (m *Memory) DismissAnnouncement(_ context.Context, userID, announcementID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.annDismiss[userID] = announcementID
	return nil
}
func (m *Memory) DismissedAnnouncement(_ context.Context, userID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.annDismiss[userID], nil
}
func (m *Memory) GetSettings(_ context.Context) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.settings))
	for k, v := range m.settings {
		out[k] = v
	}
	return out, nil
}
func (m *Memory) SetSettings(_ context.Context, values map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range values {
		m.settings[k] = v
	}
	return nil
}
