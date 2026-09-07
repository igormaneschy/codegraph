package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

// TestToolSpecs_RequiredIsNeverNull is a regression test for a bug dogfooding
// caught: tools with no required args (dead_code, detect_changes) emitted
// `"required":null`, and MCP clients reject it ("expected array, received null"),
// which fails the whole tools/list. JSON Schema's `required` must be an array.
func TestToolSpecs_RequiredIsNeverNull(t *testing.T) {
	b, err := json.Marshal((&Server{}).toolSpecs())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"required":null`)) {
		t.Errorf("a tool spec emits required:null (MCP clients reject it); specs: %s", b)
	}
}

// driveToolCall runs the server over a single `search` tool call and returns the
// text content of its reply, with the given readiness gate installed (nil = none).
func driveToolCall(t *testing.T, ready func() (bool, string)) string {
	t.Helper()
	store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := query.NewEngine(store, "proj", t.TempDir())

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"foo"}}}` + "\n"
	var out bytes.Buffer
	srv := NewServer(eng, strings.NewReader(req), &out)
	if ready != nil {
		srv.SetReadiness(ready)
	}
	if err := srv.Serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("bad response %q: %v", out.String(), err)
	}
	if len(resp.Result.Content) == 0 {
		return ""
	}
	return resp.Result.Content[0].Text
}

// stubEngine is a canned QueryEngine for dispatch tests: one ref page plus a
// configurable similarity notice.
type stubEngine struct {
	page   query.RefPage
	notice string
}

func (s stubEngine) SearchPage(q, label string, limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) CallersPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) CalleesPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) NeighborsPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) SimilarPage(qn string, limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) DeadCodePage(limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
func (s stubEngine) SimilarNotice() string { return s.notice }
func (s stubEngine) Architecture(topN int) (query.Architecture, error) {
	return query.Architecture{}, nil
}
func (s stubEngine) SnippetPage(file string, start, end, limit int, cursor string) (query.SnippetPage, error) {
	return query.SnippetPage{}, nil
}
func (s stubEngine) DetectChanges() (index.Changes, error) {
	return index.Changes{}, nil
}

func driveNamedToolCall(t *testing.T, eng QueryEngine, name, args string) string {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + strconv.Quote(name) + `,"arguments":` + args + `}}` + "\n"
	var out bytes.Buffer
	srv := NewServer(eng, strings.NewReader(req), &out)
	if err := srv.Serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("bad response %q: %v", out.String(), err)
	}
	if len(resp.Result.Content) == 0 {
		return ""
	}
	return resp.Result.Content[0].Text
}

// TestServer_SimilarPrependsCoverageNotice pins P4's query surface: a partial
// clone index prepends actionable context to `similar` answers (an incomplete
// page must not read as "no clones"), while other tools stay bare.
func TestServer_SimilarPrependsCoverageNotice(t *testing.T) {
	eng := stubEngine{
		page:   query.RefPage{Cursor: "-", Generation: "abc"},
		notice: "similarity index partial (docs=10): clone answers may miss pairs",
	}
	got := driveNamedToolCall(t, eng, "similar", `{"qualified_name":"a"}`)
	if !strings.HasPrefix(got, "similarity index partial") {
		t.Errorf("similar must prepend the coverage notice, got %q", got)
	}
	got = driveNamedToolCall(t, eng, "callers", `{"qualified_name":"a"}`)
	if strings.HasPrefix(got, "similarity index partial") {
		t.Errorf("non-similar tools must not carry the notice, got %q", got)
	}
	bare := stubEngine{page: query.RefPage{Cursor: "-", Generation: "abc"}}
	got = driveNamedToolCall(t, bare, "similar", `{"qualified_name":"a"}`)
	if strings.Contains(got, "similarity index") {
		t.Errorf("complete coverage must stay silent, got %q", got)
	}
}

// background index is still building, a tool call returns a human "indexing" status
// instead of querying a half-built store; once ready, the call serves normally
// (here: an empty search result, i.e. not the status message).
// TestServer_GatesToolCallsUntilIndexed pins the auto-index gate: while the
func TestServer_GatesToolCallsUntilIndexed(t *testing.T) {
	const msg = "codegraph is indexing, retry shortly"

	if got := driveToolCall(t, func() (bool, string) { return false, msg }); got != msg {
		t.Errorf("not-ready tool call = %q, want the indexing status %q", got, msg)
	}
	// A ready gate with a notice (e.g. degraded/stale context) serves the query
	// with the notice prepended; with an empty notice the answer is bare.
	if got := driveToolCall(t, func() (bool, string) { return true, msg }); !strings.HasPrefix(got, msg) {
		t.Errorf("ready tool call with notice must prepend it; got %q", got)
	}
}

// TestServer_SurfacesFailureStatusWhenReady pins the post-failure MCP path: when
// background indexing fails but the previous graph was reopened, tools stay ready
// and prepend the failure status so agents see stale-data context with results.
func TestServer_SurfacesFailureStatusWhenReady(t *testing.T) {
	const failMsg = "codegraph: indexing testproj failed: boom"
	got := driveToolCall(t, func() (bool, string) { return true, failMsg })
	if !strings.HasPrefix(got, failMsg) {
		t.Errorf("ready+failed status must prepend failure message; got %q", got)
	}
}

// TestServer_ExtraToolsRegistry pins the P1 host-tool seam: registered tools
// appear in tools/list with a valid schema, ungated ones answer while the gate
// reports not-ready, gated ones observe the gate, and collisions are rejected.
func TestServer_ExtraToolsRegistry(t *testing.T) {
	newSrv := func(ready func() (bool, string)) (*Server, *bytes.Buffer) {
		store, err := graph.Open(filepath.Join(t.TempDir(), "g.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		eng := query.NewEngine(store, "proj", t.TempDir())
		var out bytes.Buffer
		srv := NewServer(eng, strings.NewReader(""), &out)
		if ready != nil {
			srv.SetReadiness(ready)
		}
		return srv, &out
	}
	call := func(srv *Server, out *bytes.Buffer, name string) string {
		out.Reset()
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + strconv.Quote(name) + `,"arguments":{}}}` + "\n"
		srv.in = bufio.NewScanner(strings.NewReader(req))
		if err := srv.Serve(); err != nil {
			t.Fatalf("serve: %v", err)
		}
		var resp struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
			t.Fatalf("bad response %q: %v", out.String(), err)
		}
		if len(resp.Result.Content) == 0 {
			return ""
		}
		return resp.Result.Content[0].Text
	}

	down := func() (bool, string) { return false, "updating; retry shortly" }
	srv, out := newSrv(down)
	if err := srv.RegisterTool("refresh", "reindex", map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
		return "started", nil
	}); err != nil {
		t.Fatalf("register refresh: %v", err)
	}
	if err := srv.RegisterTool("status", "state", map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
		return "state=ready", nil
	}); err != nil {
		t.Fatalf("register status: %v", err)
	}
	if got := call(srv, out, "refresh"); got != "started" {
		t.Errorf("ungated tool while not-ready = %q, want %q", got, "started")
	}
	if err := srv.RegisterTool("gated-op", "g", map[string]any{}, nil, false, func(args json.RawMessage) (string, error) {
		return "ran", nil
	}); err != nil {
		t.Fatalf("register gated: %v", err)
	}
	if got := call(srv, out, "gated-op"); got != "updating; retry shortly" {
		t.Errorf("gated tool while not-ready = %q, want the gate message", got)
	}
	if err := srv.RegisterTool("search", "collision", map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
		return "", nil
	}); err == nil {
		t.Errorf("built-in name collision was not rejected")
	}
	if err := srv.RegisterTool("refresh", "dup", map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
		return "", nil
	}); err == nil {
		t.Errorf("duplicate registration was not rejected")
	}

	specs := srv.toolSpecs()
	names := map[string]bool{}
	for _, sp := range specs {
		name, _ := sp["name"].(string)
		names[name] = true
	}
	for _, want := range []string{"search", "refresh", "status", "gated-op"} {
		if !names[want] {
			t.Errorf("tools/list misses %q", want)
		}
	}
}

// TestServer_OmitsEmptyReadyStatus: a clean ready gate (empty notice) serves
// the query bare — notices are session-owned (P1 states); the server prepends
// whatever non-empty notice the gate returns and nothing else. "Bare" means
// the page wire format with no notice prefix: empty refs plus the `#` trailer.
func TestServer_OmitsEmptyReadyStatus(t *testing.T) {
	// Empty store + empty notice: the query is served (empty result), bare.
	const want = "# has_more=false cursor=- generation=none shown=0\n"
	if got := driveToolCall(t, func() (bool, string) { return true, "" }); got != want {
		t.Errorf("clean ready gate must serve the bare page, got %q want %q", got, want)
	}
}
