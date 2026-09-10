package indexer

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Large-workspace candidate prefilter.
//
// The exhaustive pipeline loads every blob's content (with decryption) and
// every chunk row before ranking — cost linear in workspace size, which put a
// 12k-file / 69k-chunk project at 8-12s per search on production hardware
// while everything else on the box sat idle. Above the threshold below, a
// cheap selection pass first decides which files are worth ranking, and only
// that subset flows into the unchanged pipeline.
//
// Selection signals, mirroring the pipeline's own ranking paths so a file the
// full pipeline would have surfaced is very likely kept by the same signal:
//   - semantic: pgvector best-head-chunk similarity, in-database (one query
//     vector in, file names out), under its own wall-clock cap — a cold
//     vector cache turned this scan into 6s on a 22k-file snapshot, and a
//     prefilter stage must never cost what it saves;
//   - identifier: query tokens against chunk symbols (a narrow indexed read
//     scored in Go — the inverted index can't serve this stage, see the
//     BlobSymbols interface note);
//   - structural: query tokens against paths, plus manifest files so the
//     broad/structural stages keep their prior targets.
//
// Nominations are spent against a chunk budget, not a file count: documentation
// warehouses average 5-20 chunks per file, so 800 nominated files can smuggle
// 20k+ chunks into the pipeline and reproduce the exhaustive-path latency.
// Tiers are drained round-robin (semantic and identifier lists are
// relevance-ordered) until the budget runs out; the subset the pipeline then
// ranks is bounded in the unit that actually drives its cost.
//
// When selection would rest on manifests alone (semantic signal timed out or
// unavailable AND nothing matches a path or symbol — e.g. a Chinese query on
// a cold cache), the prefilter declines and the caller keeps the full set:
// slow-but-right beats fast-but-blind.
const (
	// prefilterFileThreshold triggers the prefilter unconditionally: at this
	// many files the workspace is heavy no matter how thin its files are.
	prefilterFileThreshold = 4000
	// prefilterChunkThreshold triggers it below the file threshold when the
	// stored chunk total says the workspace is heavy anyway. File count is a
	// proxy; chunks are the pipeline's real cost unit, and chunk-dense
	// mid-size projects invert the intent of a file-count gate: a 2.7k-file
	// Java workspace carrying 12-14k chunks ran 3-6s exhaustively while a
	// 24k-file project answered in ~2s through the prefilter. The margin
	// over the budget is deliberately thin (1.25x): production's chunk-heavy
	// mid-size projects cluster at 11-14k chunks, and a first cut at 1.5x
	// split that cluster down the middle — an 11.2k-chunk project ran 9s
	// exhaustively (concurrent with its own just-uploaded ingest) while its
	// 12.3k-chunk sibling qualified for selection. Selection also trims blob
	// loading and decryption, which is why it pays even when the chunk
	// reduction alone looks modest.
	prefilterChunkThreshold = prefilterChunkBudget * 5 / 4
	// prefilterProbeFloor bounds the cost of asking: workspaces at or below
	// this many files skip the chunk-count probe entirely — even at a
	// documentation warehouse's chunk density they stay well under the
	// threshold, so the probe could only ever say no.
	prefilterProbeFloor  = 500
	prefilterVectorFiles = 800
	prefilterSymbolFiles   = 400
	prefilterPathFiles     = 400
	prefilterManifestFiles = 200
	// prefilterChunkBudget caps the subset's total stored chunks. ~8k chunks
	// keeps the pipeline's unbounded DB stages (postings probes, in-database
	// vector scoring, content splitting) around a second on production
	// hardware while still covering hundreds of files.
	prefilterChunkBudget = 8000
	// prefilterVectorTTL bounds the in-database semantic scan. Warm it runs
	// in well under a second per 10k files; cold it has been measured at 6s,
	// which the other signals must not wait for.
	prefilterVectorTTL = 2500 * time.Millisecond
)

// shouldPrefilter decides whether the workspace is heavy enough to be worth
// selecting from, and returns the chunk counts it probed so the prefilter
// does not fetch them twice. Above the file threshold the answer is yes
// without a probe (counts nil — the prefilter fetches its own); at or below
// the probe floor the answer is no without a probe; in between, one indexed
// GROUP BY over the workspace's chunks settles it.
func (s *Service) shouldPrefilter(ctx context.Context, names []string) (map[string]int, bool) {
	if len(names) > prefilterFileThreshold {
		return nil, true
	}
	if len(names) <= prefilterProbeFloor {
		return nil, false
	}
	counts, err := s.store.BlobChunkCounts(ctx, names)
	if err != nil {
		return nil, false
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	return counts, total > prefilterChunkThreshold
}

// prefilterBlobNames returns the selected subset (nil to keep the full set),
// the count of names that actually exist in the store (the caller's
// ghost-blob accounting can no longer compare against loaded blobs, because
// not loading most blobs is the whole point), and the query-embed channel it
// started — the embedding is needed here for the semantic signal and is
// handed onward so the pipeline doesn't pay for a second embed call.
// counts may carry the chunk counts shouldPrefilter already probed; nil means
// fetch them here.
func (s *Service) prefilterBlobNames(ctx context.Context, query string, names []string, counts map[string]int) (subset []string, found int, embedCh <-chan queryEmbedResult) {
	started := time.Now()
	paths, err := s.store.BlobPaths(ctx, names)
	if err != nil {
		return nil, 0, nil
	}
	found = len(paths)
	embedCh = s.startQueryEmbed(ctx, query)

	// Path and manifest tiers score while the query embed is in flight.
	ptoks := []string{}
	for _, t := range tokens(query) {
		if len([]rune(t)) >= 2 {
			ptoks = append(ptoks, t)
		}
	}
	pathScored := make([]fileScore, 0, 256)
	manifestTier := []string{}
	for n, p := range paths {
		lp := strings.ToLower(logicalPath(p))
		hits := 0
		for _, t := range ptoks {
			if strings.Contains(lp, t) {
				hits++
			}
		}
		if hits > 0 {
			pathScored = append(pathScored, fileScore{n, hits})
		}
		if len(manifestTier) < prefilterManifestFiles && manifestBoost(logicalPath(p)) > 0 {
			manifestTier = append(manifestTier, n)
		}
	}
	pathTier := topNames(pathScored, prefilterPathFiles)

	// Identifier tier: code-aware query tokens against declared symbols.
	// This is what keeps "where is renderAnnouncement handled" selecting the
	// right file even when neither its path nor its head chunks match.
	symbolTier := []string{}
	if symTokens := codeTokens(query); len(symTokens) > 0 {
		if symbols, err := s.store.BlobSymbols(ctx, names); err == nil {
			symScored := make([]fileScore, 0, 256)
			for n, syms := range symbols {
				hits := 0
				for _, sym := range syms {
					ls := strings.ToLower(sym)
					for _, t := range symTokens {
						if strings.Contains(ls, t) {
							hits++
						}
					}
				}
				if hits > 0 {
					symScored = append(symScored, fileScore{n, hits})
				}
			}
			symbolTier = topNames(symScored, prefilterSymbolFiles)
		}
	}

	// Semantic tier: the query vector earns the same bounded wait as in the
	// pipeline (most of it has usually elapsed during the scoring above). A
	// received result is re-buffered so the pipeline's select still gets its
	// value; on timeout the original channel passes through and the
	// pipeline's own wait picks up whatever arrives later.
	var pre queryEmbedResult
	received := false
	select {
	case pre = <-embedCh:
		received = true
	case <-time.After(queryEmbedWait):
	}
	if received {
		re := make(chan queryEmbedResult, 1)
		re <- pre
		embedCh = re
	}
	vectorTier := []string{}
	if received && pre.ok {
		vctx, cancel := context.WithTimeout(ctx, prefilterVectorTTL)
		top, err := s.store.TopVectorBlobs(vctx, names, pre.cfg.Model, pre.vec, prefilterVectorFiles)
		if err == nil {
			vectorTier = top
		} else {
			// Never swallow this: the semantic tier failing looks identical
			// to it finding nothing ("vector":0 in the summary line), and a
			// database-side error hid behind that for two days in production
			// (parallel hash join exhausting the container's 64MB /dev/shm)
			// while every prefiltered search silently ran without semantics.
			slog.Warn("search prefilter: vector signal failed", "err", err,
				"files", len(names), "elapsed_ms", time.Since(started).Milliseconds())
		}
		cancel()
	}

	// Content-grounded signals only: when both the semantic and the
	// identifier/path tiers are empty, manifests alone must not decide what
	// the pipeline sees — decline and let the exhaustive path run. The
	// decline is logged: a declined prefilter is exactly a slow search, and
	// without this line those searches are indistinguishable from ones the
	// gate never selected (which is how a 4s exhaustive run on a chunk-heavy
	// workspace went unexplained in production).
	if len(vectorTier) == 0 && len(symbolTier) == 0 && len(pathTier) == 0 {
		slog.Info("search prefilter declined: no grounded signal",
			"files", len(names), "embed_received", received, "embed_ok", received && pre.ok,
			"duration_ms", time.Since(started).Milliseconds())
		return nil, found, embedCh
	}

	// Round-robin drain against the chunk budget. The tiers are
	// relevance-ordered, so interleaving keeps each signal's best files even
	// when chunk-heavy documentation would exhaust the budget in one tier.
	if counts == nil {
		var err error
		if counts, err = s.store.BlobChunkCounts(ctx, names); err != nil {
			counts = map[string]int{}
		}
	}
	keep := map[string]bool{}
	kept, chunks := 0, 0
	tiers := [][]string{vectorTier, symbolTier, pathTier, manifestTier}
	idx := make([]int, len(tiers))
	for chunks < prefilterChunkBudget {
		progressed := false
		for t := range tiers {
			for idx[t] < len(tiers[t]) {
				n := tiers[t][idx[t]]
				idx[t]++
				if keep[n] {
					continue
				}
				c := counts[n]
				if c == 0 {
					c = 1 // legacy blob without stored chunks: chunked on the fly
				}
				if chunks+c > prefilterChunkBudget {
					continue // too fat for what's left; smaller files may still fit
				}
				keep[n] = true
				kept++
				chunks += c
				progressed = true
				break
			}
		}
		if !progressed {
			break
		}
	}
	if kept == 0 {
		slog.Info("search prefilter declined: nothing fit the chunk budget",
			"files", len(names), "duration_ms", time.Since(started).Milliseconds())
		return nil, found, embedCh
	}
	subset = make([]string, 0, kept)
	for _, n := range names {
		if keep[n] {
			subset = append(subset, n)
		}
	}
	slog.Info("search prefilter",
		"files", len(names), "found", found, "kept", kept, "chunks", chunks,
		"vector", len(vectorTier), "symbol", len(symbolTier), "path", len(pathTier), "manifest", len(manifestTier),
		"duration_ms", time.Since(started).Milliseconds())
	return subset, found, embedCh
}

type fileScore struct {
	name string
	hits int
}

// topNames returns the names of the k highest-scoring files, best first.
func topNames(scored []fileScore, k int) []string {
	sort.Slice(scored, func(i, j int) bool { return scored[i].hits > scored[j].hits })
	if len(scored) > k {
		scored = scored[:k]
	}
	out := make([]string, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.name)
	}
	return out
}
