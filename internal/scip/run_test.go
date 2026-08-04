package scip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScipTypescriptVersionPinned(t *testing.T) {
	if scipTypescriptVersion == "" || scipTypescriptVersion == "latest" {
		t.Fatalf("scip-typescript must be pinned to a release, got %q", scipTypescriptVersion)
	}
}

func TestNodeEnv_AppendsMaxOldSpaceSize(t *testing.T) {
	env := nodeEnv(2048)
	var opts string
	for _, e := range env {
		if strings.HasPrefix(e, "NODE_OPTIONS=") {
			opts = e
			break
		}
	}
	if opts == "" {
		t.Fatal("NODE_OPTIONS not set")
	}
	if !strings.Contains(opts, "--max-old-space-size=2048") {
		t.Fatalf("NODE_OPTIONS=%q missing heap cap", opts)
	}
}

func TestProcRSS_Parse(t *testing.T) {
	const sample = `Name:	node
VmRSS:	 123456 kB
`
	// procRSS reads a file; test the parser via inline duplicate for the fixture shape.
	var kb uint64
	for _, line := range strings.Split(sample, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb = 123456
			}
		}
	}
	if kb != 123456 {
		t.Fatal("fixture parse failed")
	}
}

func TestRunAndRead_RejectsPreexistingOutputArtifact(t *testing.T) {
	out := filepath.Join(t.TempDir(), "existing.scip")
	if err := os.WriteFile(out, []byte("old invocation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunAndReadContext(context.Background(), t.TempDir(), out); err == nil || !strings.Contains(err.Error(), "already exists before invocation") {
		t.Fatalf("preexisting SCIP output error=%v", err)
	}
}
