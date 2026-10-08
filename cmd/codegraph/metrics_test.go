package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/bench"
	"github.com/Lordymine/codegraph/internal/index"
)

func TestIndexAndBenchExposeRunMetrics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package fixture\nfunc Run() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	output := captureMetricsOutput(t, func() {
		if err := cmdIndex(root); err != nil {
			t.Fatal(err)
		}
	})
	for _, required := range []string{"indexed ", "decision=rebuild", "outcome=success", "staged_bytes=0", "phase=definitions", "scip_peak_rss_bytes=0"} {
		if !strings.Contains(output, required) {
			t.Fatalf("index missing %q: %s", required, output)
		}
	}
	output = captureMetricsOutput(t, func() {
		if err := cmdIndex(root); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "decision=noop") || strings.Contains(output, "phase=definitions") {
		t.Fatalf("noop metrics=%s", output)
	}
	result := index.Result{Metrics: index.RunMetrics{Decision: "rebuild", Outcome: "success", Duration: time.Millisecond, StagedBytes: 12}}
	output = captureMetricsOutput(t, func() { printBench(result, time.Second, 0, nil, bench.Summary{}) })
	if !strings.Contains(output, "staged_bytes=12") || !strings.Contains(output, "decision=rebuild") {
		t.Fatalf("bench=%s", output)
	}
}

func TestSessionReportsMetricsEvenAfterCancelledIndex(t *testing.T) {
	root := writeP1Repo(t, map[string]string{"app.go": "package fixture\nfunc Run() {}\n"})
	session, _ := openP1Session(t, root)
	// Use an actual cancellation error so the session takes its cancellation path,
	// which previously discarded the last run result before status could see it.
	session.indexFn = cancelledMetricsIndex
	session.startInitial()
	if !session.waitRound(30 * time.Second) {
		t.Fatal("round did not finish")
	}
	text := session.statusText()
	for _, required := range []string{"outcome=cancelled", "staged_bytes=42", "indexing canceled"} {
		if !strings.Contains(text, required) {
			t.Fatalf("status missing %q: %s", required, text)
		}
	}
}

func cancelledMetricsIndex(context.Context, string, string) (index.Result, error) {
	return index.Result{Metrics: index.RunMetrics{Decision: "rebuild", Outcome: "cancelled", Duration: time.Millisecond, StagedBytes: 42}}, context.Canceled
}

func captureMetricsOutput(t *testing.T, run func()) string {
	t.Helper()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writeEnd
	defer func() { os.Stdout = old; _ = writeEnd.Close(); _ = readEnd.Close() }()
	var captured bytes.Buffer
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(&captured, readEnd); copied <- err }()
	run()
	os.Stdout = old
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-copied; err != nil {
		t.Fatal(err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	return captured.String()
}
