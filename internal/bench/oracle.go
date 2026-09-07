package bench

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// GoCallersOracle answers "who calls FuncName?" for a Go tree with go/parser +
// go/ast — a different frontend from the graph's own tree-sitter + go/packages
// VTA pipeline. That independence is the point: completeness is measured
// against this oracle, never by asking the graph whether it agrees with
// itself.
//
// Keys are qualified like the graph's stored refs: `<relpath>.<func>` for
// plain functions (`relpath` keeps the `.go` suffix, e.g.
// `c00.go.Caller0001`), `<relpath>.<Recv>.<method>` for methods. Only direct identifier
// calls (`Hub(...)`, `r.Hub(...)` where the selector is Hub) count —
// function values, interface dispatch and reflection are out of scope for both
// sides here, so the fixture must avoid them (same constraint the graph's
// precision statement already carries).
//
// Test files are included: the indexer loads them too, so excluding them would
// skew the comparison. Files that fail to parse fail the oracle loudly.
func GoCallersOracle(root, funcName string) (map[string]bool, error) {
	out := map[string]bool{}
	var parseErr error
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			parseErr = fmt.Errorf("oracle parse %s: %w", path, err)
			return parseErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		base := filepath.ToSlash(rel) // keeps ".go": matches stored QNs
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			caller := base + "." + fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				caller = base + "." + recvName(fn.Recv.List[0].Type) + "." + fn.Name.Name
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if found {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					found = fun.Name == funcName
				case *ast.SelectorExpr:
					found = fun.Sel.Name == funcName
				}
				return !found
			})
			if found {
				out[caller] = true
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvName(t.X)
	}
	return "recv"
}
