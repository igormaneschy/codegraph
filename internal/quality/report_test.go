package quality

import (
	"strings"
	"testing"
)

func TestReportLabelsMethodMatrixAndEscapesMetadata(t *testing.T) {
	id := "question|`<script>"
	mode := "custom|<mode>"
	report, err := Report([]Question{{ID: id, Type: TypeCallers}}, []Truth{{ID: id, Items: []string{}}}, []Answer{{ID: id, Mode: mode, Items: []string{}}}, EvaluationOptions{Modes: []string{mode}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"scorer=qualified-name-v1", "questions=1", "question&#124;&#96;&lt;script&gt;", "custom&#124;&lt;mode&gt;", "| 100% |"} {
		if !strings.Contains(report, expected) {
			t.Errorf("report lacks %q: %s", expected, report)
		}
	}
	if strings.Contains(report, "<script>") || strings.Contains(report, "| graph | baseline |") {
		t.Fatalf("unsafe or undeclared report columns: %s", report)
	}
}

func TestReportRejectsInvalidRunBeforeRendering(t *testing.T) {
	report, err := Report(nil, nil, nil, EvaluationOptions{})
	if err == nil || report != "" {
		t.Fatalf("report=%q err=%v", report, err)
	}
}

func TestReportOrderingAndLegacyWarning(t *testing.T) {
	qs, truths, answers := completeRunFixture()
	report, err := Report(qs, truths, answers, EvaluationOptions{Scorer: ScorerLegacy})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "scorer=name-v1") || !strings.Contains(report, "Not comparable to qualified-name-v1") || strings.Index(report, "| **graph**") > strings.Index(report, "| **baseline**") {
		t.Fatalf("legacy report=%s", report)
	}
}
