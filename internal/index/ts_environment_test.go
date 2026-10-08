package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/Lordymine/codegraph/internal/scip"
)

func writeTSRuntimeFixture(t *testing.T) string {
	t.Helper()
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	writeSecurityFile(t, root, "main.ts", "export function run() {}\n")
	return root
}

// writeTSRuntimeLauncher installs a node/npx stand-in under dir. Observation
// hashes these bytes; it never launches them.
func writeTSRuntimeLauncher(t *testing.T, dir, body string) {
	t.Helper()
	for _, name := range []string{"node", "npx"} {
		// #nosec G306 -- launcher stand-in must stay executable (0755).
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTSEnvironment_ProcessSettingsChangeObservation(t *testing.T) {
	for _, key := range []string{"PATH", "NODE_OPTIONS", "NODE_PATH", "npm_config_userconfig", "npm_config_cache"} {
		t.Run(key, func(t *testing.T) {
			root := writeTSRuntimeFixture(t)
			t.Setenv(key, "fixture-before")
			before, err := scanRepositoryContext(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "fixture-after")
			after, err := scanRepositoryContext(context.Background(), root)
			if err != nil || sameRepositoryScan(before, after) {
				t.Fatalf("runtime setting %s was not observed: %v", key, err)
			}
		})
	}
}

func TestTSEnvironment_LauncherByteChangeInvalidatesIdentity(t *testing.T) {
	root := writeTSRuntimeFixture(t)
	runtimeDir := t.TempDir()
	writeTSRuntimeLauncher(t, runtimeDir, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", runtimeDir)

	before, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !before.manifest.TSEnvironment.Available {
		t.Fatalf("runtime must be observed: %+v", before.manifest.TSEnvironment)
	}
	writeTSRuntimeLauncher(t, runtimeDir, "#!/bin/sh\nexit 1\n")
	after, err := scanRepositoryContext(context.Background(), root)
	if err != nil || sameManifestFingerprint(before.manifest, after.manifest) {
		t.Fatalf("launcher byte change was not observed: %v", err)
	}
}

func TestTSEnvironment_IdentityDoesNotPersistRuntimeSettings(t *testing.T) {
	root := writeTSRuntimeFixture(t)
	runtimeDir := t.TempDir()
	writeTSRuntimeLauncher(t, runtimeDir, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", runtimeDir)
	t.Setenv("npm_config_userconfig", "/private/fixture-user-config.json")
	t.Setenv("npm_config_cache", "/private/fixture-cache")

	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(scan.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-user-config") || strings.Contains(string(encoded), "fixture-cache") {
		t.Fatal("runtime settings must never be persisted in the manifest")
	}
	if scan.manifest.TSEnvironment.Version != "ts-env-v1" || !validSHA256(scan.manifest.TSEnvironment.Digest) {
		t.Fatalf("runtime identity=%+v, want an opaque validated digest", scan.manifest.TSEnvironment)
	}
}

func TestTSEnvironment_MissingRuntimeFailsResolverExplicitly(t *testing.T) {
	root := writeTSRuntimeFixture(t)
	t.Setenv("PATH", t.TempDir())
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(db, root)
	if err != nil {
		t.Fatalf("first degraded index failed: %v", err)
	}
	if result.Status != StatusDegraded || !result.Resolver.HasFailures() {
		t.Fatalf("missing runtime result=%+v", result)
	}
	if !resolverFailureMentions(result, "runtime is unavailable") {
		t.Fatalf("resolver scopes=%+v, want explicit unavailability", result.Resolver.Scopes)
	}
}

func resolverFailureMentions(result Result, needle string) bool {
	for _, scope := range result.Resolver.Scopes {
		if strings.Contains(scope.Error, needle) {
			return true
		}
	}
	return false
}

func TestTSEnvironment_UncertifiedRuntimeNeverReusesHealthyCalls(t *testing.T) {
	root := writeTSRuntimeFixture(t)
	original := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = original })
	invocations := 0
	scipRunAndRead = func(context.Context, string, string, *scip.ExecutionEnvironment) (*scippb.Index, scip.RunStats, error) {
		invocations++
		return &scippb.Index{}, scip.RunStats{}, nil
	}
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	for round := 1; round <= 2; round++ {
		result, err := RunAtomic(db, root)
		if err != nil || result.Status != StatusHealthy || result.Reused || invocations != round {
			t.Fatalf("round=%d result=%+v invocations=%d err=%v", round, result, invocations, err)
		}
	}
}
