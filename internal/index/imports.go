package index

import (
	"context"
	"fmt"
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/securefile"
)

// imports.go is the IMPORTS-edge pass for TS/JS: importer File -> imported File.
//
// Scope (MVP): relative specifiers only (`./x`, `../x`), resolved against the set
// of indexed files. Package imports (`@nestjs/common`, `react`) have no File node
// and are skipped. Go imports target packages, not files, which needs a
// package-node model — deferred. Unresolved edges would be dropped by the store
// anyway; we resolve here in-memory so we never emit a wrong edge.

// fileSrc is a file's bytes + metadata for the testable resolver (no disk).
type fileSrc struct {
	RelPath string
	Lang    Lang
	Data    []byte
}

// ResolveImports reads the files and emits IMPORTS edges. It is the disk-reading
// wrapper around resolveImports. Prefer resolveImportsStreaming during indexing —
// it holds one file at a time instead of the whole codebase.
func ResolveImports(project string, files []SourceFile) []graph.Edge {
	srcs := make([]fileSrc, 0, len(files))
	for _, f := range files {
		if !supportsImports(f.Lang) {
			continue
		}
		data, err := securefile.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		srcs = append(srcs, fileSrc{RelPath: f.RelPath, Lang: f.Lang, Data: data})
	}
	return resolveImports(project, srcs)
}

// collectImportsStreamingContext resolves IMPORTS one file at a time. Source
// bytes are not retained for the whole repo; only the small edge list grows
// until a single insert. It checks cancellation at each file boundary in both
// the existence pass and the source-reading pass. The caller can therefore stop
// a large import collection without waiting for every remaining file.
func collectImportsStreamingContext(ctx context.Context, project string, files []SourceFile) ([]graph.Edge, error) {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		exists[f.RelPath] = true
	}
	rubyLoadPaths, err := rubyRequireLoadPathsFromSourceFilesContext(ctx, files)
	if err != nil {
		return nil, err
	}
	var edges []graph.Edge
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return edges, err
		}
		if !supportsImports(f.Lang) {
			continue
		}
		data, err := securefile.ReadFile(f.AbsPath)
		if err != nil {
			return edges, fmt.Errorf("read source for imports %q: %w", f.RelPath, err)
		}
		edges = append(edges, importEdgesForSource(project, fileSrc{RelPath: f.RelPath, Lang: f.Lang, Data: data}, exists, rubyLoadPaths)...)
	}
	if err := ctx.Err(); err != nil {
		return edges, err
	}
	return edges, nil
}

func importEdgesForFile(project, relPath string, lang Lang, data []byte, exists map[string]bool) []graph.Edge {
	fileQN := project + ":" + relPath
	var edges []graph.Edge
	for _, spec := range extractImportSpecifiers(langFor(lang), data) {
		target, ok := resolveTSImport(relPath, spec, exists)
		if !ok || target == relPath {
			continue
		}
		edges = append(edges, graph.Edge{
			Project: project, SourceQN: fileQN,
			TargetQN: project + ":" + target, Type: graph.EdgeImports,
			Props: map[string]any{"specifier": spec},
		})
	}
	return edges
}

// resolveImports is the testable core: it resolves each TS/JS file's relative
// imports against the known file set and returns IMPORTS edges.
func resolveImports(project string, files []fileSrc) []graph.Edge {
	exists := make(map[string]bool, len(files))
	for _, f := range files {
		exists[f.RelPath] = true
	}
	rubyLoadPaths := rubyRequireLoadPaths(files)

	var edges []graph.Edge
	for _, f := range files {
		edges = append(edges, importEdgesForSource(project, f, exists, rubyLoadPaths)...)
	}
	return edges
}

// Unsupported languages have no IMPORTS model; opening their source adds work
// without evidence. Definitions and other source consumers still validate them.
func supportsImports(lang Lang) bool {
	return lang == LangTS || lang == LangTSX || lang == LangJS || lang == LangRuby || lang == LangPython
}

func importEdgesForSource(project string, f fileSrc, exists map[string]bool, rubyLoadPaths []string) []graph.Edge {
	switch f.Lang {
	case LangRuby:
		return rubyImportEdgesForFile(project, f.RelPath, f.Data, exists, rubyLoadPaths)
	case LangPython:
		return pythonImportEdgesForFile(project, f.RelPath, f.Data, exists)
	case LangTS, LangTSX, LangJS:
		return importEdgesForFile(project, f.RelPath, f.Lang, f.Data, exists)
	default:
		return nil
	}
}

func rubyImportEdgesForFile(project, relPath string, data []byte, exists map[string]bool, rubyLoadPaths []string) []graph.Edge {
	fileQN := project + ":" + relPath
	var edges []graph.Edge
	seen := map[string]bool{}
	for _, spec := range extractRubyRequireRelativeSpecifiers(data) {
		target, ok := resolveRubyRequireRelative(relPath, spec, exists)
		if !ok || target == relPath || seen[target] {
			continue
		}
		seen[target] = true
		edges = append(edges, graph.Edge{Project: project, SourceQN: fileQN,
			TargetQN: project + ":" + target, Type: graph.EdgeImports,
			Props: map[string]any{"specifier": spec}})
	}
	for _, spec := range extractRubyRequireSpecifiers(data) {
		target, ok := resolveRubyRequire(spec, rubyLoadPaths, exists)
		if !ok || target == relPath || seen[target] {
			continue
		}
		seen[target] = true
		edges = append(edges, graph.Edge{Project: project, SourceQN: fileQN,
			TargetQN: project + ":" + target, Type: graph.EdgeImports,
			Props: map[string]any{"specifier": spec}})
	}
	return edges
}

// pythonImportEdgesForFile resolves repo-local, source-level Python imports to
// importer File -> imported File edges. Only module-level statements claim
// files: a conditional import inside try/except is runtime-dependent, so it
// claims nothing rather than guessing. Absolute `import a.b.c` claims the full
// dotted chain (each prefix is a module Python executes on import, resolved
// against the indexed file set); `from a.b import sym` claims only the module
// chain because the symbol may equally be an attribute; and `from . import sym`
// (empty module inside a package) resolves each symbol as a sibling submodule
// file of the importing file's directory, proven by file existence. unresolved
// specifiers are skipped — the store would drop the edge anyway, but resolving
// in-memory keeps the pass honest about what it claimed.
type pyImport struct {
	dots    int      // leading dots of a relative import (0 = absolute)
	module  string   // dotted module path without the dots; "" for `from . import x`
	symbols []string // sibling submodules, only meaningful when module == ""
	spec    string   // dotted specifier, e.g. "services.auth" or ".auth"
}

func pythonImportEdgesForFile(project, relPath string, data []byte, exists map[string]bool) []graph.Edge {
	fileQN := project + ":" + relPath
	var edges []graph.Edge
	seen := map[string]bool{}
	for _, imp := range extractPyImports(data) {
		for _, target := range resolvePyImportTargets(relPath, imp, exists) {
			if target == relPath || seen[target] {
				continue
			}
			seen[target] = true
			edges = append(edges, graph.Edge{Project: project, SourceQN: fileQN,
				TargetQN: project + ":" + target, Type: graph.EdgeImports,
				Props: map[string]any{"specifier": imp.spec}})
		}
	}
	return edges
}

// resolvePyImportTargets maps one pyImport to the repository-relative files the
// import executes, proven by the indexed file set. Absolute modules claim every
// dotted prefix that resolves to a module file or package (`import a.b.c`
// executes a/__init__.py, a/b/__init__.py, and a/b/c.py); `from . import sym`
// resolves each symbol as a sibling module or package of the importing file's
// directory; relative dots climb from that directory, and an import that needs
// to escape the repository resolves to nothing.
func resolvePyImportTargets(importerRel string, imp pyImport, exists map[string]bool) []string {
	if imp.dots == 0 && imp.module == "" {
		return nil
	}
	baseDir := path.Dir(importerRel)
	if imp.dots == 0 {
		baseDir = "" // absolute modules resolve from the repository root
	}
	for i := 1; i < imp.dots; i++ { // one dot stays inside the importing directory
		parent := path.Dir(baseDir)
		if parent == baseDir {
			return nil // the import climbs above the repository root
		}
		baseDir = parent
	}
	if imp.module == "" {
		// Empty module means `from . import sym`: each symbol is a sibling
		// module or package of the importing file's directory.
		var out []string
		for _, sym := range imp.symbols {
			if sym == "*" {
				continue
			}
			out = appendPyTarget(out, path.Join(baseDir, sym), exists)
		}
		return out
	}
	var out []string
	for _, mod := range pyModulePrefixes(imp.module) {
		out = appendPyTarget(out, path.Join(baseDir, mod), exists)
	}
	return out
}

// pyModulePrefixes expands "a.b.c" into its executable import chain
// ("a", "a/b", "a/b/c").
func pyModulePrefixes(module string) []string {
	parts := strings.Split(module, ".")
	out := make([]string, 0, len(parts))
	parent := ""
	for _, part := range parts {
		parent = path.Join(parent, part)
		out = append(out, parent)
	}
	return out
}

// appendPyTarget claims one module reference (rel without extension) as either
// its module file or its package initializer, both of which Python executes.
func appendPyTarget(out []string, rel string, exists map[string]bool) []string {
	if exists[rel+".py"] {
		out = append(out, rel+".py")
	}
	if exists[rel+"/"+"__init__.py"] {
		out = append(out, rel+"/"+"__init__.py")
	}
	return out
}

func extractPyImports(data []byte) []pyImport {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(langFor(LangPython)); err != nil {
		return nil
	}
	tree := parser.Parse(data, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	var out []pyImport
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		switch n.Kind() {
		case "import_statement":
			for j := uint(0); j < n.NamedChildCount(); j++ {
				if name := pyImportedModuleName(n.NamedChild(j), data); name != "" {
					out = append(out, pyImport{module: name, spec: name})
				}
			}
		case "import_from_statement":
			if imp, ok := pyImportFrom(n, data); ok {
				out = append(out, imp)
			}
		}
	}
	return out
}

// pyImportedModuleName reduces an `import` argument (dotted_name or an
// aliased `module as alias` wrapper) to the module being imported.
func pyImportedModuleName(n *tree_sitter.Node, data []byte) string {
	switch n.Kind() {
	case "dotted_name":
		return n.Utf8Text(data)
	case "aliased_import":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(data)
		}
	}
	return ""
}

// pyImportFrom reduces `from MODULE import SYMBOLS` to its module reference and
// imported symbols. The module part is the first dotted_name/relative_import
// child; each later imported symbol contributes its name, and a wildcard
// import contributes none (a star import claims attributes, not files).
func pyImportFrom(n *tree_sitter.Node, data []byte) (pyImport, bool) {
	var imp pyImport
	for j := uint(0); j < n.NamedChildCount(); j++ {
		c := n.NamedChild(j)
		switch c.Kind() {
		case "dotted_name":
			if imp.module == "" && imp.dots == 0 && imp.spec == "" {
				imp.module = c.Utf8Text(data)
				imp.spec = imp.module
				continue
			}
			imp.symbols = append(imp.symbols, c.Utf8Text(data))
		case "relative_import":
			text := c.Utf8Text(data)
			imp.dots = len(text) - len(strings.TrimLeft(text, "."))
			imp.module = strings.TrimLeft(text, ".")
			imp.spec = text
		case "aliased_import", "import_as_name":
			if name := c.ChildByFieldName("name"); name != nil {
				imp.symbols = append(imp.symbols, name.Utf8Text(data))
			}
		case "wildcard_import":
			// from x import *: no single symbol, nothing resolvable to claim.
		}
	}
	if imp.dots == 0 && imp.module == "" {
		return pyImport{}, false
	}
	return imp, true
}
func extractRubyRequireRelativeSpecifiers(data []byte) []string {
	return extractRubyCallSpecifiers(data, "require_relative")
}

func extractRubyRequireSpecifiers(data []byte) []string {
	return extractRubyCallSpecifiers(data, "require")
}

func extractRubyCallSpecifiers(data []byte, method string) []string {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(langFor(LangRuby)); err != nil {
		return nil
	}
	tree := parser.Parse(data, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	var specs []string
	var walk func(*tree_sitter.Node)
	walk = func(n *tree_sitter.Node) {
		if n.Kind() == "call" && n.ChildByFieldName("receiver") == nil &&
			rubyNodeName(n.ChildByFieldName("method"), data) == method {
			if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() == 1 {
				if spec, ok := rubyLiteralStringNode(args.NamedChild(0), data); ok && spec != "" {
					specs = append(specs, spec)
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return specs
}

func resolveRubyRequireRelative(importerRel, spec string, exists map[string]bool) (string, bool) {
	// RelPath is normalized with filepath.ToSlash during discovery.
	base := path.Clean(path.Join(path.Dir(importerRel), spec))
	if exists[base] {
		return base, true
	}
	if exists[base+".rb"] {
		return base + ".rb", true
	}
	return "", false
}

// rubyRequireLoadPaths recognizes only Rails application config that explicitly adds
// literal autoload paths to Ruby's $LOAD_PATH via tree-sitter AST, never via regex
// on raw text. This prevents false matches inside strings, heredocs, and comments.
func rubyRequireLoadPaths(files []fileSrc) []string {
	for _, f := range files {
		if f.RelPath == "config/application.rb" && f.Lang == LangRuby {
			return rubyRequireLoadPathsFromApplication(f.Data)
		}
	}
	return nil
}

func rubyRequireLoadPathsFromSourceFilesContext(ctx context.Context, files []SourceFile) ([]string, error) {
	ctx = nonNilContext(ctx)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.RelPath != "config/application.rb" || f.Lang != LangRuby {
			continue
		}
		data, err := securefile.ReadFile(f.AbsPath)
		if err != nil {
			return nil, fmt.Errorf("read Ruby application config %q: %w", f.RelPath, err)
		}
		paths := rubyRequireLoadPathsFromApplication(data)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return paths, nil
	}
	return nil, nil
}

// rubyRequireLoadPathsFromApplication parses config/application.rb with the Ruby
// tree-sitter grammar and reads only explicit, non-string-literal configuration
// statements. A false or ambiguous `add_autoload_paths_to_load_path` disables;
// string-literal content is skipped; conditional branches are treated conservatively.
func rubyRequireLoadPathsFromApplication(data []byte) []string {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(langFor(LangRuby)); err != nil {
		return nil
	}
	tree := parser.Parse(data, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	enabled := false
	disabled := false
	var paths []string

	var walk func(*tree_sitter.Node, bool)
	walk = func(n *tree_sitter.Node, cond bool) {
		// AST-walking safety: never descend into string literal or heredoc
		// content — code there is data, not executed configuration.
		switch n.Kind() {
		case "string", "heredoc_body", "heredoc_beginning", "interpolation", "comment":
			return
		}

		// Control-flow nodes mark subsequent config as conditional. We reject
		// conditional config because it is not provably active at runtime.
		switch n.Kind() {
		case "if", "unless", "case", "while", "until", "elsif", "else":
			cond = true
		}

		// Pattern: config.add_autoload_paths_to_load_path = true/false
		// tree-sitter-ruby: assignment node, two named children (LHS + RHS),
		// anonymous = sign. Reject inside conditionals.
		if !cond && n.Kind() == "assignment" && n.NamedChildCount() >= 2 &&
			n.NamedChild(0).Kind() == "call" &&
			rubyNodeName(n.NamedChild(0), data) == "config.add_autoload_paths_to_load_path" {
			switch n.NamedChild(1).Kind() {
			case "true":
				enabled = true
			case "false":
				disabled = true
			}
			return
		}

		// Pattern: config.autoload_paths << Rails.root.join("path")
		// tree-sitter-ruby: binary node, two named children (LHS + RHS), and an
		// anonymous operator child at index 1. We verify the operator is <<.
		// Reject inside conditionals because the path addition is not provable.
		if !cond && n.Kind() == "binary" && n.ChildCount() >= 3 &&
			n.Child(1).Kind() == "<<" &&
			n.NamedChildCount() >= 2 &&
			n.NamedChild(0).Kind() == "call" &&
			rubyNodeName(n.NamedChild(0), data) == "config.autoload_paths" {
			if p, ok := extractRubyAutoloadPathFromJoin(n.NamedChild(1), data); ok && p != "" {
				paths = append(paths, p)
			}
			return
		}

		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), cond)
		}
	}
	walk(tree.RootNode(), false)

	if disabled || !enabled {
		return nil
	}
	return paths
}

// extractRubyAutoloadPathFromJoin extracts a literal path argument from a
// Rails.root.join("path") call, returning ("", false) for any other call shape.
func extractRubyAutoloadPathFromJoin(n *tree_sitter.Node, data []byte) (string, bool) {
	if n.Kind() != "call" {
		return "", false
	}
	method := rubyNodeName(n.ChildByFieldName("method"), data)
	if method != "join" {
		return "", false
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil || rubyNodeName(recv, data) != "Rails.root" {
		return "", false
	}
	args := n.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() != 1 {
		return "", false
	}
	p, ok := rubyLiteralStringNode(args.NamedChild(0), data)
	if !ok || p == "" {
		return "", false
	}
	rel := path.Clean(p)
	if rel == "." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	return rel, true
}

func resolveRubyRequire(spec string, loadPaths []string, exists map[string]bool) (string, bool) {
	if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return "", false
	}
	for _, loadPath := range loadPaths {
		base := path.Clean(path.Join(loadPath, spec))
		if exists[base] {
			return base, true
		}
		if exists[base+".rb"] {
			return base + ".rb", true
		}
	}
	return "", false
}

// extractImportSpecifiers returns the raw `from` strings of import/export
// statements (e.g. "./bar", "@nestjs/common"), quotes stripped.
func extractImportSpecifiers(grammar *tree_sitter.Language, data []byte) []string {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(grammar); err != nil {
		return nil
	}
	tree := parser.Parse(data, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	root := tree.RootNode()
	var specs []string
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		if k := n.Kind(); k != "import_statement" && k != "export_statement" {
			continue
		}
		src := n.ChildByFieldName("source") // nil for `export { x }` with no `from`
		if src == nil {
			continue
		}
		specs = append(specs, unquote(src.Utf8Text(data)))
	}
	return specs
}

// resolveTSImport resolves a relative specifier to an existing repo-relative file
// path, trying common extensions then a directory index file. Returns
// ("", false) for non-relative (package) specifiers or when nothing matches.
func resolveTSImport(importerRel, specifier string, exists map[string]bool) (string, bool) {
	if !strings.HasPrefix(specifier, ".") {
		return "", false
	}
	base := path.Join(path.Dir(importerRel), specifier)
	exts := []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}

	if exists[base] { // specifier already carried an extension
		return base, true
	}
	for _, e := range exts {
		if exists[base+e] {
			return base + e, true
		}
	}
	for _, e := range exts {
		if cand := base + "/index" + e; exists[cand] {
			return cand, true
		}
	}
	return "", false
}

func unquote(s string) string {
	if len(s) >= 2 {
		q := s[0]
		if (q == '\'' || q == '"' || q == '`') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}
