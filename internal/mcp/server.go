// Package mcp implements a minimal Model Context Protocol server over stdio.
//
// Transport: newline-delimited JSON-RPC 2.0 (the MCP stdio convention — one
// JSON message per line). This is deliberately dependency-free; if it grows,
// swap in github.com/mark3labs/mcp-go. It exposes the query tools so a coding
// agent (Claude Code etc.) can drive the graph. See docs/ARCHITECTURE.md.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// QueryEngine is the query surface the server exposes. *query.Engine satisfies
// it; hosts may wrap it (e.g. with a lifetime guard) without touching dispatch.
type QueryEngine interface {
	SearchPage(q, label string, limit int, cursor string) (query.RefPage, error)
	CallersPage(qualifiedName string, limit int, cursor string) (query.RefPage, error)
	CalleesPage(qualifiedName string, limit int, cursor string) (query.RefPage, error)
	NeighborsPage(qualifiedName string, limit int, cursor string) (query.RefPage, error)
	SimilarPage(qualifiedName string, limit int, cursor string) (query.RefPage, error)
	DeadCodePage(limit int, cursor string) (query.RefPage, error)
	Architecture(topN int) (query.Architecture, error)
	SnippetPage(filePath string, start, end, limit int, cursor string) (query.SnippetPage, error)
	DetectChanges() (index.Changes, error)
}

// Server serves MCP over the given streams using a query engine.
type Server struct {
	eng   QueryEngine
	in    *bufio.Scanner
	out   *json.Encoder
	ready func() (bool, string) // nil = always ready; see SetReadiness
	extra []extraTool
}

// extraTool is a host-registered tool (e.g. refresh/status): schema plus a
// handler returning the text content. Ungated tools answer even while the
// readiness gate reports not-ready, so handshake-adjacent operations stay
// responsive during an update.
type extraTool struct {
	name        string
	description string
	properties  map[string]any
	required    []string
	ungated     bool
	handler     func(args json.RawMessage) (string, error)
}

func NewServer(eng QueryEngine, in io.Reader, out io.Writer) *Server {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	return &Server{eng: eng, in: sc, out: json.NewEncoder(out)}
}

// RegisterTool adds a host-owned tool (e.g. refresh/status) to tools/list and
// dispatch. The handler receives the raw arguments object and returns the text
// content. When ungated is true the tool answers even while the readiness gate
// reports not-ready — reserved for operations that must stay responsive during
// an update. Names colliding with built-ins are rejected.
func (s *Server) RegisterTool(name, description string, properties map[string]any, required []string, ungated bool, handler func(args json.RawMessage) (string, error)) error {
	if handler == nil {
		return fmt.Errorf("mcp: tool %q has no handler", name)
	}
	switch name {
	case "search", "callers", "callees", "neighbors", "similar", "dead_code",
		"get_architecture", "snippet", "detect_changes":
		return fmt.Errorf("mcp: tool %q is built in", name)
	}
	for _, e := range s.extra {
		if e.name == name {
			return fmt.Errorf("mcp: tool %q already registered", name)
		}
	}
	if required == nil {
		required = []string{}
	}
	s.extra = append(s.extra, extraTool{name: name, description: description,
		properties: properties, required: required, ungated: ungated, handler: handler})
	return nil
}

// SetReadiness installs a gate consulted before every tool call. While it reports
// not-ready, tool calls return its status message instead of querying — so a repo
// whose background index is still building (or rebuilding) reports "indexing"
// rather than answering from a half-built store. initialize/tools/list are never
// gated, so the agent still sees the server and its tools immediately.
func (s *Server) SetReadiness(fn func() (bool, string)) { s.ready = fn }

// defaultProtocolVersion is answered when the client requests a version this
// server does not support; supportedProtocolVersions lists what it can serve. Only
// the tools/stdio surface is used, which is stable across these revisions; we never
// advertise capabilities we do not implement.
const defaultProtocolVersion = "2024-11-05"

var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
}

// Serve runs the request loop until stdin closes. A malformed line is answered
// with a JSON-RPC parse error and the stream continues; a write failure ends the
// loop (the transport is gone, so silently swallowing it would leave the client
// waiting forever).
func (s *Server) Serve() error {
	for s.in.Scan() {
		line := s.in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if werr := s.fail(nil, -32700, "parse error: "+err.Error()); werr != nil {
				return werr
			}
			continue
		}
		if err := s.handle(req); err != nil {
			return err
		}
	}
	return s.in.Err()
}

func (s *Server) reply(id json.RawMessage, result any) error {
	return s.out.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}
func (s *Server) fail(id json.RawMessage, code int, msg string) error {
	return s.out.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// respond sends a result for a request and stays silent for a notification.
func (s *Server) respond(id json.RawMessage, notification bool, result any) error {
	if notification {
		return nil
	}
	return s.reply(id, result)
}

// handle dispatches one request. A request with no id is a JSON-RPC notification
// and never receives a response. A non-nil error is a transport failure and stops
// the loop.
func (s *Server) handle(req rpcRequest) error {
	notification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		return s.respond(req.ID, notification, s.initializeResult(req.Params))
	case "notifications/initialized":
		return nil // notification: no response by design
	case "tools/list":
		return s.respond(req.ID, notification, map[string]any{"tools": s.toolSpecs()})
	case "tools/call":
		return s.callTool(req, notification)
	default:
		if notification {
			return nil // unknown notifications must not be answered
		}
		return s.fail(req.ID, -32601, "method not found: "+req.Method)
	}
}

// initializeResult negotiates the protocol version: echo the client's version when
// it is one this server supports, otherwise answer with the supported default (the
// MCP handshake lets the server choose the version it will use).
func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := defaultProtocolVersion
	var client struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &client) == nil && supportedProtocolVersions[client.ProtocolVersion] {
		version = client.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "codegraph", "version": "0.0.1"},
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolArgs is the union of the built-in tools' parameters.
type toolArgs struct {
	Query         string `json:"query"`
	Label         string `json:"label"`
	QualifiedName string `json:"qualified_name"`
	File          string `json:"file"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Limit         int    `json:"limit"`
	Cursor        string `json:"cursor"`
}

// validateToolArgs rejects a call whose required field is empty before any query
// runs, so a missing qualified_name/file/query is a parameter error rather than a
// meaningless empty answer.
func validateToolArgs(name string, args toolArgs) error {
	switch name {
	case "search":
		if strings.TrimSpace(args.Query) == "" {
			return fmt.Errorf("search requires a non-empty `query`")
		}
	case "callers", "callees", "neighbors", "similar":
		if strings.TrimSpace(args.QualifiedName) == "" {
			return fmt.Errorf("%s requires a non-empty `qualified_name`", name)
		}
	case "snippet":
		if strings.TrimSpace(args.File) == "" {
			return fmt.Errorf("snippet requires a non-empty `file`")
		}
	}
	return nil
}

func (s *Server) callTool(req rpcRequest, notification bool) error {
	failTool := func(code int, msg string) error {
		if notification {
			return nil
		}
		return s.fail(req.ID, code, msg)
	}
	var p toolCallParams
	if !jsonObject(req.Params) || json.Unmarshal(req.Params, &p) != nil || p.Name == "" {
		return failTool(-32602, "tools/call params must be an object with a tool name")
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage(`{}`)
	}
	if !jsonObject(p.Arguments) {
		return failTool(-32602, "tool arguments must be a JSON object")
	}
	if text, err, handled := s.callExtra(p, req); handled {
		if err != nil {
			return failTool(-32000, err.Error())
		}
		return s.respond(req.ID, notification, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		})
	}
	var notice string
	if s.ready != nil {
		ok, msg := s.ready()
		if !ok {
			return s.respond(req.ID, notification, map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}})
		}
		notice = msg
	}
	var args toolArgs
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		return failTool(-32602, "invalid tool arguments: "+err.Error())
	}
	if err := validateToolArgs(p.Name, args); err != nil {
		return failTool(-32602, err.Error())
	}

	// Ref/snippet tools emit one page plus a `#` trailer line carrying
	// has_more + cursor + generation, so a truncated answer never looks
	// exhaustive. Pass the trailer's cursor back as `cursor` for the next
	// page. We never JSON-wrap the result — that wrapper is exactly the token
	// overhead the compact format exists to avoid.
	var (
		text string
		err  error
	)
	pageText := func(p query.RefPage, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return p.WireText(), nil
	}
	switch p.Name {
	case "search":
		text, err = pageText(s.eng.SearchPage(args.Query, args.Label, args.Limit, args.Cursor))
	case "callers":
		text, err = pageText(s.eng.CallersPage(args.QualifiedName, args.Limit, args.Cursor))
	case "callees":
		text, err = pageText(s.eng.CalleesPage(args.QualifiedName, args.Limit, args.Cursor))
	case "neighbors":
		text, err = pageText(s.eng.NeighborsPage(args.QualifiedName, args.Limit, args.Cursor))
	case "similar":
		var page query.RefPage
		page, err = s.eng.SimilarPage(args.QualifiedName, args.Limit, args.Cursor)
		if err == nil {
			text = page.WireText()
			if page.Notice != "" {
				text = page.Notice + "\n\n" + text
			}
		}
	case "dead_code":
		text, err = pageText(s.eng.DeadCodePage(args.Limit, args.Cursor))
	case "get_architecture":
		var arch query.Architecture
		arch, err = s.eng.Architecture(args.Limit)
		if err == nil {
			text = query.RenderArchitecture(arch)
		}
	case "snippet":
		var pg query.SnippetPage
		pg, err = s.eng.SnippetPage(args.File, args.StartLine, args.EndLine, args.Limit, args.Cursor)
		if err == nil {
			text = pg.WireText()
		}
	case "detect_changes":
		ch, derr := s.eng.DetectChanges()
		if err = derr; err == nil {
			if text = ch.Summary(); text == "" {
				text = "no source or recorded-config changes since the last index (environment identity is not compared; run index to verify)"
			}
		}
	default:
		return failTool(-32602, "unknown tool: "+p.Name)
	}
	if err != nil {
		return failTool(-32000, err.Error())
	}
	if notice != "" {
		if text != "" {
			text = notice + "\n\n" + text
		} else {
			text = notice
		}
	}
	return s.respond(req.ID, notification, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

func jsonObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// callExtra dispatches host-registered tools. Ungated tools bypass the
// readiness gate; gated ones observe it like built-ins. It returns
// handled=false for built-in names.
func (s *Server) callExtra(p toolCallParams, req rpcRequest) (string, error, bool) {
	for _, e := range s.extra {
		if e.name != p.Name {
			continue
		}
		if !e.ungated && s.ready != nil {
			if ok, msg := s.ready(); !ok {
				return msg, nil, true
			}
		}
		text, err := e.handler(p.Arguments)
		return text, err, true
	}
	return "", nil, false
}

func (s *Server) toolSpecs() []map[string]any {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	spec := func(name, desc string, props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{} // JSON Schema `required` must be an array, never null
		}
		return map[string]any{
			"name": name, "description": desc,
			"inputSchema": map[string]any{"type": "object", "properties": props, "required": required},
		}
	}
	out := []map[string]any{
		spec("search", "Ranked BM25 symbol search. Returns one page of compact refs (one TSV line per hit: label<TAB>name<TAB>file:line<TAB>qualified_name) plus a `#` trailer line with has_more/cursor/generation. Pass a returned qualified_name straight to callers/callees. Default page 500 refs / 32 KiB (bytes prevail); pass the trailer cursor back as `cursor` for the next page; cursors bind to the query and graph generation.",
			map[string]any{"query": str, "label": str, "limit": num, "cursor": str}, "query"),
		spec("callers", "Inbound references to a symbol (who uses it). One page of TSV refs (see search) plus the `#` has_more/cursor/generation trailer — enumerate every page for a complete answer. Accepts a qualified_name with or without the project prefix.",
			map[string]any{"qualified_name": str, "limit": num, "cursor": str}, "qualified_name"),
		spec("callees", "Outbound references from a symbol (what it uses). One page of TSV refs (see search) plus the `#` has_more/cursor/generation trailer.",
			map[string]any{"qualified_name": str, "limit": num, "cursor": str}, "qualified_name"),
		spec("neighbors", "Both inbound and outbound neighbors of a symbol. One page of TSV refs (see search) plus the `#` has_more/cursor/generation trailer.",
			map[string]any{"qualified_name": str, "limit": num, "cursor": str}, "qualified_name"),
		spec("similar", "Near-clone symbols of this one (SIMILAR_TO edges from MinHash/LSH). Surfaces copy-paste/duplicated logic to refactor. One page of TSV refs (see search) plus the `#` has_more/cursor/generation trailer.",
			map[string]any{"qualified_name": str, "limit": num, "cursor": str}, "qualified_name"),
		spec("dead_code", "CANDIDATES for unused private functions/methods: zero inbound CALLS, excluding entry points (exported, decorated, main/init, tests). NOT a delete list — a caller the resolver missed or an indirect reference (function value, interface, reflection) makes a live function look dead, so confirm each (e.g. grep the name) before acting. One page of TSV refs (see search) plus the `#` has_more/cursor/generation trailer.",
			map[string]any{"limit": num, "cursor": str}),
		spec("get_architecture", "One-shot repo map from the graph: languages, node/edge counts, top packages by symbol count, and hotspots (most complex functions + most-called hubs). Call this FIRST to orient in an unfamiliar repo instead of grepping. `limit` caps each top-N list (default 10).",
			map[string]any{"limit": num}),
		spec("snippet", "Read one page of source lines for a node (default 200 lines / 32 KiB, bytes prevail) plus a `#` trailer with has_more/cursor/generation and the covered line range. A single oversize line comes back whole, marked long_line=true. Pass the trailer cursor back as `cursor` to continue; the file must not change between pages. Use only when you must see code.",
			map[string]any{"file": str, "start_line": num, "end_line": num, "limit": num, "cursor": str}, "file"),
		spec("detect_changes", "List source files changed/added/deleted since the last index, plus `config<TAB>path` lines for recorded sidecar inputs (tsconfig, go.mod/go.work, ignore/workspace config) whose bytes changed. TSV: status<TAB>path. Empty = no source or recorded-config change (environment identity, e.g. GOFLAGS, is not compared — run index to verify). Check it before trusting the graph for a region; re-index if stale.",
			map[string]any{}),
	}
	for _, e := range s.extra {
		props := e.properties
		if props == nil {
			props = map[string]any{}
		}
		out = append(out, spec(e.name, e.description, props, e.required...))
	}
	return out
}

var _ = fmt.Sprintf // reserved for future structured logging
