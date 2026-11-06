package index

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/Lordymine/codegraph/internal/graph"
)

// pythonAnalysisVersion is incremented whenever the Python definitions or
// import analysis changes its output for unchanged source. It forces one full
// Python rebuild so existing repositories receive the new analysis without an
// edit — the same mechanism as Ruby's ruby_analysis_version.
const pythonAnalysisVersion = 1

// walkPyDefs extracts declarations that are explicit in Python source:
// module- and class-level functions (async included), classes (nested
// included), their methods, and UPPER_SNAKE single-target assignments (the
// Python convention for constants, at module and class scope). Nested
// functions, lambdas, and framework semantics (routes from decorators) stay
// out of scope: codegraph indexes the language, not frameworks.
func walkPyDefs(root *tree_sitter.Node, src []byte, add addFn) {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		pyWalkScopeElement(root.NamedChild(i), src, "", add)
	}
}

// pyWalkScopeElement handles one statement position at module or class scope.
// It descends through conditional/try/with wrappers (an `if TYPE_CHECKING:`
// guard or a dual-import try block is a real declaration), but function bodies
// are emitted by their own case and never descended into: a def nested inside
// a function is runtime-composed, not a stable module member.
func pyWalkScopeElement(n *tree_sitter.Node, src []byte, owner string, add addFn) {
	switch n.Kind() {
	case "function_definition":
		pyEmitFunction(n, src, owner, nil, add)
	case "decorated_definition":
		def, decorators := pyUndecoratedDefinition(n, src)
		if def == nil {
			return
		}
		switch def.Kind() {
		case "function_definition":
			pyEmitFunction(def, src, owner, decorators, add)
		case "class_definition":
			pyEmitClass(def, src, owner, decorators, add)
		}
	case "class_definition":
		pyEmitClass(n, src, owner, nil, add)
	case "assignment":
		pyEmitConstant(n, src, owner, add)
	default:
		for i := uint(0); i < n.NamedChildCount(); i++ {
			pyWalkScopeElement(n.NamedChild(i), src, owner, add)
		}
	}
}

func pyEmitFunction(n *tree_sitter.Node, src []byte, owner string, decorators []string, add addFn) {
	name := pyNodeName(n.ChildByFieldName("name"), src)
	if name == "" {
		return
	}
	extra := map[string]any{"is_exported": pyExported(name), "complexity": pyCyclomatic(n)}
	label := graph.LabelFunction
	qualified := name
	if owner != "" {
		label = graph.LabelMethod
		qualified = owner + "." + name
		extra["owner"] = owner
	}
	if len(decorators) > 0 {
		extra["decorators"] = decorators
	}
	add(label, name, qualified, n.StartPosition().Row, n.EndPosition().Row, extra)
}

func pyEmitClass(n *tree_sitter.Node, src []byte, owner string, decorators []string, add addFn) {
	name := pyNodeName(n.ChildByFieldName("name"), src)
	if name == "" {
		return
	}
	qualified := name
	if owner != "" {
		qualified = owner + "." + name
	}
	extra := map[string]any{"is_exported": pyExported(name)}
	if len(decorators) > 0 {
		extra["decorators"] = decorators
	}
	add(graph.LabelClass, name, qualified, n.StartPosition().Row, n.EndPosition().Row, extra)
	if body := n.ChildByFieldName("body"); body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			pyWalkScopeElement(body.NamedChild(i), src, qualified, add)
		}
	}
}

func pyEmitConstant(n *tree_sitter.Node, src []byte, owner string, add addFn) {
	left := n.ChildByFieldName("left")
	if left == nil || left.Kind() != "identifier" {
		return // tuple targets and attribute assignments are not simple constants
	}
	name := pyNodeName(left, src)
	if !pyConstantName(name) {
		return
	}
	qualified := name
	if owner != "" {
		qualified = owner + "." + name
	}
	add(graph.LabelConstant, name, qualified, n.StartPosition().Row, n.EndPosition().Row,
		map[string]any{"is_exported": pyExported(name)})
}

// pyUndecoratedDefinition unwraps a decorated_definition node: it returns the
// inner function/class definition and the decorator names in source order
// (the definition is the last named child of the wrapper).
func pyUndecoratedDefinition(n *tree_sitter.Node, src []byte) (*tree_sitter.Node, []string) {
	var decorators []string
	var def *tree_sitter.Node
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "decorator":
			if name := pyDecoratorName(c, src); name != "" {
				decorators = append(decorators, name)
			}
		case "function_definition", "class_definition":
			def = c
		}
	}
	return def, decorators
}

// pyDecoratorName reduces @app.get("/x") / @property to "app.get" / "property".
// Like the TypeScript extractor it captures any decorator framework generically.
func pyDecoratorName(n *tree_sitter.Node, src []byte) string {
	text := pyNodeName(n, src)
	text = strings.TrimPrefix(text, "@")
	if cut := strings.IndexAny(text, "(\n"); cut >= 0 {
		text = text[:cut]
	}
	return strings.TrimSpace(text)
}

// pyExported applies Python's leading-underscore convention. Dunder names
// (__init__, __repr__, ...) are protocol entry points the runtime invokes, so
// they remain exported despite the double underscore.
func pyExported(name string) bool {
	if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
		return true
	}
	return !strings.HasPrefix(name, "_")
}

// pyConstantName accepts only SCREAMING_SNAKE_CASE identifiers, so incidental
// lowercase assignments never become graph constants.
func pyConstantName(name string) bool {
	if name == "" {
		return false
	}
	hasUpper := false
	for _, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			// digits are fine inside the name
		default:
			return false
		}
	}
	return hasUpper
}

func pyNodeName(n *tree_sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return n.Utf8Text(src)
}
