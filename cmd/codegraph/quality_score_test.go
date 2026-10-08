package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/quality"
)

func writeQualityRun(t *testing.T, qs []quality.Question, truths []quality.Truth, answers []quality.Answer) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "questions.json"), qs); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "truth.json"), truths); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "answers.json"), answers); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestQualityScoreDefaultDoesNotCollapseHomonyms(t *testing.T) {
	qs := []quality.Question{{ID: "callers-01", Type: quality.TypeCallers}}
	truths := []quality.Truth{{ID: "callers-01", Items: []string{"src/a.go.Service.Run"}}}
	answers := []quality.Answer{
		{ID: "callers-01", Mode: "graph", Items: []string{"src/b.go.Service.Run"}},
		{ID: "callers-01", Mode: "baseline", Items: []string{"src/a.go.Service.Run"}},
	}
	dir := writeQualityRun(t, qs, truths, answers)
	if err := cmdQualityScore(dir); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "| **graph** | 0% |") {
		t.Fatalf("homonyms scored as equal: %s", report)
	}
}

func TestQualityScoreFlagsSelectExplicitLegacyAndGraphOnly(t *testing.T) {
	qs := []quality.Question{{ID: "callers-01", Type: quality.TypeCallers}}
	truths := []quality.Truth{{ID: "callers-01", Items: []string{"src/a.go.Owner.Run"}}}
	answers := []quality.Answer{{ID: "callers-01", Mode: "graph", Items: []string{"src/b.go.Owner.Run"}}}
	dir := writeQualityRun(t, qs, truths, answers)
	if err := cmdQuality([]string{"score", dir, "--scorer", "name-v1", "--modes", "graph"}); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "scorer=name-v1") || !strings.Contains(string(report), "| **graph** | 100% |") || strings.Contains(string(report), "| graph | baseline |") {
		t.Fatalf("explicit legacy graph-only report=%s", report)
	}
	for _, args := range [][]string{nil, {dir, "--unknown"}, {dir, "extra"}, {dir, "--scorer", "unknown", "--modes", "graph"}, {dir, "--modes", ""}} {
		if err := cmdQualityScoreArgs(args); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

func TestQualityScoreRejectsIncompleteRunWithoutReplacingReport(t *testing.T) {
	qs := []quality.Question{{ID: "callers-01", Type: quality.TypeCallers}}
	truths := []quality.Truth{{ID: "callers-01", Items: []string{"src/a.go.Run"}}}
	answers := []quality.Answer{{ID: "callers-01", Mode: "graph", Items: []string{"src/a.go.Run"}}}
	dir := writeQualityRun(t, qs, truths, answers)
	report := filepath.Join(dir, "report.md")
	if err := os.WriteFile(report, []byte("keep existing report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdQualityScore(dir); err == nil {
		t.Fatal("missing baseline accepted")
	}
	content, err := os.ReadFile(report)
	if err != nil || string(content) != "keep existing report\n" {
		t.Fatalf("invalid run replaced report: %q err=%v", content, err)
	}
}
