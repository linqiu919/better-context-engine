package indexer

import (
	"context"
	"log/slog"
	"time"

	"github.com/linqiu919/better-context-engine/internal/store"
)

// Orphan GC: every client sync mints a new content-addressed checkpoint and
// only the newest one per user/project stays referenced, so superseded
// checkpoints (and blobs only they held) pile up — in production ~1,400
// orphan snapshots and ~73k blobs (~10 GB with postings) within a month.
// A nightly sweep removes them once they are past the grace window.

const (
	// orphanGrace keeps recently superseded checkpoints: a client may still
	// send an old checkpoint ID, and deleting it early would only force a
	// full re-upload (410) for no space gain worth having.
	orphanGrace = 14 * 24 * time.Hour
	// orphanGCHour is the Beijing-time hour the sweep runs — the quietest
	// traffic window, since the blob cascade is IO-heavy on the 2-core host.
	orphanGCHour = 4
	// orphanGCTimeout bounds one sweep; a backlog that does not fit resumes
	// the next night (every batch commits on its own).
	orphanGCTimeout = 3 * time.Hour
)

// StartOrphanGC schedules the nightly orphan sweep when the store supports
// it (Postgres); the in-memory store is a no-op.
func (s *Service) StartOrphanGC() {
	collector, ok := s.store.(store.OrphanCollector)
	if !ok {
		return
	}
	go func() {
		for {
			time.Sleep(time.Until(nextOrphanGC(time.Now())))
			s.collectOrphans(collector)
		}
	}()
}

func (s *Service) collectOrphans(collector store.OrphanCollector) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanGCTimeout)
	defer cancel()
	started := time.Now()
	snapshots, blobs, err := collector.CollectOrphans(ctx, orphanGrace)
	attrs := []any{"snapshots", snapshots, "blobs", blobs, "ms", time.Since(started).Milliseconds()}
	if err != nil {
		slog.Warn("orphan gc: sweep stopped early", append(attrs, "error", err)...)
		return
	}
	slog.Info("orphan gc", attrs...)
}

// nextOrphanGC is the next orphanGCHour:00 in Beijing time strictly after now.
func nextOrphanGC(now time.Time) time.Time {
	local := now.In(store.QuotaZone)
	next := time.Date(local.Year(), local.Month(), local.Day(), orphanGCHour, 0, 0, 0, store.QuotaZone)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
