package index

import (
	"fmt"

	"github.com/Lordymine/codegraph/internal/graph"
)

type stressCallGraph interface {
	ForEachCallEdge(project string, visit func(graph.CallEdge) error) error
}

type stressCallExpectation struct {
	source string
	target string
	file   string
}

// This fixture's source spec has exactly a successor call and local call per Fn.
// Expected identities are independent of syntheticGoFile and resolver output.
func validateGoStressCalls(result Result, calls stressCallGraph, files int) (int, error) {
	if result.Project == "" || files < 1 {
		return 0, fmt.Errorf("expected nonempty project and positive fixture size, got project=%q files=%d", result.Project, files)
	}
	if err := validateGoStressOutcome(result); err != nil {
		return 0, err
	}
	expected := expectedGoStressCalls(result.Project, files)
	matched, err := consumeGoStressCalls(calls, result.Project, expected)
	if err != nil {
		return matched, err
	}
	if len(expected) != 0 {
		return matched, fmt.Errorf("missing %d expected CALLS: got %d, expected %d", len(expected), matched, 2*files)
	}
	return matched, nil
}

func validateGoStressOutcome(result Result) error {
	if result.Status != StatusHealthy || result.Reused {
		return fmt.Errorf("expected healthy cold Go stress result, got status=%q reused=%v", result.Status, result.Reused)
	}
	scopes := map[string]struct{}{resolverScopeKey("go-vta", "go"): {}}
	if err := result.Resolver.ValidateExpected(scopes); err != nil {
		return err
	}
	scope := result.Resolver.Scopes[0]
	if !scope.Attempted || !scope.Succeeded || scope.Reused || scope.Failed {
		return fmt.Errorf("expected attempted successful Go stress scope, got %+v", scope)
	}
	return nil
}

func expectedGoStressCalls(project string, files int) map[stressCallExpectation]struct{} {
	expected := make(map[stressCallExpectation]struct{}, 2*files)
	for index := 0; index < files; index++ {
		file := fmt.Sprintf("stress/f%d.go", index)
		source := fmt.Sprintf("%s:%s.Fn%d", project, file, index)
		successor := (index + 1) % files
		target := fmt.Sprintf("%s:stress/f%d.go.Fn%d", project, successor, successor)
		local := fmt.Sprintf("%s:%s.local%d", project, file, index)
		expected[stressCallExpectation{source: source, target: target, file: file}] = struct{}{}
		expected[stressCallExpectation{source: source, target: local, file: file}] = struct{}{}
	}
	return expected
}

func consumeGoStressCalls(calls stressCallGraph, project string, expected map[stressCallExpectation]struct{}) (int, error) {
	matched := 0
	err := calls.ForEachCallEdge(project, func(edge graph.CallEdge) error {
		key := stressCallExpectation{source: edge.SourceQN, target: edge.TargetQN, file: edge.SourceFile}
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("unexpected/duplicate CALLS or source file: %q -> %q in %q", edge.SourceQN, edge.TargetQN, edge.SourceFile)
		}
		delete(expected, key)
		matched++
		return nil
	})
	return matched, err
}
