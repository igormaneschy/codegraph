package quality

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func evaluateCallSet(t *testing.T, answers, truths []string, options EvaluationOptions) Evaluation {
	t.Helper()
	options.Modes = []string{"graph"}
	evaluation, err := Evaluate([]Question{{ID: "callers-01", Type: TypeCallers}}, []Truth{{ID: "callers-01", Items: truths}}, []Answer{{ID: "callers-01", Mode: "graph", Items: answers}}, options)
	if err != nil {
		t.Fatal(err)
	}
	return evaluation
}

func TestStrictScoringSeparatesHomonymsAndMemberKinds(t *testing.T) {
	for _, other := range []string{"src/b.go.Service.Run", "src/a.go.Other.Run", "src/a.go.Service.run"} {
		evaluation := evaluateCallSet(t, []string{other}, []string{"src/a.go.Service.Run"}, EvaluationOptions{})
		if evaluation.Scores[0].Quality != 0 {
			t.Fatalf("homonym %q scored as correct: %+v", other, evaluation.Scores)
		}
	}
	evaluation := evaluateCallSet(t, []string{"lib/gateway.rb.Owner.method"}, []string{"lib/gateway.rb.Owner#method"}, EvaluationOptions{})
	if evaluation.Scores[0].Quality != 0 {
		t.Fatal("Ruby singleton and instance method collapsed")
	}
	evaluation = evaluateCallSet(t, []string{"lib/gateway.rb.Outer::Inner#method"}, []string{"lib/gateway.rb.Outer::Inner#method"}, EvaluationOptions{})
	if evaluation.Scores[0].Quality != 1 {
		t.Fatal("Ruby namespace did not preserve identity")
	}
}

func TestStrictScoringPreservesSetsAndEmptyTruth(t *testing.T) {
	cases := []struct {
		name                  string
		answers, truths       []string
		precision, recall, f1 float64
	}{
		{"dedup", []string{"src/a.ts.Owner.run", "src/a.ts.Owner.run"}, []string{"src/a.ts.Owner.run"}, 1, 1, 1},
		{"same-name-two-files", []string{"src/a.ts.Owner.run"}, []string{"src/a.ts.Owner.run", "src/b.ts.Owner.run"}, 1, .5, 2.0 / 3},
		{"empty", []string{}, []string{}, 1, 1, 1},
		{"hallucination", []string{"src/a.ts.Run"}, []string{}, 0, 1, 0},
		{"miss", []string{}, []string{"src/a.ts.Run"}, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evaluation := evaluateCallSet(t, c.answers, c.truths, EvaluationOptions{})
			score := evaluation.Scores[0]
			if !approx(score.Precision, c.precision) || !approx(score.Recall, c.recall) || !approx(score.Quality, c.f1) {
				t.Fatalf("score=%+v", score)
			}
		})
	}
}

func TestLegacyIdentityRequiresExplicitSelection(t *testing.T) {
	evaluation := evaluateCallSet(t, []string{"src/b.go.Service.Run"}, []string{"src/a.go.Service.Run"}, EvaluationOptions{Scorer: ScorerLegacy})
	if evaluation.Scorer != ScorerLegacy || evaluation.Scores[0].Quality != 1 {
		t.Fatalf("legacy=%+v", evaluation)
	}
}

func TestStrictDefinitionsUseExactFullLocation(t *testing.T) {
	for _, c := range []struct {
		name    string
		items   []string
		quality float64
	}{
		{"exact", []string{"src/a.go:12"}, 1},
		{"other-directory", []string{"other/a.go:12"}, 0},
		{"nearby-declaration", []string{"src/a.go:13"}, 0},
		{"empty", []string{}, 0},
		{"shotgun", []string{"src/a.go:12", "other/b.go:20"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			evaluation, err := Evaluate([]Question{{ID: "definition-01", Type: TypeDefinition}}, []Truth{{ID: "definition-01", Items: []string{"src/a.go:12"}}}, []Answer{{ID: "definition-01", Mode: "graph", Items: c.items}}, EvaluationOptions{Modes: []string{"graph"}})
			if err != nil || evaluation.Scores[0].Quality != c.quality {
				t.Fatalf("evaluation=%+v err=%v", evaluation, err)
			}
		})
	}
}

func TestEvaluateOrderingAndCostsAreDeterministic(t *testing.T) {
	qs, truths, answers := completeRunFixture()
	first, err := Evaluate(qs, truths, answers, EvaluationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(truths)
	slices.Reverse(answers)
	second, err := Evaluate(qs, truths, answers, EvaluationOptions{})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("nondeterministic results: first=%+v second=%+v err=%v", first, second, err)
	}
	for _, mode := range []string{"graph", "baseline"} {
		aggregate := first.Aggregates[mode]
		if aggregate.N != 2 || aggregate.TotalTokens != 30 || aggregate.TotalCalls != 3 || !approx(aggregate.MeanQuality, .9) || !approx(aggregate.ByType[TypeOpen], .8) {
			t.Fatalf("aggregate=%+v", aggregate)
		}
	}
}

func completeRunFixture() ([]Question, []Truth, []Answer) {
	qs := []Question{{ID: "callers-01", Type: TypeCallers}, {ID: "open-01", Type: TypeOpen}}
	truths := []Truth{{ID: "callers-01", Items: []string{"src/a.go.Run"}}, {ID: "open-01", Notes: "Independent source-grounded responsibilities"}}
	judge := .8
	var answers []Answer
	for _, mode := range []string{"graph", "baseline"} {
		answers = append(answers, Answer{ID: "callers-01", Mode: mode, Items: []string{"src/a.go.Run"}, Tokens: 10, Calls: 1}, Answer{ID: "open-01", Mode: mode, Text: "Explanation", Judge: &judge, Tokens: 20, Calls: 2})
	}
	return qs, truths, answers
}

func TestEvaluateRejectsMalformedOrIncompleteRuns(t *testing.T) {
	cases := []struct {
		name, context string
		mutate        func(*[]Question, *[]Truth, *[]Answer, *EvaluationOptions)
	}{
		{"empty-questions", "empty question", func(q *[]Question, _ *[]Truth, _ *[]Answer, _ *EvaluationOptions) { *q = nil }},
		{"empty-id", "question ID", func(q *[]Question, _ *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*q)[0].ID = "" }},
		{"duplicate-question", "duplicate question", func(q *[]Question, _ *[]Truth, _ *[]Answer, _ *EvaluationOptions) { *q = append(*q, (*q)[0]) }},
		{"unknown-type", "unsupported type", func(q *[]Question, _ *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*q)[0].Type = "guess" }},
		{"missing-truth", "missing truth", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { *tr = (*tr)[1:] }},
		{"duplicate-truth", "duplicate truth", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { *tr = append(*tr, (*tr)[0]) }},
		{"unknown-truth-id", "unknown question", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*tr)[0].ID = "unexpected" }},
		{"null-truth-items", "explicit items array", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*tr)[0].Items = nil }},
		{"missing-rubric", "rubric notes", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*tr)[1].Notes = "" }},
		{"missing-answer", "missing answer", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { *a = (*a)[1:] }},
		{"duplicate-answer", "duplicate answer", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { *a = append(*a, (*a)[0]) }},
		{"unknown-answer-id", "unknown question", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].ID = "unexpected" }},
		{"unexpected-mode", "unexpected mode", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Mode = "unknown" }},
		{"null-answer-items", "explicit items array", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Items = nil }},
		{"bare-truth", "callers-01", func(_ *[]Question, tr *[]Truth, _ *[]Answer, _ *EvaluationOptions) { (*tr)[0].Items = []string{"Run"} }},
		{"bare-answer", "answer in mode graph", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Items = []string{"Run"} }},
		{"negative-tokens", "nonnegative costs", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Tokens = -1 }},
		{"negative-calls", "nonnegative costs", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Calls = -1 }},
		{"missing-judge", "judge=missing", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[1].Judge = nil }},
		{"missing-text", "needs text", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[1].Text = "" }},
		{"empty-modes", "empty modes", func(_ *[]Question, _ *[]Truth, _ *[]Answer, o *EvaluationOptions) { o.Modes = []string{} }},
		{"duplicate-mode", "duplicate mode", func(_ *[]Question, _ *[]Truth, _ *[]Answer, o *EvaluationOptions) {
			o.Modes = []string{"graph", "graph"}
		}},
		{"unknown-scorer", "invalid scorer", func(_ *[]Question, _ *[]Truth, _ *[]Answer, o *EvaluationOptions) { o.Scorer = "future" }},
		{"token-overflow", "cost overflow", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Tokens = math.MaxInt }},
		{"call-overflow", "cost overflow", func(_ *[]Question, _ *[]Truth, a *[]Answer, _ *EvaluationOptions) { (*a)[0].Calls = math.MaxInt }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qs, truths, answers := completeRunFixture()
			options := EvaluationOptions{}
			c.mutate(&qs, &truths, &answers, &options)
			result, err := Evaluate(qs, truths, answers, options)
			if err == nil || !strings.Contains(err.Error(), c.context) || result.Scores != nil || result.Aggregates != nil {
				t.Fatalf("invalid run produced result=%+v err=%v, want %q", result, err, c.context)
			}
		})
	}
}

func TestEvaluateJudgeBounds(t *testing.T) {
	for _, judge := range []float64{0, 1, -.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(judgeValue(&judge), func(t *testing.T) {
			qs, truths, answers := completeRunFixture()
			answers[1].Judge = &judge
			_, err := Evaluate(qs, truths, answers, EvaluationOptions{})
			valid := judge == 0 || judge == 1
			if (err == nil) != valid {
				t.Fatalf("judge=%v err=%v", judge, err)
			}
		})
	}
}
