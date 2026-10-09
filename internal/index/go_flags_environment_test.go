package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Lordymine/codegraph/internal/securefile"
)

func TestGoFlagsPersistedSettingsAreAdmittedAfterEffectiveObservation(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	config := filepath.Join(securityPhysicalTempDir(t), "go-config")
	t.Setenv("GOENV", config)
	root := writeGoBuildTagFixture(t)
	if err := os.WriteFile(config, []byte("GOFLAGS='--overlay=/unopened/private.env'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanRepositoryContext(context.Background(), root); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("persisted redirection admitted: %v", err)
	}
	if err := os.WriteFile(config, []byte("GOFLAGS=--C=/unopened/private.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanRepositoryContext(context.Background(), root); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unobservable persisted controls admitted: %v", err)
	}
	if err := os.WriteFile(config, []byte("GOFLAGS=\"--toolexec=/unopened/wrapper private-token\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil || !slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, "go-build-flags-inputs-unobserved") {
		t.Fatalf("persisted wrapper coverage: %v", err)
	}
	encoded, err := json.Marshal(scan.manifest)
	if err != nil || strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "/unopened/wrapper") {
		t.Fatal("effective flag command leaked into manifest")
	}
}

func TestGoExternalCachePersistedCommandCannotCertifyReuse(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOCACHEPROG", "")
	config := filepath.Join(securityPhysicalTempDir(t), "go-config")
	t.Setenv("GOENV", config)
	if err := os.WriteFile(config, []byte("GOCACHEPROG=/unopened/cache-driver --credential=private-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan, err := scanRepositoryContext(context.Background(), writeGoBuildTagFixture(t))
	if err != nil || !slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, "go-external-cache-inputs-unobserved") {
		t.Fatalf("persisted cache coverage: reasons=%v available=%v err=%v", scan.manifest.ResolverInputs.NoReuseReasons, scan.manifest.GoEnvironment.Available, err)
	}
	encoded, err := json.Marshal(scan.manifest)
	if err != nil || strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "/unopened/cache-driver") {
		t.Fatal("effective cache command leaked into manifest")
	}
}

func TestGoInputAdmissionPriorPolicyForcesOneRebuild(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	root := writeGoBuildTagFixture(t)
	database := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if result, err := RunAtomic(database, root); err != nil || result.Status != StatusHealthy {
		t.Fatalf("seed=%+v err=%v", result, err)
	}
	manifest, err := ReadManifest(database)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ResolverInputs.Version = "resolver-inputs-v3"
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a previously published sidecar, not a writer admitted by today's policy.
	if err := securefile.WritePrivate(ManifestPath(database), encoded); err != nil {
		t.Fatal(err)
	}
	if result, err := RunAtomic(database, root); err != nil || result.Reused || result.Status != StatusHealthy {
		t.Fatalf("old-policy refresh=%+v err=%v", result, err)
	}
	if result, err := RunAtomic(database, root); err != nil || !result.Reused {
		t.Fatalf("unchanged admitted new-policy refresh=%+v err=%v", result, err)
	}
}
