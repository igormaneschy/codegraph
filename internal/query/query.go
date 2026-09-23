// Package query turns store calls into compact, agent-friendly results.
//
// Token-efficiency principle: every result is a small struct (name + file +
// line + label), NEVER source code. The agent asks for Snippet only when it
// actually needs to read code. That selectivity is where the 10x token saving
// comes from — see docs/ARCHITECTURE.md.
package query

import (
	"strconv"
	"strings"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
)

// Ref is the compact reference returned for every symbol.
type Ref struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Label         string `json:"label"`
	File          string `json:"file"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
}

func refOf(n graph.Node) Ref {
	return Ref{
		Name: n.Name, QualifiedName: n.QualifiedName, Label: string(n.Label),
		File: n.FilePath, StartLine: n.StartLine, EndLine: n.EndLine,
	}
}

// CompactRefs renders refs as the token-efficient wire format: one tab-separated
// line per ref — `label<TAB>name<TAB>file:line<TAB>qn`. No repeated JSON keys, and
// the project prefix is stripped from the qualified name (the engine re-adds it on
// input, so a returned qn can be passed straight back to callers/callees). This is
// the format the MCP/CLI tools return AND the format the benchmark meters, so the
// reported token win reflects the real product, not a measurement trick.
func CompactRefs(refs []Ref) string {
	var b strings.Builder
	for _, r := range refs {
		b.WriteString(r.Label)
		b.WriteByte('\t')
		b.WriteString(r.Name)
		b.WriteByte('\t')
		b.WriteString(r.File)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(r.StartLine))
		b.WriteByte('\t')
		b.WriteString(StripProjectPrefix(r.QualifiedName))
		b.WriteByte('\n')
	}
	return b.String()
}

// StripProjectPrefix drops the `project:` prefix from a qualified name. The rest
// is still globally unambiguous within a project, so it round-trips through
// normalizeQN on the next query.
func StripProjectPrefix(qn string) string {
	if _, rest, found := strings.Cut(qn, ":"); found {
		return rest
	}
	return qn
}

// Engine wraps a store + repo root for a single project.
type Engine struct {
	store       *graph.Store
	project     string
	repoRoot    string
	manifest    index.Manifest
	manifestErr error
}

func NewEngine(store *graph.Store, project, repoRoot string) *Engine {
	e := &Engine{store: store, project: project, repoRoot: repoRoot}
	e.loadManifest()
	return e
}

func (e *Engine) loadManifest() {
	e.manifest, e.manifestErr = index.ReadManifest(e.store.DBPath())
}

// Close releases the underlying store. Safe to call multiple times.
func (e *Engine) Close() error {
	if e.store == nil {
		return nil
	}
	err := e.store.Close()
	e.store = nil
	return err
}

// Reopen closes the current store and opens dbPath. Used after RunAtomic replaces
// the on-disk graph file while the MCP server is running.
func (e *Engine) Reopen(dbPath string) error {
	if e.store == nil {
		st, err := graph.Open(dbPath)
		if err != nil {
			return err
		}
		e.store = st
		e.loadManifest()
		return nil
	}
	if err := e.store.Reopen(dbPath); err != nil {
		return err
	}
	e.loadManifest()
	return nil
}

// TopByInboundCalls returns call-graph hubs for benchmarking and quality tooling.
func (e *Engine) TopByInboundCalls(limit int) ([]graph.Node, error) {
	return e.store.TopByInboundCalls(e.project, limit)
}

// isEntryPoint reports whether an uncalled symbol legitimately has no in-graph
// caller — so the absence of callers is not evidence that it's dead.
func isEntryPoint(n graph.Node) bool {
	if n.Props["is_exported"] == true {
		return true
	}
	if n.Name == "main" || n.Name == "init" {
		return true
	}
	if index.IsTestFile(n.FilePath) {
		return true
	}
	return hasDecorators(n.Props)
}

// hasDecorators reports whether a node carries any decorator. The value round-trips
// through JSON as []any (string slices come back as []interface{}), so accept both.
func hasDecorators(props map[string]any) bool {
	switch d := props["decorators"].(type) {
	case []any:
		return len(d) > 0
	case []string:
		return len(d) > 0
	}
	return false
}

// normalizeQN lets callers pass a qualified name with or without the project
// prefix — the compact wire format strips it, so a returned qn comes back short.
// It also accepts common Go symbol notation that agents infer from package docs.
func (e *Engine) normalizeQN(qn string) string {
	prefix := e.project + ":"
	short := strings.TrimPrefix(qn, prefix)
	short = stripGoModulePrefix(short)
	short = stripGoPointerReceiver(short)
	return prefix + short
}

// stripGoModulePrefix removes github.com/<owner>/<repo>/ from an inferred Go QN.
// Stored QNs are rooted at repository-relative source paths, not module paths.
func stripGoModulePrefix(qn string) string {
	const marker = "github.com/"
	if !strings.HasPrefix(qn, marker) {
		return qn
	}
	rest := strings.TrimPrefix(qn, marker)
	ownerEnd := strings.IndexByte(rest, '/')
	if ownerEnd < 0 {
		return qn
	}
	repoEnd := strings.IndexByte(rest[ownerEnd+1:], '/')
	if repoEnd < 0 {
		return qn
	}
	return rest[ownerEnd+1+repoEnd+1:]
}

// stripGoPointerReceiver turns Go's (*T).Method spelling into T.Method.
func stripGoPointerReceiver(qn string) string {
	for {
		start := strings.Index(qn, "(*")
		if start < 0 {
			return qn
		}
		end := strings.Index(qn[start+2:], ").")
		if end < 0 {
			return qn
		}
		end += start + 2
		qn = qn[:start] + qn[start+2:end] + qn[end+1:]
	}
}

// DetectChanges reports which source files changed since the last index — the
// staleness check behind the detect_changes tool. The agent can tell whether the
// graph is fresh for a region, and re-index if not (cheap now: scope-gated).
func (e *Engine) DetectChanges() (index.Changes, error) {
	return index.DetectChanges(e.store, e.project, e.repoRoot)
}
