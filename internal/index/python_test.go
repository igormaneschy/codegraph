package index

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// Contract tests for the Python passes, in the same oracle style as the Ruby
// suite: they state what a correct extractor produces for explicit Python
// source, and every assertion says why it matters. Grammar node kinds the
// walker relies on (elif_clause, boolean_operator, case_clause, decorated_
// definition, relative_import with import_prefix) were verified against
// tree-sitter-python v0.25 before these tests were written.

const pythonDefsSrc = `
API_MAX = 10
_RETRIES = 3

class Account(Base):
    """doc"""
    RATE = 0.5

    def __init__(self):
        self.x = 1

    @property
    def show(self):
        return "ok"

    async def fetch(self):
        pass

def top():
    pass

def _hidden():
    pass

async def fetch_all():
    pass

class Registry:
    class Inner:
        class Deep:
            def check(self):
                pass

        def probe(self):
            pass

if TYPE_CHECKING:
    class Ghost:
        pass
`

type pySymbol struct {
	qnSuffix string // after the "p:app/models/account.py." prefix
	label    graph.NodeLabel
	exported bool
	props    map[string]any // additional required props, exact values
}

func pythonSuffix(n graph.Node) string {
	return strings.TrimPrefix(n.QualifiedName, "p:app/models/account.py.")
}

func TestDefs_Python_ParsesModuleScopeDeclarations(t *testing.T) {
	nodes, edges := extractDefsFromSource("p", "app/models/account.py", LangPython, []byte(pythonDefsSrc))

	file := nodes[0]
	if file.Label != graph.LabelFile || file.Props["python_analysis_version"] != pythonAnalysisVersion {
		t.Fatalf("file node without a python analysis version marker: %+v", file)
	}

	want := []pySymbol{
		{qnSuffix: "API_MAX", label: graph.LabelConstant, exported: true},
		{qnSuffix: "_RETRIES", label: graph.LabelConstant, exported: false},
		{qnSuffix: "Account", label: graph.LabelClass, exported: true},
		{qnSuffix: "Account.RATE", label: graph.LabelConstant, exported: true},
		{qnSuffix: "Account.__init__", label: graph.LabelMethod, exported: true,
			props: map[string]any{"owner": "Account"}}, // dunder is a runtime entry point
		{qnSuffix: "Account.show", label: graph.LabelMethod, exported: true,
			props: map[string]any{"owner": "Account", "decorators": []string{"property"}}},
		{qnSuffix: "Account.fetch", label: graph.LabelMethod, exported: true,
			props: map[string]any{"owner": "Account"}}, // async def is an ordinary definition
		{qnSuffix: "top", label: graph.LabelFunction, exported: true},
		{qnSuffix: "_hidden", label: graph.LabelFunction, exported: false},
		{qnSuffix: "fetch_all", label: graph.LabelFunction, exported: true},
		{qnSuffix: "Registry", label: graph.LabelClass, exported: true},
		{qnSuffix: "Registry.Inner", label: graph.LabelClass, exported: true},
		{qnSuffix: "Registry.Inner.Deep", label: graph.LabelClass, exported: true},
		{qnSuffix: "Registry.Inner.Deep.check", label: graph.LabelMethod, exported: true,
			props: map[string]any{"owner": "Registry.Inner.Deep"}},
		{qnSuffix: "Registry.Inner.probe", label: graph.LabelMethod, exported: true,
			props: map[string]any{"owner": "Registry.Inner"}},
		{qnSuffix: "Ghost", label: graph.LabelClass, exported: true}, // TYPE_CHECKING guard is a real declaration
	}

	got := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Label == graph.LabelFile {
			continue
		}
		got[pythonSuffix(n)] = n
	}
	if len(got) != len(want) {
		t.Fatalf("symbol count %d, want %d; got suffixes %v", len(got), len(want), slices.Sorted(maps.Keys(got)))
	}
	for _, w := range want {
		n, ok := got[w.qnSuffix]
		if !ok {
			t.Errorf("missing symbol %s", w.qnSuffix)
			continue
		}
		if n.Label != w.label {
			t.Errorf("%s label %s, want %s", w.qnSuffix, n.Label, w.label)
		}
		if exported := n.Props["is_exported"]; exported != w.exported {
			t.Errorf("%s is_exported %v, want %v", w.qnSuffix, exported, w.exported)
		}
		for key, value := range w.props {
			if got := n.Props[key]; !pyPropEqual(got, value) {
				t.Errorf("%s %s %v, want %v", w.qnSuffix, key, got, value)
			}
		}
		if w.label == graph.LabelClass {
			if _, has := n.Props["complexity"]; has {
				t.Errorf("%s class node must not carry cyclomatic complexity", w.qnSuffix)
			}
		}
	}

	defines := 0
	for _, e := range edges {
		if e.Type == graph.EdgeDefines && e.SourceQN == "p:app/models/account.py" {
			defines++
		}
	}
	if defines != len(want) {
		t.Errorf("DEFINES edges %d, want %d", defines, len(want))
	}
}

// pyPropEqual compares an extractor prop against the wanted value. String
// slices must not be compared with ==; every other prop is exact.
func pyPropEqual(got, want any) bool {
	if gs, ok := got.([]string); ok {
		ws, ok2 := want.([]string)
		return ok2 && slices.Equal(gs, ws)
	}
	return got == want
}

const pythonComplexitySrc = `
def complex(a, b):
    if a and not b:
        pass
    elif a or b:
        pass
    elif a:
        pass
    while a:
        a -= 1
    for i in range(a):
        if a and b and a:
            break
    try:
        pass
    except ValueError:
        pass
    except (TypeError, KeyError):
        pass
    c = 1 if a else 2
    match a:
        case 1:
            pass
        case _ if b:
            pass

def simple():
    pass

def match_only(x):
    match x:
        case 1:
            pass

def outer_nested():
    def inner():
        if a:
            pass
`

func TestDefs_Python_ComplexityCountsPythonDecisionNodes(t *testing.T) {
	nodes, _ := extractDefsFromSource("p", "svc.py", LangPython, []byte(pythonComplexitySrc))

	for _, tc := range []struct {
		suffix string
		want   int
	}{
		// 1 (base) + 2 if + 4 boolean operators (and-chains nest) + 2 elif +
		// while + for + 2 except + ternary + 2 match arms + 1 match guard.
		{suffix: "complex", want: 17},
		{suffix: "simple", want: 1},
		{suffix: "match_only", want: 2},   // each case arm is a path
		{suffix: "outer_nested", want: 2}, // nested function bodies join the full-subtree count, the semantic TS closures already set
	} {
		n, ok := findDef(nodes, tc.suffix)
		if !ok {
			t.Fatalf("missing %s in fixture", tc.suffix)
		}
		if got := n.Props["complexity"]; got != tc.want {
			t.Errorf("%s complexity %v, want %v", tc.suffix, got, tc.want)
		}
	}
	if _, inner := findDef(nodes, "inner"); inner {
		t.Error("a def nested inside a function body must not become a stable module declaration")
	}
}

func TestImports_Python_ResolvesModuleAndSymbolTargets(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "app/pkg/use.py", Lang: LangPython, Data: []byte(`
import a.b.c
from a.b import sym
import vendored.package
from . import config, settings
from .sub import helper
from .. import up
`)},
		{RelPath: "a/__init__.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "a/b/__init__.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "a/b/c.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "a/b.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "app/pkg/config.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "app/pkg/settings.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "app/pkg/sub.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "app/up.py", Lang: LangPython, Data: []byte("")},
	})
	abs := "p:app/pkg/use.py"
	for _, tgt := range []string{
		"p:a/__init__.py", // import a.b.c executes every resolvable dotted prefix
		"p:a/b.py",        // both the module-file and package shapes resolve prefix a/b
		"p:a/b/__init__.py",
		"p:a/b/c.py",
		"p:app/pkg/config.py", // from . import config, settings: sibling submodules
		"p:app/pkg/settings.py",
		"p:app/pkg/sub.py", // from .sub import helper: module file
		"p:app/up.py",      // from .. import up climbs one directory
	} {
		if !hasImport(edges, abs, tgt) {
			t.Errorf("missing IMPORTS use.py -> %s; got %+v", tgt, edges)
		}
	}
	if hasImport(edges, abs, "p:vendored/package.py") {
		t.Error("package import must stay unclaimed")
	}
	// from a.b import sym: the symbol may equally be an attribute, so only the
	// module chain is claimed.
	if hasImport(edges, abs, "p:a/b/sym.py") {
		t.Error("from-import symbol must not become a file claim")
	}
	specifiers := map[string]string{}
	for _, e := range edges {
		if e.SourceQN == abs && e.Props["specifier"] != nil {
			specifiers[e.TargetQN] = e.Props["specifier"].(string)
		}
	}
	if specifiers["p:app/pkg/sub.py"] != ".sub" || specifiers["p:app/pkg/config.py"] != "." ||
		specifiers["p:a/b/c.py"] != "a.b.c" {
		t.Errorf("specifier props drifted: %v", specifiers)
	}
}

func TestImports_Python_ClaimsBothFileAndPackageModuleShapes(t *testing.T) {
	// Both x.py and x/__init__.py model a resolvable module; Python executes
	// whichever it finds, so both resolving shapes are claimed as written.
	edges := resolveImports("p", []fileSrc{
		{RelPath: "svc.py", Lang: LangPython, Data: []byte("import mod\n")},
		{RelPath: "mod.py", Lang: LangPython, Data: []byte("x = 1\n")},
		{RelPath: "mod/__init__.py", Lang: LangPython, Data: []byte("y = 2\n")},
	})
	if !hasImport(edges, "p:svc.py", "p:mod.py") || !hasImport(edges, "p:svc.py", "p:mod/__init__.py") {
		t.Errorf("module file or package initializer missed: %+v", edges)
	}
}

func TestImports_Python_ParsesImportListShapes(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "app/main.py", Lang: LangPython, Data: []byte(`
import json, os
import long as alias
from parens import (alpha, beta)
from star import *
from xml import etree as tag
`)},
		{RelPath: "json.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "os.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "long.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "parens/__init__.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "xml/__init__.py", Lang: LangPython, Data: []byte("")},
	})
	for _, tgt := range []string{
		"p:json.py", "p:os.py", "p:long.py", // comma list and import-as
		"p:parens/__init__.py", // parenthesized symbol list flattens to one module
		"p:xml/__init__.py",    // aliased from-import still claims the module
	} {
		if !hasImport(edges, "p:app/main.py", tgt) {
			t.Errorf("missing IMPORTS main.py -> %s; got %+v", tgt, edges)
		}
	}
	// from parens import (alpha, ...): symbols may be attributes, so parens
	// claims its package, not each symbol file.
	if edges2 := resolveImports("p", []fileSrc{
		{RelPath: "app/main.py", Lang: LangPython, Data: []byte("from parens import (alpha, beta)\n")},
		{RelPath: "parens/alpha.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "parens/__init__.py", Lang: LangPython, Data: []byte("")},
	}); hasImport(edges2, "p:app/main.py", "p:parens/alpha.py") {
		t.Error("from-import symbols must not individually claim files")
	}
}

func TestImports_Python_ExcludesSelfImports(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "pkg/mod.py", Lang: LangPython, Data: []byte("import pkg.mod\nfrom .mod import pad\n")},
		{RelPath: "pkg/__init__.py", Lang: LangPython, Data: []byte("")},
	})
	if hasImport(edges, "p:pkg/mod.py", "p:pkg/mod.py") {
		t.Errorf("a module must not import itself: %+v", edges)
	}
	if !hasImport(edges, "p:pkg/mod.py", "p:pkg/__init__.py") {
		t.Errorf("the package prefix of pkg.mod must still be claimed: %+v", edges)
	}
}

func TestImports_Python_SkipsConditionalImportsAndEscapes(t *testing.T) {
	// Only module-level import statements claim files: imports inside try/except
	// or if branches are runtime-dependent, and relative dots that climb above
	// the repository resolve to nothing rather than a wrong edge.
	edges := resolveImports("p", []fileSrc{
		{RelPath: "app/fragile.py", Lang: LangPython, Data: []byte(`
try:
    import flaky
except ImportError:
    pass

if flag:
    import optional

from .... import gone
`)},
		{RelPath: "top.py", Lang: LangPython, Data: []byte("from .. import beyond\n")},
		{RelPath: "flaky.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "optional.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "gone.py", Lang: LangPython, Data: []byte("")},
		{RelPath: "beyond.py", Lang: LangPython, Data: []byte("")},
	})
	if len(edges) != 0 {
		t.Errorf("conditional or escaping imports must not claim files: %+v", edges)
	}
}

// TestPrepareIndexing_PythonAnalysisFreshness pins the Python gate on both
// prepareIndexing outcomes: a graph whose python_analysis_version is current
// certifies a no-op, while a legacy graph (built before the marker existed)
// must rebuild — the same upgrade policy the Ruby analysis established.
func TestPrepareIndexing_PythonAnalysisFreshness(t *testing.T) {
	dir := t.TempDir()
	source := []byte("def run():\n    pass\n")
	writeSourceFile(t, dir, "app/main.py", string(source))
	dbPath := filepath.Join(dir, "graph.db")

	if _, err := RunAtomic(dbPath, dir); err != nil {
		t.Fatalf("initial atomic run: %v", err)
	}
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, reused, err := prepareIndexing(store, dir); err != nil {
		t.Fatal(err)
	} else if reused == nil {
		t.Error("a graph with current python analysis must take the no-op path")
	}
}

func TestPrepareIndexing_UpgradesLegacyPythonAnalysis(t *testing.T) {
	dir := t.TempDir()
	source := []byte("def run():\n    pass\n")
	writeSourceFile(t, dir, "app/main.py", string(source))
	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	nodes, edges := extractDefsFromSource(project, "app/main.py", LangPython, source)
	delete(nodes[0].Props, "python_analysis_version") // simulate the previous release's file metadata
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	_, reused, err := prepareIndexing(store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if reused != nil {
		t.Fatal("legacy Python graph must not take the no-op path")
	}
}

func TestIsTestFile_PythonTestPrefixAndConftest(t *testing.T) {
	for path, want := range map[string]bool{
		"tests/test_user.py": true, // pytest convention
		"app/test_main.py":   true,
		"tests/conftest.py":  false, // fixture collector, not a test entry point
		"tests/testing.py":   false, // only the test_ file-name prefix counts
		"src/user.py":        false,
		"utils_test.go":      true, // the pre-existing _test.go convention still wins
	} {
		if got := IsTestFile(path); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}
