package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Admin analytics: six hourly series over the last 24 hours, all derived from
// the metrics table. The whole response is assembled once and memoized for
// analyticsCacheTTL — the page is a dashboard, not a live feed, and the six
// underlying queries (index-assisted, but still range scans) should not run
// on every admin tab focus.
const analyticsCacheTTL = 60 * time.Second

const analyticsWindow = 24 * time.Hour

// analyticsHour is one hourly bucket. Fields cover the union of what the six
// sections need; sections ignore fields they don't chart. Rates are 0..1.
type analyticsHour struct {
	Hour time.Time `json:"hour"`
	// Latency (retrieval-stage net of inline embedding, milliseconds).
	Searches  int     `json:"searches"`
	AvgMS     float64 `json:"avg_ms,omitempty"`
	P50MS     float64 `json:"p50_ms,omitempty"`
	P95MS     float64 `json:"p95_ms,omitempty"`
	// Degraded rate.
	Degraded     int     `json:"degraded,omitempty"`
	DegradedRate float64 `json:"degraded_rate"`
	// Requests (pipeline searches + cache hits) for the QPS section.
	Requests int `json:"requests"`
	// Ingest volume.
	IngestBytes float64 `json:"ingest_bytes"`
	IngestBlobs float64 `json:"ingest_blobs"`
	// Cache hit rate.
	CacheHits   int     `json:"cache_hits"`
	CacheMisses int     `json:"cache_misses"`
	CacheRate   float64 `json:"cache_rate"`
	// Inline embedding debt (first queries).
	InlineCount int     `json:"inline_count,omitempty"`
	InlineAvgMS float64 `json:"inline_avg_ms,omitempty"`
	InlineMaxMS float64 `json:"inline_max_ms,omitempty"`
}

type analyticsResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Hours       []analyticsHour `json:"hours"`
	// Window totals for the section headers.
	TotalSearches int     `json:"total_searches"`
	TotalDegraded int     `json:"total_degraded"`
	TotalRequests int     `json:"total_requests"`
	TotalIngestMB float64 `json:"total_ingest_mb"`
	CacheHitRate  float64 `json:"cache_hit_rate"`
}

func (s *Server) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	s.analyticsMu.Lock()
	if s.analyticsCache != nil && time.Since(s.analyticsAt) < analyticsCacheTTL {
		cached := s.analyticsCache
		s.analyticsMu.Unlock()
		writeJSON(w, 200, cached)
		return
	}
	s.analyticsMu.Unlock()

	ctx := r.Context()
	since := time.Now().Add(-analyticsWindow)
	fetch := func(name string) []domain.MetricPoint {
		points, _ := s.store.Metrics(ctx, "", name, since, 100000)
		return points
	}
	// Buckets are aligned to the hour and pre-created for the whole window so
	// idle hours chart as zero instead of leaving axis gaps.
	start := time.Now().Truncate(time.Hour).Add(-(analyticsWindow - time.Hour))
	index := map[int64]*analyticsHour{}
	hours := make([]*analyticsHour, 0, 25)
	for h := start; !h.After(time.Now()); h = h.Add(time.Hour) {
		bucket := &analyticsHour{Hour: h}
		index[h.Unix()] = bucket
		hours = append(hours, bucket)
	}
	at := func(ts time.Time) *analyticsHour { return index[ts.Truncate(time.Hour).Unix()] }

	latencySamples := map[int64][]float64{}
	for _, p := range fetch("search_latency_ms") {
		if b := at(p.Timestamp); b != nil {
			b.Searches++
			latencySamples[b.Hour.Unix()] = append(latencySamples[b.Hour.Unix()], p.Value)
		}
	}
	for key, samples := range latencySamples {
		b := index[key]
		sort.Float64s(samples)
		var sum float64
		for _, v := range samples {
			sum += v
		}
		b.AvgMS = round1(sum / float64(len(samples)))
		b.P50MS = round1(samples[int(float64(len(samples)-1)*.5)])
		b.P95MS = round1(samples[int(float64(len(samples)-1)*.95)])
	}
	for _, p := range fetch("search_degraded") {
		if b := at(p.Timestamp); b != nil && p.Value > 0 {
			b.Degraded++
		}
	}
	for _, p := range fetch("search_cache_hit") {
		if b := at(p.Timestamp); b != nil {
			b.Requests++
			if p.Value > 0 {
				b.CacheHits++
			} else {
				b.CacheMisses++
			}
		}
	}
	for _, p := range fetch("ingest_bytes") {
		if b := at(p.Timestamp); b != nil {
			b.IngestBytes += p.Value
		}
	}
	for _, p := range fetch("ingest_blobs") {
		if b := at(p.Timestamp); b != nil {
			b.IngestBlobs += p.Value
		}
	}
	for _, p := range fetch("inline_embed_ms") {
		if b := at(p.Timestamp); b != nil {
			b.InlineCount++
			b.InlineAvgMS += p.Value // sum for now, divided below
			if p.Value > b.InlineMaxMS {
				b.InlineMaxMS = round1(p.Value)
			}
		}
	}

	out := analyticsResponse{GeneratedAt: time.Now(), Hours: make([]analyticsHour, 0, len(hours))}
	var hits, misses int
	for _, b := range hours {
		if b.Searches > 0 {
			b.DegradedRate = float64(b.Degraded) / float64(b.Searches)
		}
		if n := b.CacheHits + b.CacheMisses; n > 0 {
			b.CacheRate = float64(b.CacheHits) / float64(n)
		}
		if b.InlineCount > 0 {
			b.InlineAvgMS = round1(b.InlineAvgMS / float64(b.InlineCount))
		}
		out.TotalSearches += b.Searches
		out.TotalDegraded += b.Degraded
		out.TotalRequests += b.Requests
		out.TotalIngestMB += b.IngestBytes
		hits += b.CacheHits
		misses += b.CacheMisses
		out.Hours = append(out.Hours, *b)
	}
	out.TotalIngestMB = round1(out.TotalIngestMB / (1024 * 1024))
	if hits+misses > 0 {
		out.CacheHitRate = float64(hits) / float64(hits+misses)
	}

	s.analyticsMu.Lock()
	s.analyticsCache = &out
	s.analyticsAt = time.Now()
	s.analyticsMu.Unlock()
	writeJSON(w, 200, out)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
