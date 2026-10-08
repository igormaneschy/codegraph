package index

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/Lordymine/codegraph/internal/graph"
)

// routes.go derives HTTP Route nodes from NestJS decorators (@Controller + @Get/
// @Post/...). The decorator capture in treesitter.go records (name, arg) pairs; here
// we join the controller base path with each handler's path into a Route node, placed
// at the handler method so snippet/search land on the handling code.

// decor is a captured decorator: its bare name and path argument. The argument
// is tri-state (routeArg) because "" alone cannot distinguish @Get()/@Controller()
// (valid: the root) from @Get(PATH) (statically unresolvable).
type decor struct {
	name string
	arg  routeArg
}

// routeArg is a decorator's path argument in one of three states: absent
// (@Get()), a single string literal (@Get('users')), or unknown (@Get(PATH),
// arrays, interpolation, or multiple arguments). Only absent and literal may
// produce a Route: an unknown argument is omitted, never guessed as "/".
type routeArg struct {
	kind    routeArgKind
	literal string
}

type routeArgKind uint8

const (
	routeArgAbsent routeArgKind = iota
	routeArgLiteral
	routeArgUnknown
)

// httpVerbs maps NestJS method decorators to their HTTP verb.
var httpVerbs = map[string]string{
	"Get": "GET", "Post": "POST", "Put": "PUT", "Delete": "DELETE",
	"Patch": "PATCH", "Options": "OPTIONS", "Head": "HEAD", "All": "ALL",
}

// emitRoutes emits one Route node per HTTP-verb decorator on a handler method, but
// only inside a class that is itself a @Controller and only when both the
// controller base and the handler path are known. An unresolvable argument
// (@Controller(BASE), @Get(PATH), arrays) omits the Route instead of inventing
// "GET /". The route's location is the handler method's, and add() wires the
// file→route DEFINES edge.
func emitRoutes(pending []decor, isController bool, base routeArg, methodQN string, m *tree_sitter.Node, add addFn) {
	if !isController || base.kind == routeArgUnknown {
		return
	}
	for _, d := range pending {
		verb, ok := httpVerbs[d.name]
		if !ok || d.arg.kind == routeArgUnknown {
			continue
		}
		path := joinRoute(base.literal, d.arg.literal)
		add(graph.LabelRoute, verb+" "+path, methodQN+"#"+verb,
			m.StartPosition().Row, m.EndPosition().Row,
			map[string]any{"method": verb, "path": path, "handler": methodQN})
	}
}

// joinRoute joins a controller base path and a handler sub-path into a normalized
// "/a/b" route, dropping empty segments and stray slashes.
func joinRoute(base, sub string) string {
	var parts []string
	for _, p := range []string{base, sub} {
		if p = strings.Trim(p, "/"); p != "" {
			parts = append(parts, p)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// decorNames extracts the bare decorator names (the method node's `decorators` prop
// keeps the existing shape — names only).
func decorNames(ds []decor) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.name)
	}
	return out
}

// controllerArg returns (isController, basePath) for a class's decorators.
func controllerArg(ds []decor) (bool, routeArg) {
	for _, d := range ds {
		if d.name == "Controller" {
			return true, d.arg
		}
	}
	return false, routeArg{}
}

// decoratorPath classifies a decorator's path argument without guessing.
// (@Controller('users') -> literal "users", @Get() -> absent, @Get(PATH) or
// @Get(['a','b']) -> unknown.)
func decoratorPath(d *tree_sitter.Node, src []byte) routeArg {
	var call *tree_sitter.Node
	for i := uint(0); i < d.NamedChildCount(); i++ {
		if c := d.NamedChild(i); c.Kind() == "call_expression" {
			call = c
			break
		}
	}
	if call == nil {
		return routeArg{kind: routeArgUnknown}
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return routeArg{kind: routeArgUnknown}
	}
	var only *tree_sitter.Node
	count := uint(0)
	for i := uint(0); i < args.NamedChildCount(); i++ {
		child := args.NamedChild(i)
		if child.Kind() == "comment" {
			continue
		}
		count++
		only = child
	}
	switch {
	case count == 0:
		return routeArg{kind: routeArgAbsent}
	case count == 1 && only.Kind() == "string":
		return routeArg{kind: routeArgLiteral, literal: stringFragment(only, src)}
	default:
		return routeArg{kind: routeArgUnknown}
	}
}

// stringFragment returns the content of a tree-sitter `string` node (without quotes);
// an empty literal (”) has no fragment child, so it returns "".
func stringFragment(n *tree_sitter.Node, src []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Kind() == "string_fragment" {
			return c.Utf8Text(src)
		}
	}
	return ""
}
