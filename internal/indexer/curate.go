package indexer

import (
	"crypto/sha256"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/linqiu919/better-context-engine/internal/domain"
)

// Context curation: the fused ranking is post-processed so the final context
// window reads well — one file cannot crowd out the rest, adjacent fragments
// of the same blob collapse into one continuous span, and tiny fragments are
// padded with surrounding lines so they carry enough context on their own.

const (
	perFileCap     = 3 // diversity: max fragments per file in the head selection
	mergeGapLines  = 2 // selected chunks this close (in lines) merge into one span
	expandMinLines = 6 // spans shorter than this get context padding
	expandPadLines = 3
)

// parsePseudoPath recognizes ACE-client pseudo paths ("file.vue#chunk2of3",
// emitted when an oversized file is uploaded as several line-sliced blobs)
// and returns the logical file plus the 1-based part position. Anything not
// matching the exact suffix shape is a real path.
func parsePseudoPath(path string) (base string, n, m int, ok bool) {
	i := strings.LastIndexByte(path, '#')
	if i < 0 || !strings.HasPrefix(path[i+1:], "chunk") {
		return "", 0, 0, false
	}
	rest := path[i+6:]
	j := strings.Index(rest, "of")
	if j <= 0 {
		return "", 0, 0, false
	}
	n, err1 := strconv.Atoi(rest[:j])
	m, err2 := strconv.Atoi(rest[j+2:])
	if err1 != nil || err2 != nil || n < 1 || m < 1 || n > m {
		return "", 0, 0, false
	}
	return path[:i], n, m, true
}

// logicalPath collapses a pseudo path to the file the user actually has on
// disk; every per-file policy (diversity cap, one-caller-per-file, fanout)
// counts logical files so a large split file is not treated as M files.
func logicalPath(path string) string {
	if base, _, _, ok := parsePseudoPath(path); ok {
		return base
	}
	return path
}

// pseudoSpan places one pseudo-blob inside its logical file.
type pseudoSpan struct {
	base   string // logical file path
	offset int    // line offset of this part within the logical file
}

// buildPseudoIndex reassembles line-sliced pseudo-blobs: per-blob line
// offsets (parts are split on line boundaries, so offsets are exact) and the
// rejoined full content keyed by logical path. A file is only remapped when
// all M parts are present and consistent; otherwise its parts keep per-blob
// paths and line numbers.
func buildPseudoIndex(files []domain.RepositoryFile) (map[string]pseudoSpan, map[string]string) {
	type filePart struct{ blob, content string }
	parts := map[string][]filePart{}
	consistent := map[string]bool{}
	for _, f := range files {
		base, n, m, ok := parsePseudoPath(f.Path)
		if !ok {
			continue
		}
		if _, seen := parts[base]; !seen {
			parts[base] = make([]filePart, m)
			consistent[base] = true
		}
		if len(parts[base]) != m || parts[base][n-1].blob != "" {
			consistent[base] = false
			continue
		}
		parts[base][n-1] = filePart{blob: f.BlobName, content: f.Content}
	}
	spans := map[string]pseudoSpan{}
	logical := map[string]string{}
	for base, ps := range parts {
		if !consistent[base] {
			continue
		}
		complete := true
		for _, p := range ps {
			if p.blob == "" {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		offset := 0
		contents := make([]string, len(ps))
		for i, p := range ps {
			spans[p.blob] = pseudoSpan{base: base, offset: offset}
			offset += lineCount(p.content)
			contents[i] = p.content
		}
		logical[base] = strings.Join(contents, "\n")
	}
	return spans, logical
}

// diversifyOrder walks the ranking and caps picks per file; indices skipped by
// the cap refill the tail when the scan runs out before reaching limit. Broad
// queries pass a wider limit with a tighter per-file cap: coverage across
// files over depth within one.
// Near-duplicate seating control, language/ecosystem agnostic. Multi-module
// repositories carry per-module copies of the same file differing by a line
// or two (logging configs, CI templates, vendored code); the semantic path
// scores every copy alike and the list fills with one file under varying
// paths. A same-basename candidate is capped only when its content largely
// overlaps chunks already seated under that name — same-name files with
// genuinely different content (index.ts, mod.rs, __init__.py) are untouched.
const (
	dupBasenameCap    = 2   // seats for near-identical copies sharing a basename
	dupLineJaccardMin = 0.7 // line-set overlap above this = a copied variant
)

func lineSet(content string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}

func lineJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, big := a, b
	if len(small) > len(big) {
		small, big = big, small
	}
	inter := 0
	for line := range small {
		if big[line] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func diversifyOrder(candidates []*candidate, order []int, limit, fileCap int) []int {
	picked := make([]int, 0, min(limit, len(order)))
	perFile := map[string]int{}
	seatedByBase := map[string][]map[string]bool{}
	seenContent := map[[32]byte]bool{}
	overflow := []int{}
	seat := func(idx int, sum [32]byte, base string, lines map[string]bool) {
		seenContent[sum] = true
		perFile[logicalPath(candidates[idx].path)]++
		seatedByBase[base] = append(seatedByBase[base], lines)
		picked = append(picked, idx)
	}
	// nearCopies counts already-seated same-basename chunks this content
	// largely overlaps; at dupBasenameCap the candidate defers to overflow.
	nearCopies := func(base string, lines map[string]bool) int {
		n := 0
		for _, seated := range seatedByBase[base] {
			if lineJaccard(lines, seated) >= dupLineJaccardMin {
				n++
			}
		}
		return n
	}
	for _, idx := range order {
		if len(picked) >= limit {
			return picked
		}
		// Byte-identical chunks at different paths (copy-pasted module
		// configs, vendored duplicates) never earn a second seat — and never
		// backfill either: repeating picked content is pure waste.
		sum := sha256.Sum256([]byte(candidates[idx].content))
		if seenContent[sum] {
			continue
		}
		path := logicalPath(candidates[idx].path)
		base := strings.ToLower(filepath.Base(path))
		lines := lineSet(candidates[idx].content)
		if perFile[path] >= fileCap || nearCopies(base, lines) >= dupBasenameCap {
			overflow = append(overflow, idx)
			continue
		}
		seat(idx, sum, base, lines)
	}
	// Backfill relaxes the per-file cap only. The near-copy cap still holds:
	// when the survivor pool is homogeneous (e.g. one pom.xml copied across
	// modules dominating a rerank cut), re-seating the capped copies would
	// undo the dedup above — fewer, distinct hits beat a full quota of
	// repeats.
	for _, idx := range overflow {
		if len(picked) >= limit {
			break
		}
		sum := sha256.Sum256([]byte(candidates[idx].content))
		if seenContent[sum] {
			continue
		}
		base := strings.ToLower(filepath.Base(logicalPath(candidates[idx].path)))
		lines := lineSet(candidates[idx].content)
		if nearCopies(base, lines) >= dupBasenameCap {
			continue
		}
		seat(idx, sum, base, lines)
	}
	return picked
}

// curateHits turns the selected candidate indices into final hits: adjacent
// spans of the same blob merge, small spans get padded, and content is
// re-sliced from blob lines so merged/padded ranges stay accurate. Chunks of
// a split oversized file (pseudo paths in the pseudo index) are remapped to
// logical-file coordinates first, so spans merge across part boundaries and
// hits report the real path with true line numbers — an agent can follow up
// with a plain file read.
func curateHits(candidates []*candidate, order []int, fused map[int]float64, contentByBlob map[string]string, pseudo map[string]pseudoSpan, logicalContent map[string]string) []SearchHit {
	type group struct {
		key        string // blob name, or logical path for remapped pseudo parts
		logical    bool
		start, end int // 1-based inclusive line span
		bestRank   int
		members    []int
	}
	// keyOf/startOf/endOf express every chunk in the coordinates of the unit
	// it merges within: its blob normally, its logical file when remapped.
	keyOf := func(idx int) (string, bool) {
		blob := candidates[idx].chunk.BlobName
		if p, ok := pseudo[blob]; ok {
			return p.base, true
		}
		return blob, false
	}
	startOf := func(idx int) int {
		c := candidates[idx]
		return c.chunk.StartLine + pseudo[c.chunk.BlobName].offset
	}
	endOf := func(idx int) int {
		c := candidates[idx]
		return c.chunk.EndLine + pseudo[c.chunk.BlobName].offset
	}
	rankOf := map[int]int{}
	byKey := map[string][]int{}
	logicalKey := map[string]bool{}
	for r, idx := range order {
		rankOf[idx] = r
		key, isLogical := keyOf(idx)
		byKey[key] = append(byKey[key], idx)
		logicalKey[key] = isLogical
	}
	groups := []*group{}
	for key, idxs := range byKey {
		sort.Slice(idxs, func(i, j int) bool {
			return startOf(idxs[i]) < startOf(idxs[j])
		})
		var cur *group
		for _, idx := range idxs {
			if cur != nil && startOf(idx) <= cur.end+1+mergeGapLines {
				cur.end = max(cur.end, endOf(idx))
				cur.bestRank = min(cur.bestRank, rankOf[idx])
				cur.members = append(cur.members, idx)
				continue
			}
			cur = &group{key: key, logical: logicalKey[key], start: startOf(idx), end: endOf(idx), bestRank: rankOf[idx], members: []int{idx}}
			groups = append(groups, cur)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].bestRank < groups[j].bestRank })

	linesByKey := map[string][]string{}
	hits := make([]SearchHit, 0, len(groups))
	for _, g := range groups {
		best := g.members[0]
		hit := SearchHit{}
		for _, idx := range g.members {
			c := candidates[idx]
			if rankOf[idx] < rankOf[best] {
				best = idx
			}
			hit.LexicalScore = max(hit.LexicalScore, c.lex)
			hit.PathScore = max(hit.PathScore, c.pathScore)
			hit.SymbolScore = max(hit.SymbolScore, c.symbol)
			hit.SemanticScore = max(hit.SemanticScore, c.semantic)
			hit.RerankScore = max(hit.RerankScore, c.rerank)
		}
		bc := candidates[best]
		hit.Path = bc.path
		if g.logical {
			hit.Path = g.key
		}
		hit.Symbol = bc.chunk.Symbol
		if hit.Symbol == "" {
			for _, idx := range g.members {
				if s := candidates[idx].chunk.Symbol; s != "" {
					hit.Symbol = s
					break
				}
			}
		}
		hit.Score = fused[best]
		lines, ok := linesByKey[g.key]
		if !ok {
			if content, has := contentByBlob[g.key]; has {
				lines = strings.Split(content, "\n")
			} else if content, has := logicalContent[g.key]; has {
				lines = strings.Split(content, "\n")
			}
			linesByKey[g.key] = lines
		}
		if len(lines) == 0 {
			// Content unavailable: fall back to the best member's own span
			// and text so line numbers stay consistent with content.
			hit.StartLine, hit.EndLine, hit.Content = startOf(best), endOf(best), bc.content
			hits = append(hits, hit)
			continue
		}
		hit.StartLine, hit.EndLine = g.start, g.end
		if hit.EndLine-hit.StartLine+1 < expandMinLines {
			hit.StartLine = max(1, hit.StartLine-expandPadLines)
			hit.EndLine = min(len(lines), hit.EndLine+expandPadLines)
		}
		start := hit.StartLine - 1
		end := min(hit.EndLine, len(lines))
		hit.Content = bc.content
		if start >= 0 && start < len(lines) {
			hit.Content = strings.Join(lines[start:end], "\n")
		}
		hits = append(hits, hit)
	}
	return hits
}
