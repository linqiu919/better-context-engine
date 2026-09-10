package indexer

import (
	"sort"
	"strings"
)

// Post-curation relation pass: retrieval ranks chunks by query relevance, so
// definitions the results depend on (a struct referenced in every signature)
// and sibling symbols on the same flow score poorly and get dropped. This
// pass closes the gap structurally instead of semantically — identifiers in
// the selected chunks are matched against symbol definitions in the visible
// candidate set.

const (
	maxPulledDefs     = 4 // referenced type definitions (A)
	maxPulledFuncDefs = 3 // referenced behavioral definitions, callee direction (B)
	maxPulledCallers  = 3 // chunks referencing top symbols, caller direction (B)
	maxPulledImpls    = 3 // implementations of referenced interfaces (C)
	maxRelatedHints   = 8
	minIdentLen       = 3
	// Both expansion directions only follow selective symbols: names this
	// short, or referenced in more logical files than the fanout cap, are
	// infrastructure-grade generic (a shared HTTP wrapper, a base response
	// type) — pulling their definition into every search is repetitive noise,
	// so they are left to the related_symbols hints instead.
	minSeedSymLen   = 4
	callerSeedLimit = 6
	callerFanoutMax = 15
)

// typeDefKinds are pulled into the result set when referenced (A); a type
// definition is small and almost always wanted when its name appears in a
// selected signature. behaviorDefKinds are pulled with a tighter cap (B,
// callee direction) — e.g. the HTTP wrapper a picked component calls.
var typeDefKinds = map[string]bool{"type": true, "class": true, "interface": true}

var behaviorDefKinds = map[string]bool{"func": true, "method": true, "var": true, "impl": true}

var hintKinds = map[string]bool{"type": true, "class": true, "interface": true, "func": true, "method": true, "impl": true}

// identTokens extracts identifier-shaped tokens from source text. Matching is
// exact against known symbol names, so language keywords are harmless; the
// length floor keeps single letters and short noise out.
func identTokens(content string) map[string]bool {
	out := map[string]bool{}
	start := -1
	for i, r := range content {
		isIdent := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if isIdent {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if tok := content[start:i]; len(tok) >= minIdentLen && !(tok[0] >= '0' && tok[0] <= '9') {
				out[tok] = true
			}
			start = -1
		}
	}
	if start >= 0 {
		if tok := content[start:]; len(tok) >= minIdentLen && !(tok[0] >= '0' && tok[0] <= '9') {
			out[tok] = true
		}
	}
	return out
}

// expandRelations is the one-hop structural expansion (optimizations A + B):
// after curation picked the head by query relevance, this pass appends chunks
// the head is *connected to* regardless of their own relevance score —
//
//  1. type definitions the picks reference (struct/class named in signatures),
//  2. behavioral definitions the picks call (the HTTP wrapper behind
//     emailAPI.getLatestMail), and
//  3. cross-file callers of the picks' own symbols (the route handler that
//     dispatches into a picked service method).
//
// Appending after the picks keeps expansions at the tail of the context
// window where the budget loop can still drop them.
func expandRelations(candidates []*candidate, order []int, fused map[int]float64) []int {
	if len(order) == 0 {
		return order
	}
	picked := make(map[int]bool, len(order))
	pickedSyms := map[string]bool{}
	pickedPaths := map[string]bool{}
	for _, idx := range order {
		picked[idx] = true
		pickedPaths[logicalPath(candidates[idx].path)] = true
		if s := candidates[idx].chunk.Symbol; s != "" {
			pickedSyms[s] = true
		}
	}
	// fileFanout counts the distinct logical files that reference a symbol —
	// the genericity signal both directions gate on. Memoized because it
	// scans the whole candidate corpus per symbol.
	fanoutCache := map[string]int{}
	fileFanout := func(sym string) int {
		if v, ok := fanoutCache[sym]; ok {
			return v
		}
		files := map[string]bool{}
		for _, c := range candidates {
			if containsIdent(c.content, sym) {
				files[logicalPath(c.path)] = true
			}
		}
		fanoutCache[sym] = len(files)
		return len(files)
	}
	// Tokens referenced anywhere in the picked head, counted once per chunk.
	refTokens := map[string]int{}
	for _, idx := range order {
		for tok := range identTokens(candidates[idx].content) {
			refTokens[tok]++
		}
	}
	// Definition index over the non-picked corpus. Smallest chunk wins per
	// symbol: pulling a 400-line class body for a name reference defeats the
	// budget.
	typeDefs := map[string]int{}
	funcDefs := map[string]int{}
	for i, c := range candidates {
		sym := c.chunk.Symbol
		if picked[i] || sym == "" || pickedSyms[sym] {
			continue
		}
		var defs map[string]int
		switch {
		case typeDefKinds[c.chunk.SymbolKind]:
			defs = typeDefs
		case behaviorDefKinds[c.chunk.SymbolKind] && len(sym) >= minSeedSymLen:
			defs = funcDefs
		default:
			continue
		}
		if prev, ok := defs[sym]; !ok || spanLines(candidates[prev]) > spanLines(c) {
			defs[sym] = i
		}
	}
	appendReferenced := func(defs map[string]int, limit int) {
		syms := make([]string, 0, len(defs))
		for sym := range defs {
			if refTokens[sym] > 0 {
				syms = append(syms, sym)
			}
		}
		sort.Slice(syms, func(i, j int) bool {
			if refTokens[syms[i]] == refTokens[syms[j]] {
				return syms[i] < syms[j]
			}
			return refTokens[syms[i]] > refTokens[syms[j]]
		})
		added := 0
		for _, sym := range syms {
			if added >= limit {
				break
			}
			idx := defs[sym]
			// Fanout gate (callee direction): a definition referenced across
			// half the codebase is a utility every search would re-pull; the
			// slot backfills with the next, more specific symbol.
			if picked[idx] || fileFanout(sym) > callerFanoutMax {
				continue
			}
			picked[idx] = true
			order = append(order, idx)
			added++
		}
	}
	appendReferenced(typeDefs, maxPulledDefs)
	appendReferenced(funcDefs, maxPulledFuncDefs)

	// Interface→implementation bridge (C): layered codebases route calls
	// through an injected interface (XxxService), so the head references the
	// interface name while the business logic lives in a class the head never
	// names (XxxServiceImpl). Neither direction above crosses that gap — the
	// callee direction stops at the interface definition, the caller direction
	// seeds only on head symbols. Two matching tiers, strongest first:
	// the <Interface>Impl naming convention, then declaration lines whose
	// `implements` clause names a referenced type.
	implAdded := 0
	bridged := map[string]bool{} // interfaces already bridged to an impl
	implDefs := map[string]int{}
	for i, c := range candidates {
		sym := c.chunk.Symbol
		if picked[i] || len(sym) <= len("Impl") || !strings.HasSuffix(sym, "Impl") {
			continue
		}
		if prev, ok := implDefs[sym]; !ok || spanLines(candidates[prev]) > spanLines(c) {
			implDefs[sym] = i
		}
	}
	implSyms := make([]string, 0, len(implDefs))
	for sym := range implDefs {
		iface := strings.TrimSuffix(sym, "Impl")
		if refTokens[iface] > 0 && fileFanout(iface) <= callerFanoutMax {
			implSyms = append(implSyms, sym)
		}
	}
	sort.Slice(implSyms, func(i, j int) bool {
		ri, rj := refTokens[strings.TrimSuffix(implSyms[i], "Impl")], refTokens[strings.TrimSuffix(implSyms[j], "Impl")]
		if ri == rj {
			return implSyms[i] < implSyms[j]
		}
		return ri > rj
	})
	for _, sym := range implSyms {
		if implAdded >= maxPulledImpls {
			break
		}
		idx := implDefs[sym]
		if picked[idx] {
			continue
		}
		picked[idx] = true
		order = append(order, idx)
		bridged[strings.TrimSuffix(sym, "Impl")] = true
		implAdded++
	}
	if implAdded < maxPulledImpls {
		// Tier 2 inverts the scan (few `implements` declarations vs. many
		// head tokens): each unpicked type-like chunk exposes the interfaces
		// its declaration line implements; a chunk whose clause names a
		// referenced, non-generic interface completes the chain.
		type implMatch struct{ idx, refs int }
		matches := []implMatch{}
		for i, c := range candidates {
			if picked[i] || !typeDefKinds[c.chunk.SymbolKind] || !strings.Contains(c.content, "implements") {
				continue
			}
			refs := 0
			for _, line := range strings.Split(c.content, "\n") {
				pos := strings.Index(line, "implements")
				if pos < 0 {
					continue
				}
				for tok := range identTokens(line[pos+len("implements"):]) {
					if len(tok) >= minSeedSymLen && tok != c.chunk.Symbol && !bridged[tok] &&
						refTokens[tok] > 0 && fileFanout(tok) <= callerFanoutMax {
						refs++
					}
				}
			}
			if refs > 0 {
				matches = append(matches, implMatch{idx: i, refs: refs})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].refs == matches[j].refs {
				return fused[matches[i].idx] > fused[matches[j].idx]
			}
			return matches[i].refs > matches[j].refs
		})
		for _, m := range matches {
			if implAdded >= maxPulledImpls {
				break
			}
			picked[m.idx] = true
			order = append(order, m.idx)
			implAdded++
		}
	}

	// Caller direction: seeds are the head picks' own symbols; matches are
	// chunks in files the head does not already cover. One caller per file
	// keeps the expansion spread across layers instead of piling onto one.
	seeds := []string{}
	for i, idx := range order {
		if i >= callerSeedLimit || len(seeds) >= callerSeedLimit {
			break
		}
		sym := candidates[idx].chunk.Symbol
		if len(sym) >= minSeedSymLen && !typeDefKinds[candidates[idx].chunk.SymbolKind] {
			seeds = append(seeds, sym)
		}
	}
	if len(seeds) == 0 {
		return order
	}
	matches := map[int][]string{}
	for i, c := range candidates {
		if picked[i] || pickedPaths[logicalPath(c.path)] {
			continue
		}
		for _, sym := range seeds {
			if containsIdent(c.content, sym) {
				matches[i] = append(matches[i], sym)
			}
		}
	}
	type caller struct {
		idx, refs int
	}
	callers := []caller{}
	for idx, syms := range matches {
		refs := 0
		for _, sym := range syms {
			if fileFanout(sym) <= callerFanoutMax {
				refs++
			}
		}
		if refs > 0 {
			callers = append(callers, caller{idx: idx, refs: refs})
		}
	}
	sort.Slice(callers, func(i, j int) bool {
		if callers[i].refs == callers[j].refs {
			return fused[callers[i].idx] > fused[callers[j].idx]
		}
		return callers[i].refs > callers[j].refs
	})
	usedPaths := map[string]bool{}
	added := 0
	for _, c := range callers {
		if added >= maxPulledCallers {
			break
		}
		path := logicalPath(candidates[c.idx].path)
		if usedPaths[path] {
			continue
		}
		usedPaths[path] = true
		picked[c.idx] = true
		order = append(order, c.idx)
		added++
	}
	return order
}

// containsIdent reports whether sym occurs in content as a whole identifier
// (not as a substring of a longer name).
func containsIdent(content, sym string) bool {
	for start := 0; ; {
		i := strings.Index(content[start:], sym)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(sym)
		if (i == 0 || !isIdentByte(content[i-1])) && (end >= len(content) || !isIdentByte(content[end])) {
			return true
		}
		start = i + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// relatedSymbolHints (optimization E) lists symbols that the selected context
// references and that are defined somewhere in the indexed corpus, but whose
// definitions did not make it into the output. Agents use these as grep leads
// instead of guessing names.
func relatedSymbolHints(candidates []*candidate, selected []SearchHit, pseudo map[string]pseudoSpan) []RelatedSymbol {
	if len(selected) == 0 {
		return nil
	}
	shownSpans := map[string][][2]int{}
	shownSyms := map[string]bool{}
	for _, hit := range selected {
		shownSpans[hit.Path] = append(shownSpans[hit.Path], [2]int{hit.StartLine, hit.EndLine})
		if hit.Symbol != "" {
			shownSyms[hit.Symbol] = true
		}
	}
	covered := func(c *candidate) bool {
		// Hits from remapped pseudo parts carry the logical path with
		// logical-file line numbers; shift the chunk by its part offset
		// before comparing. Non-remapped chunks match under their raw path.
		off := pseudo[c.chunk.BlobName].offset
		for _, span := range shownSpans[logicalPath(c.path)] {
			if c.chunk.StartLine+off >= span[0] && c.chunk.EndLine+off <= span[1] {
				return true
			}
		}
		for _, span := range shownSpans[c.path] {
			if c.chunk.StartLine >= span[0] && c.chunk.EndLine <= span[1] {
				return true
			}
		}
		return false
	}
	defs := map[string]*candidate{}
	for _, c := range candidates {
		sym := c.chunk.Symbol
		if sym == "" || !hintKinds[c.chunk.SymbolKind] || shownSyms[sym] || covered(c) {
			continue
		}
		if _, ok := defs[sym]; !ok {
			defs[sym] = c
		}
	}
	if len(defs) == 0 {
		return nil
	}
	refs := map[string]int{}
	for _, hit := range selected {
		for tok := range identTokens(hit.Content) {
			if _, ok := defs[tok]; ok {
				refs[tok]++
			}
		}
	}
	syms := make([]string, 0, len(refs))
	for sym := range refs {
		syms = append(syms, sym)
	}
	sort.Slice(syms, func(i, j int) bool {
		if refs[syms[i]] == refs[syms[j]] {
			return syms[i] < syms[j]
		}
		return refs[syms[i]] > refs[syms[j]]
	})
	if len(syms) > maxRelatedHints {
		syms = syms[:maxRelatedHints]
	}
	out := make([]RelatedSymbol, 0, len(syms))
	for _, sym := range syms {
		c := defs[sym]
		out = append(out, RelatedSymbol{Name: sym, Kind: c.chunk.SymbolKind, Path: logicalPath(c.path)})
	}
	return out
}

func spanLines(c *candidate) int { return c.chunk.EndLine - c.chunk.StartLine + 1 }

// RelatedSymbol is a grep lead emitted alongside the context block.
type RelatedSymbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}
