package indexer

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tstypescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// AST-based chunking via tree-sitter. The regex chunker's per-language
// patterns are structurally unequal — Java gets method-level symbols while
// TS/JS only match column-0 declarations, so indented class/object methods
// chunk anonymously and lose the symbol score, relate expansion and embed
// enrichment. A real parse tree makes every language equally fine-grained.
// Languages without a grammar here (configs, C++, plain text) and any parse
// that yields no declarations fall back to the heuristic chunker, so this
// layer can never fail indexing.

const maxAstDepth = 6 // recursion guard for pathologically nested declarations

// astSpec describes how one grammar maps onto chunk boundaries.
type astSpec struct {
	// decls maps tree-sitter node kinds to BCE symbol kinds; a matching node
	// becomes a chunk boundary and DFS does not descend into it (unless it is
	// oversized, in which case layoutSegments recurses to its child decls).
	decls map[string]string
	// symbolField overrides the field the symbol name is read from
	// (default "name"): C hides it in "declarator", Rust impl in "type".
	symbolField map[string]string
}

var tsLikeDecls = map[string]string{
	"function_declaration":           "func",
	"generator_function_declaration": "func",
	"class_declaration":              "class",
	"abstract_class_declaration":     "class",
	"interface_declaration":          "interface",
	"type_alias_declaration":         "type",
	"enum_declaration":               "type",
	"method_definition":              "method",
	"field_definition":               "var",
	"public_field_definition":        "var",
	"lexical_declaration":            "var",
	"variable_declaration":           "var",
}

var astSpecs = map[string]astSpec{
	"Java": {decls: map[string]string{
		"class_declaration":           "class",
		"interface_declaration":       "interface",
		"enum_declaration":            "class",
		"record_declaration":          "class",
		"annotation_type_declaration": "class",
		"method_declaration":          "method",
		"constructor_declaration":     "method",
	}},
	"Go": {decls: map[string]string{
		"function_declaration": "func",
		"method_declaration":   "method",
		"type_declaration":     "type",
		"var_declaration":      "var",
		"const_declaration":    "var",
	}},
	"TypeScript": {decls: tsLikeDecls},
	"TSX":        {decls: tsLikeDecls},
	"JavaScript": {decls: tsLikeDecls},
	"Python": {decls: map[string]string{
		"function_definition": "func",
		"class_definition":    "class",
	}},
	"Rust": {
		decls: map[string]string{
			"function_item":    "func",
			"struct_item":      "type",
			"enum_item":        "type",
			"union_item":       "type",
			"trait_item":       "interface",
			"impl_item":        "impl",
			"mod_item":         "mod",
			"const_item":       "var",
			"static_item":      "var",
			"macro_definition": "func",
		},
		symbolField: map[string]string{"impl_item": "type"},
	},
	"C": {
		decls: map[string]string{
			"function_definition": "func",
			"struct_specifier":    "type",
			"enum_specifier":      "type",
			"union_specifier":     "type",
			"type_definition":     "type",
		},
		symbolField: map[string]string{"function_definition": "declarator"},
	},
}

var astLangs = map[string]*sitter.Language{}

func init() {
	astLangs["Java"] = sitter.NewLanguage(tsjava.Language())
	astLangs["Go"] = sitter.NewLanguage(tsgo.Language())
	astLangs["TypeScript"] = sitter.NewLanguage(tstypescript.LanguageTypescript())
	astLangs["TSX"] = sitter.NewLanguage(tstypescript.LanguageTSX())
	astLangs["JavaScript"] = sitter.NewLanguage(tsjs.Language())
	astLangs["Python"] = sitter.NewLanguage(tspython.Language())
	astLangs["Rust"] = sitter.NewLanguage(tsrust.Language())
	astLangs["C"] = sitter.NewLanguage(tsc.Language())
}

// astSegments parses content with the grammar registered for lang and lays it
// out as declaration-boundary segments (0-based half-open line ranges, same
// shape the heuristic path produces). ok=false means no grammar, a failed
// parse, or a parse with no recognizable declarations — callers fall back.
func astSegments(lang, content string) ([]segment, bool) {
	tsLang, hasLang := astLangs[lang]
	spec, hasSpec := astSpecs[lang]
	if !hasLang || !hasSpec || content == "" {
		return nil, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(tsLang); err != nil {
		return nil, false
	}
	src := []byte(content)
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.Kind() == "ERROR" {
		return nil, false
	}
	lines := strings.Split(content, "\n")
	decls := collectDecls(root, spec, 0)
	if len(decls) == 0 {
		return nil, false
	}
	return layoutSegments(decls, 0, len(lines), "", "", 0, spec, src, lines), true
}

// collectDecls walks named children depth-first and returns declaration nodes
// in document order, stopping the descent at each declaration (its interior
// is only revisited if the node turns out oversized). Non-declaration
// containers (export statements, decorated definitions, object literals in a
// Vue options block) are transparent, which is how object-literal methods and
// wrapped classes surface without per-wrapper rules.
func collectDecls(node *sitter.Node, spec astSpec, depth int) []*sitter.Node {
	if depth > maxAstDepth {
		return nil
	}
	out := []*sitter.Node{}
	count := node.NamedChildCount()
	for i := uint(0); i < count; i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		if _, ok := spec.decls[child.Kind()]; ok {
			out = append(out, child)
			continue
		}
		out = append(out, collectDecls(child, spec, depth+1)...)
	}
	return out
}

// layoutSegments turns an ordered declaration list into a full cover of
// [start,end): each declaration is one segment; oversized declarations
// (beyond maxChunkLines) recurse into their child declarations so a 500-line
// class becomes per-method chunks; every gap (headers, fields, imports,
// closing braces) is a filler segment carrying the enclosing symbol, so a
// class header chunk still answers for the class name.
func layoutSegments(decls []*sitter.Node, start, end int, parentSym, parentKind string, depth int, spec astSpec, src []byte, lines []string) []segment {
	segs := []segment{}
	cursor := start
	for _, d := range decls {
		ds, de := nodeRows(d)
		if de > end {
			de = end
		}
		if ds < cursor {
			ds = cursor
		}
		if de <= ds {
			continue
		}
		// Attach contiguous doc comments, Java/Python "@" annotations and
		// Rust "#[" attributes above the declaration: route annotations like
		// @PostMapping are retrieval gold and belong to the method chunk.
		for ds > cursor {
			t := strings.TrimSpace(lines[ds-1])
			if t != "" && (isCommentLine(lines[ds-1]) || strings.HasPrefix(t, "@") || strings.HasPrefix(t, "#[")) {
				ds--
				continue
			}
			break
		}
		if ds > cursor {
			segs = append(segs, segment{start: cursor, end: ds, symbol: parentSym, kind: parentKind})
		}
		sym, kind := declInfo(d, spec, src)
		if de-ds > maxChunkLines && depth < maxAstDepth {
			if inner := collectDecls(d, spec, 0); len(inner) > 0 {
				segs = append(segs, layoutSegments(inner, ds, de, sym, kind, depth+1, spec, src, lines)...)
				cursor = de
				continue
			}
		}
		segs = append(segs, segment{start: ds, end: de, symbol: sym, kind: kind})
		cursor = de
	}
	if cursor < end {
		segs = append(segs, segment{start: cursor, end: end, symbol: parentSym, kind: parentKind})
	}
	return segs
}

// nodeRows converts a node span to 0-based half-open line indices.
func nodeRows(n *sitter.Node) (int, int) {
	start := int(n.StartPosition().Row)
	endPos := n.EndPosition()
	end := int(endPos.Row)
	if endPos.Column > 0 || end == start {
		end++
	}
	return start, end
}

func declInfo(n *sitter.Node, spec astSpec, src []byte) (string, string) {
	kind := spec.decls[n.Kind()]
	field := "name"
	if f, ok := spec.symbolField[n.Kind()]; ok {
		field = f
	}
	target := n.ChildByFieldName(field)
	if target == nil {
		target = n
	}
	return findIdentifier(target, src, 0), kind
}

// findIdentifier returns the first identifier-kind descendant's text —
// grammars name their identifier nodes "identifier", "type_identifier",
// "property_identifier" etc., so a substring match covers all of them.
func findIdentifier(n *sitter.Node, src []byte, depth int) string {
	if strings.Contains(n.Kind(), "identifier") {
		return n.Utf8Text(src)
	}
	if depth >= 3 {
		return ""
	}
	count := n.NamedChildCount()
	for i := uint(0); i < count; i++ {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}
		if sym := findIdentifier(child, src, depth+1); sym != "" {
			return sym
		}
	}
	return ""
}

// vueSegments splits a Vue SFC on its top-level blocks and parses the script
// block's interior with the JS/TS grammar (line numbers shifted by the block
// offset), so options-API methods and <script setup> declarations get real
// symbols. Template/style blocks stay whole (windowed downstream, keeping the
// block name as symbol). Returns nil when no SFC block is found — the caller
// falls back to the heuristic path.
func vueSegments(lines []string) []segment {
	segs := []segment{}
	cursor := 0
	i := 0
	for i < len(lines) {
		var tag string
		switch {
		case strings.HasPrefix(lines[i], "<script"):
			tag = "script"
		case strings.HasPrefix(lines[i], "<template"):
			tag = "template"
		case strings.HasPrefix(lines[i], "<style"):
			tag = "style"
		}
		if tag == "" {
			i++
			continue
		}
		closing := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "</"+tag) {
				closing = j
				break
			}
		}
		if closing < 0 {
			i++
			continue
		}
		if cursor < i {
			segs = append(segs, segment{start: cursor, end: i, kind: "preamble"})
		}
		parsed := false
		if tag == "script" && closing > i+1 {
			langKey := "JavaScript"
			if strings.Contains(lines[i], `lang="ts`) || strings.Contains(lines[i], "lang='ts") {
				langKey = "TypeScript"
			}
			if inner, ok := astSegments(langKey, strings.Join(lines[i+1:closing], "\n")); ok {
				segs = append(segs, segment{start: i, end: i + 1, symbol: "script", kind: "block"})
				for _, s := range inner {
					segs = append(segs, segment{start: s.start + i + 1, end: s.end + i + 1, symbol: s.symbol, kind: s.kind})
				}
				segs = append(segs, segment{start: closing, end: closing + 1, symbol: "script", kind: "block"})
				parsed = true
			}
		}
		if !parsed {
			segs = append(segs, segment{start: i, end: closing + 1, symbol: tag, kind: "block"})
		}
		cursor = closing + 1
		i = closing + 1
	}
	if cursor == 0 {
		return nil
	}
	if cursor < len(lines) {
		segs = append(segs, segment{start: cursor, end: len(lines), kind: "preamble"})
	}
	return segs
}
