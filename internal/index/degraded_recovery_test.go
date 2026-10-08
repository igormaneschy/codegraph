package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestRunAtomic_DegradedRefreshRetriesAndRestoresEveryResolverScope(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/recovery\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", "package recovery\nfunc Run() { helper() }\nfunc helper() {}\n")
	writeSecurityFile(t, root, "lib/gateway.rb", "class Gateway\n def self.authorize\n end\nend\n")
	writeSecurityFile(t, root, "lib/checkout.rb", "class Checkout\n def process\n  ::Gateway.authorize\n end\nend\n")
	dbPath := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	original := goCallEdges
	t.Cleanup(func() { goCallEdges = original })
	available, invocations := false, 0
	goCallEdges = func(ctx context.Context, project, resolverRoot string, accept func(string) bool, environment []string) ([]graph.Edge, error) {
		invocations++
		if !available {
			return nil, errors.New("resolver temporarily unavailable")
		}
		return original(ctx, project, resolverRoot, accept, environment)
	}
	first, err := RunAtomic(dbPath, root)
	if err != nil || first.Status != StatusDegraded || first.Reused || invocations != 1 {
		t.Fatalf("first=%+v err=%v calls=%d, want degraded initial build", first, err, invocations)
	}
	project := ProjectName(root)
	before := digestOf(t, dbPath, project)
	assertStoredCallTargets(t, dbPath, project, "main.go.Run", nil)
	assertStoredCallTargets(t, dbPath, project, "lib/checkout.rb.Checkout#process", nil)
	_, err = RunAtomic(dbPath, root)
	var failure *ResolverFailure
	if !errors.As(err, &failure) || invocations != 2 || digestOf(t, dbPath, project) != before {
		t.Fatalf("failed retry err=%v calls=%d, want visible failure and unchanged graph", err, invocations)
	}
	available = true
	recovered, err := RunAtomic(dbPath, root)
	if err != nil || recovered.Status != StatusHealthy || recovered.Reused || invocations != 3 {
		t.Fatalf("recovery=%+v err=%v calls=%d, want healthy rebuild", recovered, err, invocations)
	}
	for _, scope := range recovered.Resolver.Scopes {
		if !scope.Attempted || !scope.Succeeded || scope.Reused || scope.Failed {
			t.Errorf("recovery did not restore scope: %+v", scope)
		}
	}
	assertStoredCallTargets(t, dbPath, project, "main.go.Run", []string{"main.go.helper"})
	assertStoredCallTargets(t, dbPath, project, "lib/checkout.rb.Checkout#process", []string{"lib/gateway.rb.Gateway.authorize"})
	freshDB := filepath.Join(securityPhysicalTempDir(t), "fresh.db")
	if fresh, err := RunAtomic(freshDB, root); err != nil || fresh.Status != StatusHealthy {
		t.Fatalf("reference rebuild=%+v err=%v", fresh, err)
	}
	if digestOf(t, dbPath, project) != digestOf(t, freshDB, project) {
		t.Fatal("recovery differs from healthy full rebuild")
	}
	noOp, err := RunAtomic(dbPath, root)
	if err != nil || !noOp.Reused || noOp.Status != StatusHealthy || invocations != 4 {
		t.Fatalf("healthy no-op=%+v err=%v calls=%d", noOp, err, invocations)
	}
}

func assertStoredCallTargets(t *testing.T, dbPath, project, caller string, targets []string) {
	t.Helper()
	store, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodes, err := store.Neighbors(project, project+":"+caller, "out", string(graph.EdgeCalls), 100)
	if err != nil || len(nodes) != len(targets) {
		t.Fatalf("%s callees=%+v err=%v, want %v", caller, nodes, err, targets)
	}
	for i, target := range targets {
		if nodes[i].QualifiedName != project+":"+target {
			t.Errorf("%s callee=%q, want %q", caller, nodes[i].QualifiedName, project+":"+target)
		}
	}
	if err := store.ValidateIntegrity(); err != nil {
		t.Fatal(err)
	}
}
