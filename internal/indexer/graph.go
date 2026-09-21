package indexer

import (
	"context"
	"sort"
	"strings"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/store"
)

// Project relation graph: the console's per-project "graph" view. Nodes are
// the most-referenced symbol definitions in the snapshot, edges say "code
// carrying symbol X references symbol Y". Everything derives from data the
// index already stores (chunk symbols + blob contents) via the same identifier
// matching the relation pass uses at search time — no new index structures.
//
// The build is a full-content scan, so callers cache the result per snapshot
// ID (content-addressed: same snapshot, same graph, forever).

const (
	graphMaxNodes     = 40
	graphMaxEdges     = 120
	graphMaxChunks    = 120_000 // scan guardrail for pathological projects
	graphMinSymLen    = 4       // below this, names are generic across any codebase
	graphSummaryRunes = 120
	// A symbol referenced from more logical files than this is scaffolding
	// (base response types, shared logging); as graph hubs they connect to
	// everything and explain nothing.
	graphFanoutMax = 60
)

// graphNodeKinds are the chunk symbol kinds worth drawing: code declarations.
// Config keys, markdown headings and window/preamble filler stay out.
var graphNodeKinds = map[string]bool{
	"func": true, "method": true, "class": true, "type": true,
	"interface": true, "impl": true,
}

// graphSymbolStop drops names that pass the length floor but are language
// keywords or universal noise; the regex chunker occasionally mislabels them
// as declarations (the related_symbols "for" lesson).
var graphSymbolStop = map[string]bool{
	"this": true, "self": true, "true": true, "false": true, "null": true,
	"none": true, "void": true, "type": true, "impl": true, "func": true,
	"class": true, "interface": true, "return": true, "import": true,
	"export": true, "const": true, "default": true, "static": true,
	"public": true, "private": true, "string": true, "number": true,
	"boolean": true, "object": true, "value": true, "data": true,
	"name": true, "test": true, "props": true, "state": true,
}

type GraphNode struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Refs    int    `json:"refs"` // distinct other files referencing the symbol
	Summary string `json:"summary,omitempty"`
}

type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Weight int    `json:"weight"`
}

type ProjectGraph struct {
	Nodes       []GraphNode `json:"nodes"`
	Edges       []GraphEdge `json:"edges"`
	FileCount   int         `json:"file_count"`
	SymbolCount int         `json:"symbol_count"`
}

// ACEProjectByName resolves one of the caller's project rows by (name,
// branch) (for the snapshot ID the graph cache is keyed on).
func (s *Service) ACEProjectByName(ctx context.Context, userID, name, branch string) (domain.ACEProject, error) {
	projects, err := s.store.ListACEProjects(ctx, userID, s.embeddingConfig(ctx).Model)
	if err != nil {
		return domain.ACEProject{}, err
	}
	for _, p := range projects {
		if p.Name == name && p.Branch == branch {
			return p, nil
		}
	}
	return domain.ACEProject{}, store.ErrNotFound
}

// BuildProjectGraph loads a snapshot's files and derives the relation graph.
func (s *Service) BuildProjectGraph(ctx context.Context, snapshotID string) (ProjectGraph, error) {
	snapshot, err := s.store.SnapshotByID(ctx, snapshotID)
	if err != nil {
		return ProjectGraph{}, err
	}
	blobs, err := s.store.BlobsByNames(ctx, snapshot.BlobNames)
	if err != nil {
		return ProjectGraph{}, err
	}
	files := make([]domain.RepositoryFile, 0, len(blobs))
	for _, b := range blobs {
		files = append(files, domain.RepositoryFile{BlobName: b.Name, Path: b.Path, Content: b.Content})
	}
	candidates := s.collectCandidates(ctx, files)
	if len(candidates) > graphMaxChunks {
		candidates = candidates[:graphMaxChunks]
	}
	return buildProjectGraph(candidates, len(files)), nil
}

// graphEligible gates which definitions can become nodes.
func graphEligible(c *candidate) bool {
	sym := c.chunk.Symbol
	return len(sym) >= graphMinSymLen && graphNodeKinds[c.chunk.SymbolKind] && !graphSymbolStop[strings.ToLower(sym)]
}

// buildProjectGraph runs in two passes over the candidate chunks: collect the
// definition index, then walk every chunk's identifier tokens once, crediting
// reference breadth (distinct files) and def→def edges as it goes. This keeps
// the whole build O(total tokens) instead of O(defs × chunks).
func buildProjectGraph(candidates []*candidate, fileCount int) ProjectGraph {
	// Definition index: smallest chunk wins per symbol, same as the relation
	// pass — a 400-line class body is a worse anchor than its declaration.
	defs := map[string]*candidate{}
	for _, c := range candidates {
		if !graphEligible(c) {
			continue
		}
		sym := c.chunk.Symbol
		if prev, ok := defs[sym]; !ok || spanLines(prev) > spanLines(c) {
			defs[sym] = c
		}
	}
	if len(defs) == 0 {
		return ProjectGraph{Nodes: []GraphNode{}, Edges: []GraphEdge{}, FileCount: fileCount}
	}
	refFiles := map[string]map[string]bool{} // def symbol -> referencing logical files
	edgeWeight := map[[2]string]int{}        // [from,to] -> referencing chunk count
	for _, c := range candidates {
		path := logicalPath(c.path)
		own := c.chunk.Symbol // chunk's enclosing symbol, "" for windows/preamble
		for tok := range identTokens(c.content) {
			if _, ok := defs[tok]; !ok || tok == own {
				continue
			}
			if defs[tok] != nil && logicalPath(defs[tok].path) != path {
				m := refFiles[tok]
				if m == nil {
					m = map[string]bool{}
					refFiles[tok] = m
				}
				m[path] = true
			}
			// Edges connect definitions: a chunk belonging to def X that
			// mentions def Y draws X→Y, methods included via enclosing symbol.
			if own != "" && own != tok {
				if _, ok := defs[own]; ok {
					edgeWeight[[2]string{own, tok}]++
				}
			}
		}
	}
	// Node selection: reference breadth decides importance; scaffolding-grade
	// fanout is capped out, unreferenced defs stay off the canvas.
	type scored struct {
		sym  string
		refs int
	}
	ranked := make([]scored, 0, len(refFiles))
	for sym, m := range refFiles {
		if len(m) >= 1 && len(m) <= graphFanoutMax {
			ranked = append(ranked, scored{sym: sym, refs: len(m)})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].refs == ranked[j].refs {
			return ranked[i].sym < ranked[j].sym
		}
		return ranked[i].refs > ranked[j].refs
	})
	if len(ranked) > graphMaxNodes {
		ranked = ranked[:graphMaxNodes]
	}
	selected := map[string]bool{}
	for _, n := range ranked {
		selected[n.sym] = true
	}
	// Edges among selected nodes, strongest first.
	edges := make([]GraphEdge, 0, len(edgeWeight))
	for pair, w := range edgeWeight {
		if selected[pair[0]] && selected[pair[1]] {
			edges = append(edges, GraphEdge{Source: pair[0], Target: pair[1], Weight: w})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Weight == edges[j].Weight {
			if edges[i].Source == edges[j].Source {
				return edges[i].Target < edges[j].Target
			}
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Weight > edges[j].Weight
	})
	if len(edges) > graphMaxEdges {
		edges = edges[:graphMaxEdges]
	}
	// Isolated nodes read as noise on a relation canvas; keep them only when
	// the project yields no edges at all (tiny codebases still get a map).
	connected := map[string]bool{}
	for _, e := range edges {
		connected[e.Source], connected[e.Target] = true, true
	}
	nodes := make([]GraphNode, 0, len(ranked))
	for _, n := range ranked {
		if len(edges) > 0 && !connected[n.sym] {
			continue
		}
		c := defs[n.sym]
		nodes = append(nodes, GraphNode{
			ID: n.sym, Kind: c.chunk.SymbolKind, Path: logicalPath(c.path),
			Line: c.chunk.StartLine, Refs: n.refs, Summary: truncateRunes(c.chunk.Summary, graphSummaryRunes),
		})
	}
	return ProjectGraph{Nodes: nodes, Edges: edges, FileCount: fileCount, SymbolCount: len(defs)}
}

func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
