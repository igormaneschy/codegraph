package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/bench"
)

func TestBenchmarkWorkerProducesStructuredSample(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.rb"), []byte("def run\nend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := benchmarkWorkerRequest{Operation: "sample", SampleRequest: bench.SampleRequest{Root: root, Database: filepath.Join(directory, "graph.db")}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := cmdBenchSample(bytes.NewReader(encoded), &output); err != nil {
		t.Fatal(err)
	}
	var sample bench.IndexSample
	if err := json.Unmarshal(output.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Status != "healthy" || sample.Method != bench.MatrixMethod || sample.Metrics.Decision != "rebuild" || sample.SelfPeakRSSBytes == 0 {
		t.Fatalf("sample=%+v", sample)
	}
}

func TestBenchmarkWorkerRejectsUnknownOversizeAndMultipleRequests(t *testing.T) {
	for _, request := range []string{`{"operation":"sample","unknown":true}`, `{} {}`, strings.Repeat(" ", (4<<20)+1)} {
		if _, err := decodeSampleRequest(strings.NewReader(request)); err == nil {
			t.Fatal("malformed worker request accepted")
		}
	}
	for _, request := range []string{`{"operation":"unknown"}`, `{"operation":"edit"}`} {
		if err := cmdBenchSample(strings.NewReader(request), &bytes.Buffer{}); err == nil {
			t.Fatal("invalid operation accepted")
		}
	}
}
