package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/store"
)

// Idle-project auto-purge: a user opts in from the account page and picks a
// window; projects whose last retrieval is older than that are purged in the
// background exactly like a console delete. user_ace_projects.updated_at is
// the activity signal — every retrieval re-archives the row, so a project
// that is only searched (never edited) still counts as active. A purged
// project costs nothing permanent: the client's next search gets 410 and
// re-uploads, and hash-keyed vectors survive the purge, so re-indexing the
// same content does not pay the embedding API again.

// autoPurgeChoices are the windows the console offers; 0 = off.
var autoPurgeChoices = []int{0, 14, 30, 60, 90}

const (
	autoPurgeInterval = time.Hour
	// autoPurgeWarmup delays the first sweep so a restart does not stack the
	// purge on top of startup work (embedding backfill, cache warmup).
	autoPurgeWarmup = 5 * time.Minute
)

// idleProjects returns the user's projects with no retrieval for days.
func (s *Server) idleProjects(ctx context.Context, userID string, days int) ([]domain.ACEProject, error) {
	projects, err := s.indexer.ListACEProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	idle := []domain.ACEProject{}
	for _, p := range projects {
		if p.UpdatedAt.Before(cutoff) {
			idle = append(idle, p)
		}
	}
	return idle, nil
}

// autoPurgePreview lists what a given window would purge right now, so the
// account page can show the impact before the user saves. storage_bytes is
// an upper bound: sibling rows of one repository share blobs.
func (s *Server) autoPurgePreview(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days <= 0 || !slices.Contains(autoPurgeChoices, days) {
		writeError(w, 400, "days must be one of 14, 30, 60, 90")
		return
	}
	idle, err := s.idleProjects(r.Context(), currentUser(r).ID, days)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var total int64
	for _, p := range idle {
		total += p.StorageBytes
	}
	writeJSON(w, 200, map[string]any{"projects": idle, "storage_bytes": total})
}

func (s *Server) autoPurgeLoop() {
	time.Sleep(autoPurgeWarmup)
	ticker := time.NewTicker(autoPurgeInterval)
	defer ticker.Stop()
	for {
		s.autoPurgeSweep(context.Background())
		<-ticker.C
	}
}

// autoPurgeSweep purges every opted-in user's idle projects. Unlike a console
// delete it refunds no upload_bytes: background cleanup must never widen a
// user's daily ingest allowance.
func (s *Server) autoPurgeSweep(ctx context.Context) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		s.logger.Warn("auto purge: list users", "error", err)
		return
	}
	for _, user := range users {
		if user.AutoPurgeDays <= 0 {
			continue
		}
		idle, err := s.idleProjects(ctx, user.ID, user.AutoPurgeDays)
		if err != nil {
			s.logger.Warn("auto purge: list projects", "user", user.Username, "error", err)
			continue
		}
		purged := 0
		for _, p := range idle {
			// Another instance (or a console delete) may have removed the
			// row since the listing; that is the outcome we wanted anyway.
			if err := s.indexer.PurgeACEProject(ctx, user.ID, p.Name, p.Branch); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					s.logger.Warn("auto purge: purge project", "user", user.Username, "project", domain.ACEProjectLabel(p.Name, p.Branch), "error", err)
				}
				continue
			}
			purged++
			s.audit(ctx, user, "ace_project.auto_purge", "ace_project", domain.ACEProjectLabel(p.Name, p.Branch), "success", map[string]any{"idle_days": user.AutoPurgeDays, "last_active_at": p.UpdatedAt})
		}
		if purged > 0 {
			s.dropStorageCache(ctx, user.ID)
			s.logger.Info("auto purge", "user", user.Username, "projects", purged, "idle_days", user.AutoPurgeDays)
		}
	}
}
