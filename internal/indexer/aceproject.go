package indexer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
)

// sameProjectOverlap is the blob-set overlap coefficient above which two
// snapshots are considered uploads of the same workspace.
const sameProjectOverlap = 0.5

// maxReportedNameLen caps client-reported project names; folder names are
// OS-bounded anyway, this only guards against pathological payloads.
const maxReportedNameLen = 128

// ArchiveACEProject records (user, project) -> checkpoint so the console can
// list every project an ACE client has indexed. reportedName is the project
// folder name the client sends with each retrieval request (empty for stock
// ACE clients); when present it wins, so a client-side rename propagates on
// the very next search. Failures are swallowed because archival must never
// break retrieval. When the checkpoint actually changed (added/deleted
// blobs), an index-activity job is recorded so the console's activity pages
// reflect ACE uploads too.
func (s *Service) ArchiveACEProject(ctx context.Context, userID, snapshotID, reportedName string, added, deleted int) {
	name := strings.TrimSpace(reportedName)
	if r := []rune(name); len(r) > maxReportedNameLen {
		name = string(r[:maxReportedNameLen])
	}

	// Checkpoint IDs change with every edit, so a missing reported name must
	// not mint a fresh "project-<hash>" row per upload. Blob names are
	// content-addressed and mostly stable between uploads of one workspace:
	// any archived project whose snapshot shares most blobs with this one IS
	// this project, and its row is reused (or renamed once the client starts
	// reporting a name).
	matched := s.matchingACEProjects(ctx, userID, snapshotID)
	if name == "" {
		for _, prev := range matched {
			if !strings.HasPrefix(prev, "project-") {
				name = prev
				break
			}
		}
	}
	if name == "" && len(matched) > 0 {
		name = matched[0]
	}
	if name == "" {
		short := strings.TrimPrefix(snapshotID, "ckpt_")
		if len(short) > 8 {
			short = short[:8]
		}
		name = "project-" + short
	}
	for _, prev := range matched {
		if prev != name {
			_ = s.store.DeleteACEProject(ctx, userID, prev)
		}
	}
	_ = s.store.SaveACEProject(ctx, userID, name, snapshotID)
	if added+deleted > 0 {
		now := time.Now()
		_ = s.store.CreateJob(ctx, domain.IndexJob{
			ID: identity.NewID("job"), Repository: name, OwnerID: userID,
			Status: domain.JobCompleted, Priority: "realtime", Stage: "index",
			FilesAdded: added, FilesDeleted: deleted,
			CreatedAt: now, StartedAt: now, CompletedAt: now,
		})
	}
}

// EnsurePendingACEProject makes a first-upload project visible in the console
// before its initial retrieval registers a real snapshot: a placeholder row
// (empty snapshot, zero stats) is inserted under the client-reported name and
// later upgraded in place by ArchiveACEProject's same-name upsert. No-op when
// the client reports no name or the row already exists; failures are
// swallowed — visibility must never break uploads.
func (s *Service) EnsurePendingACEProject(ctx context.Context, userID, reportedName string) {
	name := strings.TrimSpace(reportedName)
	if name == "" || userID == "" {
		return
	}
	if r := []rune(name); len(r) > maxReportedNameLen {
		name = string(r[:maxReportedNameLen])
	}
	created, err := s.store.EnsureACEProject(ctx, userID, name)
	if err == nil && created {
		s.events.Publish("project.pending", map[string]any{"user_id": userID, "name": name})
	}
}

// matchingACEProjects returns the names (deterministic order) of the user's
// archived projects whose snapshot blob set overlaps the given snapshot by at
// least sameProjectOverlap of the smaller set. Errors degrade to "no match":
// archival then falls back to inserting, which is the pre-dedupe behavior.
func (s *Service) matchingACEProjects(ctx context.Context, userID, snapshotID string) []string {
	refs, err := s.store.ACEProjectRefs(ctx, userID)
	if err != nil || len(refs) == 0 {
		return nil
	}
	snap, err := s.store.SnapshotByID(ctx, snapshotID)
	if err != nil {
		return nil
	}
	newSet := make(map[string]bool, len(snap.BlobNames))
	for _, n := range snap.BlobNames {
		newSet[n] = true
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	matched := []string{}
	for _, name := range names {
		if refs[name] == snapshotID {
			matched = append(matched, name)
			continue
		}
		old, err := s.store.SnapshotByID(ctx, refs[name])
		if err != nil {
			continue
		}
		overlap := 0
		for _, n := range old.BlobNames {
			if newSet[n] {
				overlap++
			}
		}
		smaller := len(old.BlobNames)
		if len(newSet) < smaller {
			smaller = len(newSet)
		}
		if smaller > 0 && float64(overlap)/float64(smaller) >= sameProjectOverlap {
			matched = append(matched, name)
		}
	}
	return matched
}

// PurgeACEProject physically deletes a user's archived project and every
// piece of index data nothing else references (snapshots, blobs, chunks,
// postings, embeddings — see store.PurgeACEProject for the exact scope).
// Console-initiated; the client's cached checkpoint then resolves to 410 on
// the next search, which makes it re-upload the workspace in full.
func (s *Service) PurgeACEProject(ctx context.Context, userID, name string) error {
	return s.store.PurgeACEProject(ctx, userID, name)
}

// ListACEProjects returns the caller's archived ACE projects with embedding
// coverage computed against the currently configured model.
func (s *Service) ListACEProjects(ctx context.Context, userID string) ([]domain.ACEProject, error) {
	return s.store.ListACEProjects(ctx, userID, s.embeddingConfig(ctx).Model)
}

// ListAllACEProjects is the admin view across every user.
func (s *Service) ListAllACEProjects(ctx context.Context) ([]domain.ACEProject, error) {
	return s.store.ListAllACEProjects(ctx, s.embeddingConfig(ctx).Model)
}
