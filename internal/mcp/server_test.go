package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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
	page := s.page
	page.Notice = s.notice
	return page, nil
}
func (s stubEngine) DeadCodePage(limit int, cursor string) (query.RefPage, error) {
	return s.page, nil
}
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

func TestServer_RejectsMalformedToolArguments(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":[]}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"limit":"many"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"extra","arguments":null}}`,
	}
	for _, request := range cases {
		var out bytes.Buffer
		srv := NewServer(stubEngine{}, strings.NewReader(request+"\n"), &out)
		called := false
		if err := srv.RegisterTool("extra", "test", nil, nil, true, func(json.RawMessage) (string, error) {
			called = true
			return "unexpected", nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := srv.Serve(); err != nil {
			t.Fatal(err)
		}
		var response rpcResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error == nil || response.Error.Code != -32602 || called {
			t.Errorf("request %s: error=%+v, extra called=%v", request, response.Error, called)
		}
	}
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

// TestServer_ParseErrorIsAnswered pins the JSON-RPC contract for a malformed line:
// the server answers with a -32700 parse error (null id) and keeps serving instead
// of only logging to stderr.
func TestServer_ParseErrorIsAnswered(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(stubEngine{}, strings.NewReader("{not json\n{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/list\"}\n"), &out)
	if err := srv.Serve(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses=%q, want a parse error then the tools/list reply", out.String())
	}
	var first rpcResponse
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Error == nil || first.Error.Code != -32700 {
		t.Fatalf("first response=%+v, want parse error -32700", first)
	}
	if string(first.ID) != "null" {
		t.Fatalf("parse error id=%s, want null", first.ID)
	}
	var second rpcResponse
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second.Error != nil {
		t.Fatalf("stream did not recover after a bad line: %+v", second)
	}
}

// TestServer_NotificationGetsNoReply pins that a request without an id is a
// JSON-RPC notification and must not receive a response, including an unknown
// method.
func TestServer_NotificationGetsNoReply(t *testing.T) {
	var out bytes.Buffer
	srv := NewServer(stubEngine{}, strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"method\":\"tools/list\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"no/such/method\"}\n"), &out)
	if err := srv.Serve(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notifications must not be answered, got %q", out.String())
	}
}

// TestServer_RequiresToolArguments pins typed per-tool validation: a missing
// required field is -32602, never a meaningless empty answer.
func TestServer_RequiresToolArguments(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"search", `{}`},
		{"search", `{"query":"   "}`},
		{"callers", `{}`},
		{"callees", `{"qualified_name":"  "}`},
		{"neighbors", `{}`},
		{"similar", `{}`},
		{"snippet", `{}`},
	} {
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + strconv.Quote(tc.name) + `,"arguments":` + tc.args + `}}` + "\n"
		var out bytes.Buffer
		srv := NewServer(stubEngine{}, strings.NewReader(req), &out)
		if err := srv.Serve(); err != nil {
			t.Fatal(err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Errorf("%s %s: error=%+v, want -32602", tc.name, tc.args, resp.Error)
		}
	}
}

// TestServer_WriteFailureStopsTheLoop pins transport failure propagation: a write
// that fails ends Serve with an error instead of silently dropping answers.
func TestServer_WriteFailureStopsTheLoop(t *testing.T) {
	srv := NewServer(stubEngine{}, strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n"), failingWriter{})
	if err := srv.Serve(); err == nil {
		t.Fatal("a failed write must be returned, not swallowed")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("writer closed") }

// TestServer_NegotiatesProtocolVersion pins the initialize handshake: a supported
// client version is echoed; an unsupported or absent one gets the server default.
func TestServer_NegotiatesProtocolVersion(t *testing.T) {
	for _, tc := range []struct{ request, want string }{
		{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, "2025-03-26"},
		{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`, defaultProtocolVersion},
		{`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, defaultProtocolVersion},
	} {
		var out bytes.Buffer
		srv := NewServer(stubEngine{}, strings.NewReader(tc.request+"\n"), &out)
		if err := srv.Serve(); err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Result.ProtocolVersion != tc.want {
			t.Errorf("request %s: version=%q, want %q", tc.request, resp.Result.ProtocolVersion, tc.want)
		}
	}
}
