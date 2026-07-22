package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestRun_RubyCalls_AbsoluteConstantSingletonMethod(t *testing.T) {
	dir := t.TempDir()
	writeRubySource(t, dir, "lib/gateway.rb", `class Gateway
  def self.authorize
  end
end
`)
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def process
    ::Gateway.authorize
  end
end
`)

	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	callers, err := store.Neighbors(project, project+":lib/checkout.rb.Checkout#process", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 {
		t.Fatalf("Ruby CALLS = %#v, want one target", callers)
	}
	if got, want := callers[0].QualifiedName, project+":lib/gateway.rb.Gateway.authorize"; got != want {
		t.Errorf("Ruby CALLS target = %q, want %q", got, want)
	}

	var props map[string]any
	if err := store.ForEachCallEdge(project, func(edge graph.CallEdge) error {
		if edge.SourceQN == project+":lib/checkout.rb.Checkout#process" {
			props = edge.Props
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if props["resolver"] != "ruby-static" || props["confidence"] != "high" || props["evidence"] != "absolute_constant_receiver" {
		t.Errorf("Ruby CALLS provenance = %#v", props)
	}

	result, err := Run(store, dir)
	if err != nil {
		t.Fatalf("unchanged run: %v", err)
	}
	if !result.Reused {
		t.Error("Ruby graph at the current static-call version must be reusable")
	}
}

func TestRun_RubyCalls_DropsNonAbsoluteAndAmbiguousTargets(t *testing.T) {
	dir := t.TempDir()
	writeRubySource(t, dir, "lib/gateway_a.rb", `class Gateway
  def self.authorize
  end
end
`)
	writeRubySource(t, dir, "lib/gateway_b.rb", `class Gateway
  def self.authorize
  end
end
`)
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def authorize
  end

  def process(gateway)
    Gateway.authorize
    gateway.authorize
    self.authorize
    send(:authorize)
    ::Gateway.authorize
  end
end
`)

	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := store.Neighbors(project, project+":lib/checkout.rb.Checkout#process", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("Ruby resolver emitted unsafe CALLS: %#v", calls)
	}
}

func TestRun_RubyCalls_DropsLexicalSingletonDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeRubySource(t, dir, "lib/nested_gateway.rb", `class Outer
  class Gateway
    def Gateway.authorize
    end
  end
end
`)
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def process
    ::Gateway.authorize
  end
end
`)

	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := store.Neighbors(project, project+":lib/checkout.rb.Checkout#process", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("lexical singleton definition must not resolve ::Gateway: %#v", calls)
	}
}

func TestRun_RubyCalls_DropsLexicalSingletonClassDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeRubySource(t, dir, "lib/nested_gateway.rb", `class Outer
  class Gateway
  end

  class << Gateway
    def authorize
    end
  end
end
`)
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def process
    ::Gateway.authorize
  end
end
`)

	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls, err := store.Neighbors(project, project+":lib/checkout.rb.Checkout#process", "out", string(graph.EdgeCalls), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("lexical singleton class must not resolve ::Gateway: %#v", calls)
	}
}

func TestRun_RubyCalls_ReusesOtherScopesAndReplacesRubyScope(t *testing.T) {
	dir := t.TempDir()
	writeRubySource(t, dir, "go.mod", "module example.test/ruby-calls\n\ngo 1.26\n")
	writeRubySource(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeRubySource(t, dir, "lib/gateway.rb", `class Gateway
  def self.authorize
  end
end
`)
	writeRubySource(t, dir, "lib/gateway_two.rb", `class GatewayTwo
  def self.authorize
  end
end
`)
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def process
    ::Gateway.authorize
  end
end
`)

	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	caller := project + ":lib/checkout.rb.Checkout#process"
	gateway := project + ":lib/gateway.rb.Gateway.authorize"
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run1: %v", err)
	}
	if _, _, err := store.InsertEdges([]graph.Edge{{
		Project: project, SourceQN: gateway, TargetQN: caller, Type: graph.EdgeCalls,
	}}); err != nil {
		t.Fatal(err)
	}

	// A Go-only edit must reuse every Ruby CALLS edge, including this resolver-impossible sentinel.
	writeRubySource(t, dir, "main.go", "package main\n\nfunc main() { _ = 1 }\n")
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run after Go edit: %v", err)
	}
	if !hasRubyCall(t, store, project, gateway, caller) {
		t.Error("Go-only edit must reuse the unchanged Ruby CALLS scope")
	}

	// A Ruby edit must re-resolve that scope, replace its old calls, and remove the sentinel.
	writeRubySource(t, dir, "lib/checkout.rb", `class Checkout
  def process
    ::GatewayTwo.authorize
  end
end
`)
	if _, err := Run(store, dir); err != nil {
		t.Fatalf("run after Ruby edit: %v", err)
	}
	gatewayTwo := project + ":lib/gateway_two.rb.GatewayTwo.authorize"
	if !hasRubyCall(t, store, project, caller, gatewayTwo) {
		t.Error("Ruby edit must resolve the replacement absolute-constant call")
	}
	if hasRubyCall(t, store, project, caller, gateway) || hasRubyCall(t, store, project, gateway, caller) {
		t.Error("Ruby edit must not retain old or sentinel Ruby CALLS edges")
	}
}

func TestPrepareIndexing_UpgradesLegacyRubyStaticCalls(t *testing.T) {
	dir := t.TempDir()
	source := []byte("class Gateway\n  def self.authorize\n  end\nend\n")
	writeRubySource(t, dir, "lib/gateway.rb", string(source))
	store, err := graph.Open(filepath.Join(dir, "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := ProjectName(dir)
	nodes, edges := extractDefsFromSource(project, "lib/gateway.rb", LangRuby, source)
	delete(nodes[0].Props, "ruby_static_calls_version") // simulate the structural-only release
	if err := store.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertEdges(edges); err != nil {
		t.Fatal(err)
	}

	in, reused, err := prepareIndexing(store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if reused != nil {
		t.Fatal("legacy Ruby graph must not take the no-op path")
	}
	if !in.changed["ruby"] {
		t.Errorf("legacy Ruby graph changed scopes = %v, want ruby", in.changed)
	}
}

func TestRubyCallTargetUniqueness(t *testing.T) {
	targets := uniqueRubyCallTargets([]graph.RubyCallTarget{
		{Owner: "Gateway", Name: "authorize", QualifiedName: "p:a.rb.Gateway.authorize"},
		{Owner: "Gateway", Name: "authorize", QualifiedName: "p:b.rb.Gateway.authorize"},
		{Owner: "Billing::Gateway", Name: "authorize", QualifiedName: "p:c.rb.Billing::Gateway.authorize"},
	})
	if got := targets["Gateway\x00authorize"]; got != "" {
		t.Errorf("ambiguous target = %q, want dropped", got)
	}
	if got, want := targets["Billing::Gateway\x00authorize"], "p:c.rb.Billing::Gateway.authorize"; got != want {
		t.Errorf("unique target = %q, want %q", got, want)
	}
}

func writeRubySource(t *testing.T, root, rel, source string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasRubyCall(t *testing.T, store *graph.Store, project, sourceQN, targetQN string) bool {
	t.Helper()
	found := false
	if err := store.ForEachCallEdge(project, func(edge graph.CallEdge) error {
		if edge.SourceQN == sourceQN && edge.TargetQN == targetQN {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return found
}
