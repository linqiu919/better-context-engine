package indexer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/store"
)

// sameProjectOverlap is the blob-set overlap coefficient above which two
// snapshots are considered uploads of the same workspace.
const sameProjectOverlap = 0.5

// maxReportedNameLen caps client-reported project names and branches;
// folder and ref names are OS-bounded anyway, this only guards against
// pathological payloads.
const maxReportedNameLen = 128

// WorkspaceMeta is what a bce-tool-rs client reports about the workspace a
// request comes from. Name is the project folder name (empty for stock ACE
// clients). Git is true when the client sent its git block: Branch is then
// the checked-out branch ("HEAD" when detached) and Worktree marks a linked
// `git worktree` checkout. Older bce-tool-rs builds send only Name.
type WorkspaceMeta struct {
	Name     string
	Branch   string
	Worktree bool
	Git      bool
}

func clipName(v string) string {
	v = strings.TrimSpace(v)
	if r := []rune(v); len(r) > maxReportedNameLen {
		v = string(r[:maxReportedNameLen])
	}
	return v
}

// isAutoName reports whether a project name was minted server-side from a
// checkpoint ID (client reported nothing).
func isAutoName(name string) bool { return strings.HasPrefix(name, "project-") }

// ArchiveACEProject records (user, name, branch) -> checkpoint so the console
// can list every project an ACE client has indexed. The reported name and
// branch win when present, so a client-side rename or branch switch shows on
// the very next search. Failures are swallowed because archival must never
// break retrieval. When the checkpoint actually changed (added/deleted
// blobs), an index-activity job is recorded so the console's activity pages
// reflect ACE uploads too.
//
// Rows that overlap this snapshot by most of their blobs are uploads of the
// same repository (closest match first). Which row this upload lands in:
//   - a git-aware client owns exactly (name, branch); other overlapping
//     rows are sibling branches / worktrees and keep their own rows, except
//     branch-less rows archived by older clients before branches were
//     tracked — those are replaced once, so "repo" becomes "repo (main)"
//     instead of lingering as a duplicate;
//   - a client without git facts (older bce-tool-rs) adopts the closest
//     git-aware row when one exists — its snapshot moves, nothing is
//     deleted — otherwise it lands in (name, "") as before;
//   - a client reporting no name at all (stock ACE) adopts the closest
//     named row, or the closest auto-named row, or mints "project-<hash>".
//
// Auto-named rows always fold into the row this upload lands in. A mixed
// fleet of client versions on one account can therefore never collapse the
// branch rows a git-aware client created; at worst an older client moves
// the snapshot pointer of the branch it most resembles.
func (s *Service) ArchiveACEProject(ctx context.Context, userID, snapshotID string, ws WorkspaceMeta, added, deleted int) {
	name, branch, worktree := clipName(ws.Name), "", ws.Git && ws.Worktree
	if ws.Git {
		branch = clipName(ws.Branch)
	}
	// Checkpoint IDs change with every edit, so a missing reported name must
	// not mint a fresh "project-<hash>" row per upload: adopt the row of the
	// same workspace (blob names are content-addressed and mostly stable
	// between uploads) before falling back to a minted name.
	matched := s.matchingACEProjects(ctx, userID, snapshotID)
	adopt := func(keep func(domain.ACEProjectRef) bool) bool {
		for _, prev := range matched {
			if keep(prev) {
				name, branch, worktree = prev.Name, prev.Branch, prev.Worktree
				return true
			}
		}
		return false
	}
	switch {
	case name == "":
		if !adopt(func(r domain.ACEProjectRef) bool { return !isAutoName(r.Name) }) && !adopt(func(domain.ACEProjectRef) bool { return true }) {
			short := strings.TrimPrefix(snapshotID, "ckpt_")
			if len(short) > 8 {
				short = short[:8]
			}
			name = "project-" + short
		}
	case !ws.Git:
		adopt(func(r domain.ACEProjectRef) bool { return r.Branch != "" })
	}
	for _, prev := range matched {
		if prev.Name == name && prev.Branch == branch {
			continue
		}
		if isAutoName(prev.Name) || (ws.Git && prev.Branch == "") {
			_ = s.store.DeleteACEProject(ctx, userID, prev.Name, prev.Branch)
			// The row's activity history follows it to the new label, so a
			// later purge of (name, branch) removes it too instead of
			// leaving orphaned entries under the bare name.
			_ = s.store.RenameACEJobs(ctx, userID, domain.ACEProjectLabel(prev.Name, prev.Branch), domain.ACEProjectLabel(name, branch))
		}
	}
	_ = s.store.SaveACEProject(ctx, userID, domain.ACEProjectRef{Name: name, Branch: branch, Worktree: worktree, SnapshotID: snapshotID})
	if added+deleted > 0 {
		now := time.Now()
		_ = s.store.CreateJob(ctx, domain.IndexJob{
			ID: identity.NewID("job"), Repository: domain.ACEProjectLabel(name, branch), OwnerID: userID,
			Status: domain.JobCompleted, Priority: "realtime", Stage: "index",
			FilesAdded: added, FilesDeleted: deleted,
			CreatedAt: now, StartedAt: now, CompletedAt: now,
		})
	}
}

// EnsurePendingACEProject makes a first-upload project visible in the console
// before its initial retrieval registers a real snapshot: a placeholder row
// (empty snapshot, zero stats) is inserted under the client-reported name and
// branch and later upgraded in place by ArchiveACEProject's same-key upsert.
// No-op when the client reports no name or the row already exists; failures
// are swallowed — visibility must never break uploads.
func (s *Service) EnsurePendingACEProject(ctx context.Context, userID string, ws WorkspaceMeta) {
	name := clipName(ws.Name)
	if name == "" || userID == "" {
		return
	}
	ref := domain.ACEProjectRef{Name: name, Worktree: ws.Git && ws.Worktree}
	if ws.Git {
		ref.Branch = clipName(ws.Branch)
	}
	created, err := s.store.EnsureACEProject(ctx, userID, ref)
	if err == nil && created {
		s.events.Publish("project.pending", map[string]any{"user_id": userID, "name": name, "branch": ref.Branch})
	}
}

// matchingACEProjects returns the user's archived projects whose snapshot
// blob set overlaps the given snapshot by at least sameProjectOverlap of the
// smaller set, closest match first (ties in name, branch order), so callers
// adopting a row pick the branch this upload most resembles. Errors degrade
// to "no match": archival then falls back to inserting, which is the
// pre-dedupe behavior.
func (s *Service) matchingACEProjects(ctx context.Context, userID, snapshotID string) []domain.ACEProjectRef {
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
	type match struct {
		ref   domain.ACEProjectRef
		score float64
	}
	matched := []match{}
	for _, ref := range refs {
		if ref.SnapshotID == snapshotID {
			matched = append(matched, match{ref, 2})
			continue
		}
		old, err := s.store.SnapshotByID(ctx, ref.SnapshotID)
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
		if smaller == 0 {
			continue
		}
		if score := float64(overlap) / float64(smaller); score >= sameProjectOverlap {
			matched = append(matched, match{ref, score})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		if matched[i].ref.Name != matched[j].ref.Name {
			return matched[i].ref.Name < matched[j].ref.Name
		}
		return matched[i].ref.Branch < matched[j].ref.Branch
	})
	out := make([]domain.ACEProjectRef, len(matched))
	for i, m := range matched {
		out[i] = m.ref
	}
	return out
}

// ACEProjectRef resolves one of the caller's project rows by (name, branch)
// without computing stats.
func (s *Service) ACEProjectRef(ctx context.Context, userID, name, branch string) (domain.ACEProjectRef, error) {
	refs, err := s.store.ACEProjectRefs(ctx, userID)
	if err != nil {
		return domain.ACEProjectRef{}, err
	}
	for _, ref := range refs {
		if ref.Name == name && ref.Branch == branch {
			return ref, nil
		}
	}
	return domain.ACEProjectRef{}, store.ErrNotFound
}

// PurgeACEProject physically deletes a user's archived project row (name,
// branch) and every piece of index data nothing else references (snapshots,
// blobs, chunks, postings, embeddings — see store.PurgeACEProject for the
// exact scope). Console-initiated; the client's cached checkpoint then
// resolves to 410 on the next search, which makes it re-upload the workspace
// in full.
func (s *Service) PurgeACEProject(ctx context.Context, userID, name, branch string) error {
	return s.store.PurgeACEProject(ctx, userID, name, branch)
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
