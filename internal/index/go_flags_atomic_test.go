package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestGoFlagsQuotedRedirectionPreservesCommittedGraphAndManifest(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	database := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if result, err := RunAtomic(database, root); err != nil || result.Status != StatusHealthy {
		t.Fatalf("seed=%+v err=%v", result, err)
	}
	project := ProjectName(root)
	before := digestOf(t, database, project)
	manifest, err := securefile.ReadFile(ManifestPath(database))
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range []string{`"--overlay=/never opened/private.env"`, `'--modfile=/never opened/private.env'`, "--C=/never-opened"} {
		t.Setenv("GOFLAGS", flags)
		result, err := RunAtomic(database, root)
		if err == nil || !strings.Contains(err.Error(), "not admitted") || result.Metrics.Outcome != "failed" {
			t.Fatalf("redirection result=%+v err=%v", result, err)
		}
		after, err := securefile.ReadFile(ManifestPath(database))
		if err != nil || string(after) != string(manifest) || digestOf(t, database, project) != before {
			t.Fatalf("redirection replaced committed artifacts: %v", err)
		}
	}
	assertStoredCallTargets(t, database, project, "common.go.Run", []string{"default.go.selected"})
}

func TestGoFlagsWrapperMutationNeverReusesGoCallsAndMatchesRebuild(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	writeSecurityFile(t, root, "other.rb", "def unrelated\nend\n")
	wrapper := filepath.Join(securityPhysicalTempDir(t), "wrapper")
	// #nosec G306 -- owner-only delegating Go tool wrapper must be executable.
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", `"--toolexec=`+wrapper+`"`)
	original := goCallEdges
	invocations := 0
	t.Cleanup(func() { goCallEdges = original })
	goCallEdges = func(ctx context.Context, project, root string, known func(string) bool, env []string) ([]graph.Edge, error) {
		invocations++
		return original(ctx, project, root, known, env)
	}
	database := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	for round := 1; round <= 4; round++ {
		if round == 2 {
			// #nosec G306 -- mutate the same owner-only executable without changing its path.
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n# same path, different bytes\nexec \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if round == 4 {
			writeSecurityFile(t, root, "other.rb", "def unrelated\nend\ndef another\nend\n")
		}
		result, err := RunAtomic(database, root)
		if err != nil || result.Status != StatusHealthy || result.Reused || invocations != round {
			t.Fatalf("round=%d result=%+v err=%v invocations=%d", round, result, err, invocations)
		}
		if round == 3 && !slices.Contains(result.Metrics.InvalidationReasons, "go-build-flags-inputs-unobserved") {
			t.Fatalf("missing flag coverage reason: %+v", result.Metrics)
		}
		assertStoredCallTargets(t, database, ProjectName(root), "common.go.Run", []string{"default.go.selected"})
	}
	fresh := filepath.Join(securityPhysicalTempDir(t), "fresh.db")
	if result, err := RunAtomic(fresh, root); err != nil || result.Status != StatusHealthy {
		t.Fatalf("reference=%+v err=%v", result, err)
	}
	if digestOf(t, database, ProjectName(root)) != digestOf(t, fresh, ProjectName(root)) {
		t.Fatal("uncertified wrapper refresh differs from full rebuild")
	}
}
