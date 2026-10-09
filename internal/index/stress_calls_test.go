package index

import (
	"errors"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

type fakeStressCallGraph struct {
	edges   []graph.CallEdge
	failure error
}

func (fake fakeStressCallGraph) ForEachCallEdge(_ string, visit func(graph.CallEdge) error) error {
	if fake.failure != nil {
		return fake.failure
	}
	for _, edge := range fake.edges {
		if err := visit(edge); err != nil {
			return err
		}
	}
	return nil
}

func healthyStressResult() Result {
	return Result{Project: "fixture", Nodes: 12, EdgesKept: 24, Status: StatusHealthy, Resolver: ResolverReport{Scopes: []ResolverScopeStatus{{Resolver: "go-vta", Scope: "go", Attempted: true, Succeeded: true}}}}
}

// Independent literal oracle, not generated from syntheticGoFile or graph output.
func twoFileStressCalls() []graph.CallEdge {
	return []graph.CallEdge{
		{SourceQN: "fixture:stress/f0.go.Fn0", TargetQN: "fixture:stress/f1.go.Fn1", SourceFile: "stress/f0.go"},
		{SourceQN: "fixture:stress/f0.go.Fn0", TargetQN: "fixture:stress/f0.go.local0", SourceFile: "stress/f0.go"},
		{SourceQN: "fixture:stress/f1.go.Fn1", TargetQN: "fixture:stress/f0.go.Fn0", SourceFile: "stress/f1.go"},
		{SourceQN: "fixture:stress/f1.go.Fn1", TargetQN: "fixture:stress/f1.go.local1", SourceFile: "stress/f1.go"},
	}
}

func TestGoStressCallsRejectStructuralOnlyAndSameCountWrongGraphs(t *testing.T) {
	for _, scenario := range []string{"structural-only", "missing", "same-count-wrong-target", "extra", "duplicate", "wrong-project", "wrong-file"} {
		t.Run(scenario, func(t *testing.T) {
			calls := twoFileStressCalls()
			switch scenario {
			case "structural-only":
				calls = nil
			case "missing":
				calls = calls[:3]
			case "same-count-wrong-target":
				calls[0].TargetQN = "fixture:stress/f1.go.local1"
			case "extra":
				calls = append(calls, graph.CallEdge{SourceQN: "fixture:stress/f0.go.local0", TargetQN: "fixture:stress/f1.go.local1", SourceFile: "stress/f0.go"})
			case "duplicate":
				calls = append(calls, calls[0])
			case "wrong-project":
				calls[0].TargetQN = "other:stress/f1.go.Fn1"
			case "wrong-file":
				calls[0].SourceFile = "stress/f1.go"
			}
			if _, err := validateGoStressCalls(healthyStressResult(), fakeStressCallGraph{edges: calls}, 2); err == nil {
				t.Fatal("high total edge count hid an incorrect CALLS graph")
			}
		})
	}
}

func TestGoStressCallsRequireHealthyAttemptedSuccessfulColdScope(t *testing.T) {
	for _, scenario := range []string{"degraded", "stale", "reused-index", "missing-scope", "failed-scope", "reused-scope", "unattempted", "error-on-success", "extra-scope"} {
		t.Run(scenario, func(t *testing.T) {
			result := healthyStressResult()
			switch scenario {
			case "degraded":
				result.Status = StatusDegraded
			case "stale":
				result.Status = StatusStale
			case "reused-index":
				result.Reused = true
			case "missing-scope":
				result.Resolver.Scopes = nil
			case "failed-scope":
				result.Resolver.Scopes[0].Succeeded = false
				result.Resolver.Scopes[0].Failed = true
				result.Resolver.Scopes[0].Error = "fixture failure"
			case "reused-scope":
				result.Resolver.Scopes[0] = ResolverScopeStatus{Resolver: "go-vta", Scope: "go", Reused: true}
			case "unattempted":
				result.Resolver.Scopes[0].Attempted = false
			case "error-on-success":
				result.Resolver.Scopes[0].Error = "fixture failure"
			case "extra-scope":
				result.Resolver.Scopes = append(result.Resolver.Scopes, ResolverScopeStatus{Resolver: "ruby-static", Scope: "ruby", Attempted: true, Succeeded: true})
			}
			if _, err := validateGoStressCalls(result, fakeStressCallGraph{edges: twoFileStressCalls()}, 2); err == nil {
				t.Fatal("invalid status/scope hid behind total edges")
			}
		})
	}
}

func TestGoStressCallsExactCycleAndSelfCall(t *testing.T) {
	cases := []struct {
		name  string
		files int
		edges []graph.CallEdge
	}{
		{"cycle", 2, twoFileStressCalls()},
		{"self", 1, []graph.CallEdge{{SourceQN: "fixture:stress/f0.go.Fn0", TargetQN: "fixture:stress/f0.go.Fn0", SourceFile: "stress/f0.go"}, {SourceQN: "fixture:stress/f0.go.Fn0", TargetQN: "fixture:stress/f0.go.local0", SourceFile: "stress/f0.go"}}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			result := healthyStressResult()
			result.EdgesKept = 0
			count, err := validateGoStressCalls(result, fakeStressCallGraph{edges: item.edges}, item.files)
			if err != nil || count != 2*item.files {
				t.Fatalf("count=%d err=%v expected=%d actual CALLS independent of total edge metric", count, err, 2*item.files)
			}
		})
	}
}

func TestGoStressCallsPropagateReadErrorsAndRejectInvalidFixture(t *testing.T) {
	failure := errors.New("fixture store unavailable")
	if _, err := validateGoStressCalls(healthyStressResult(), fakeStressCallGraph{failure: failure}, 2); !errors.Is(err, failure) {
		t.Fatalf("store error lost: %v", err)
	}
	result := healthyStressResult()
	result.Project = ""
	if _, err := validateGoStressCalls(result, fakeStressCallGraph{}, 2); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("invalid project=%v", err)
	}
	for _, files := range []int{0, -1} {
		if _, err := validateGoStressCalls(healthyStressResult(), fakeStressCallGraph{}, files); err == nil {
			t.Fatal("invalid fixture size accepted")
		}
	}
}
