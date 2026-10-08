package quality

import (
	"strings"
	"testing"
)

func TestQualifiedIdentityIsCanonicalWithoutFolding(t *testing.T) {
	for _, qn := range []string{"src/a.go.Box.Run", "src/A.GO.Box.Run", "src/a.spec.ts.Owner.method", "lib/a.rb.Owner#[]=", "lib/a.rb.Outer::Inner.method", "src/a.go", "dir with spaces/a.ts.Owner.method", `src/a.ts.Owner."foo/bar"`, `src/a.ts.Owner["method with spaces"]`, "src/dotted.ts.dir/a.ts.Owner.method"} {
		if err := validateQualifiedName(qn); err != nil {
			t.Errorf("valid qn %q: %v", qn, err)
		}
	}
	for _, qn := range []string{"Run", "Owner.Run", "project:src/a.go.Run", "/src/a.go.Run", "../src/a.go.Run", "src/../a.go.Run", "src//a.go.Run", "src\\a.go.Run", "src/a.go.", "src/a.go.Run\tannotation", "src/a.go.Run\n", "src/a.go.\xff"} {
		if err := validateQualifiedName(qn); err == nil {
			t.Errorf("invalid qn %q accepted", qn)
		}
	}
}

func TestDeclarationLocationsRejectMissingLinesAndTraversal(t *testing.T) {
	for _, location := range []string{"src/a.go:1", "src/a.go:100", "dir with spaces/a.ts:12"} {
		if err := validateLocation(location); err != nil {
			t.Errorf("valid location %q: %v", location, err)
		}
	}
	for _, location := range []string{"src/a.go", "src/a.go:0", "src/a.go:-1", "src/a.go:01", "src/a.go:+1", "src/a.go:NaN", "src/a.go:1junk", "src/a.go:1:2", "../a.go:1", "/a.go:1", "C:/a.go:1", "a\\b.go:1", "./a.go:1"} {
		if err := validateLocation(location); err == nil {
			t.Errorf("invalid location %q accepted", location)
		}
	}
}

func TestInvalidDefinitionArtifactsReturnContext(t *testing.T) {
	for _, truth := range [][]string{nil, {}, {"src/a.go:1", "src/a.go:2"}, {"src/a.go"}} {
		_, err := Evaluate([]Question{{ID: "definition-01", Type: TypeDefinition}}, []Truth{{ID: "definition-01", Items: truth}}, []Answer{{ID: "definition-01", Mode: "graph", Items: []string{"src/a.go:1"}}}, EvaluationOptions{Modes: []string{"graph"}})
		if err == nil || !strings.Contains(err.Error(), "definition-01") {
			t.Fatalf("truth=%v err=%v", truth, err)
		}
	}
}

func TestLegacyDefinitionRetainsBasenameAndLineTolerance(t *testing.T) {
	evaluation, err := Evaluate([]Question{{ID: "definition-01", Type: TypeDefinition}}, []Truth{{ID: "definition-01", Items: []string{"src/a.go:12"}}}, []Answer{{ID: "definition-01", Mode: "graph", Items: []string{"other/a.go:15"}}}, EvaluationOptions{Scorer: ScorerLegacy, Modes: []string{"graph"}})
	if err != nil || evaluation.Scores[0].Quality != 1 {
		t.Fatalf("legacy=%+v err=%v", evaluation, err)
	}
}

func TestScoringBothCallQuestionTypesUsesStrictIdentity(t *testing.T) {
	for _, typ := range []QType{TypeCallers, TypeCallees} {
		evaluation, err := Evaluate([]Question{{ID: "question-01", Type: typ}}, []Truth{{ID: "question-01", Items: []string{"a.go.Owner.Run"}}}, []Answer{{ID: "question-01", Mode: "graph", Items: []string{"b.go.Owner.Run"}}}, EvaluationOptions{Modes: []string{"graph"}})
		if err != nil || evaluation.Scores[0].Quality != 0 {
			t.Fatalf("type=%s evaluation=%+v err=%v", typ, evaluation, err)
		}
	}
}

func TestRunIDsAndOptionsAreValidated(t *testing.T) {
	for _, id := range []string{"", " callers-01", "callers-01 ", "callers\t01", "callers\n01"} {
		if validRunID(id) {
			t.Errorf("invalid id %q accepted", id)
		}
	}
	if !validRunID("question-01") {
		t.Fatal("valid ID rejected")
	}
	for _, modes := range [][]string{{""}, {"graph "}, {"graph\nbaseline"}} {
		if _, err := normalizedOptions(EvaluationOptions{Modes: modes}); err == nil {
			t.Errorf("invalid modes %v accepted", modes)
		}
	}
	modes := []string{"graph"}
	options, err := normalizedOptions(EvaluationOptions{Modes: modes})
	if err != nil {
		t.Fatal(err)
	}
	modes[0] = "changed"
	if options.Modes[0] != "graph" {
		t.Fatal("normalized options alias caller-owned modes")
	}
}
