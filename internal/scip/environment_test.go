//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package scip

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scippb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// writeFakeRuntime installs distinguishable node/npx launchers in dir. The
// launcher records the settings it was handed and copies a SCIP fixture to the
// requested output, so a test can prove which runtime actually executed.
const fakeRuntimeScript = `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    output=$2
    shift 2
  else
    shift
  fi
done
printf '%s' "$NODE_OPTIONS" > "$MARKER_DIR/node_options"
printf '%s' "$PATH" > "$MARKER_DIR/path"
cp "$CODEGRAPH_SCIP_FIXTURE" "$output"
`

func writeFakeRuntime(t *testing.T, dir string) {
	t.Helper()
	writeExecutable(t, filepath.Join(dir, "node"), fakeRuntimeScript)
	writeExecutable(t, filepath.Join(dir, "npx"), fakeRuntimeScript)
}

func writeFixtureIndex(t *testing.T) string {
	t.Helper()
	data, err := proto.Marshal(&scippb.Index{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.scip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObserveExecutionEnvironment_TracksLauncherBytes(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntime(t, dir)
	t.Setenv("PATH", dir)

	first, err := ObserveExecutionEnvironment(context.Background())
	if err != nil || !first.Available() {
		t.Fatalf("observe runtime: %+v err=%v", first, err)
	}
	stable, err := ObserveExecutionEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	stableDigest, err := stable.Digest()
	if err != nil || firstDigest != stableDigest {
		t.Fatalf("unchanged runtime digest=%q/%q err=%v", firstDigest, stableDigest, err)
	}

	writeExecutable(t, filepath.Join(dir, "npx"), "#!/bin/sh\nexit 1\n")
	changed, err := ObserveExecutionEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := changed.Digest()
	if err != nil || changedDigest == firstDigest {
		t.Fatalf("replaced npx did not change the identity: %q err=%v", changedDigest, err)
	}
}

func TestExecutionEnvironment_VerifyDetectsExecutableSubstitution(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntime(t, dir)
	t.Setenv("PATH", dir)

	environment, err := ObserveExecutionEnvironment(context.Background())
	if err != nil || !environment.Available() {
		t.Fatalf("observe runtime: %+v err=%v", environment, err)
	}
	if err := environment.Verify(context.Background()); err != nil {
		t.Fatalf("unchanged runtime must verify: %v", err)
	}
	writeExecutable(t, filepath.Join(dir, "npx"), "#!/bin/sh\nexit 0\n")
	if err := environment.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("substituted runtime must fail verification: %v", err)
	}
}

func TestRunAndReadWithEnvironmentContext_RejectsUnavailableRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	environment, err := ObserveExecutionEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if environment.Available() {
		t.Fatalf("empty PATH must not certify a runtime: %+v", environment)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if _, _, err := RunAndReadWithEnvironmentContext(context.Background(), t.TempDir(), out, environment); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing runtime error=%v, want explicit unavailability", err)
	}
}

func TestRunAndReadWithEnvironmentContext_InjectsCapturedRuntime(t *testing.T) {
	observedDir := t.TempDir()
	writeFakeRuntime(t, observedDir)
	markers := t.TempDir()
	// Keep the system PATH behind the fake launcher directory so the fixture
	// script can still use ordinary utilities in the captured environment.
	observedPath := observedDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", observedPath)
	t.Setenv("MARKER_DIR", markers)
	t.Setenv("CODEGRAPH_SCIP_FIXTURE", writeFixtureIndex(t))

	environment, err := ObserveExecutionEnvironment(context.Background())
	if err != nil || !environment.Available() {
		t.Fatalf("observe runtime: %+v err=%v", environment, err)
	}

	// Replace the live process settings after observation. The captured launcher
	// and settings must win; the second runtime would fail if it were used.
	replacement := t.TempDir()
	writeExecutable(t, filepath.Join(replacement, "node"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(replacement, "npx"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", replacement)
	t.Setenv("MARKER_DIR", filepath.Join(replacement, "unused"))

	out := filepath.Join(t.TempDir(), "index.scip")
	if _, _, err := RunAndReadWithEnvironmentContext(context.Background(), t.TempDir(), out, environment); err != nil {
		t.Fatalf("captured runtime invocation: %v", err)
	}
	recordedPath, err := os.ReadFile(filepath.Join(markers, "path"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(recordedPath)) != observedPath {
		t.Fatalf("child PATH=%q, want captured %q", strings.TrimSpace(string(recordedPath)), observedPath)
	}
	recordedOptions, err := os.ReadFile(filepath.Join(markers, "node_options"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recordedOptions), "--max-old-space-size=") {
		t.Fatalf("child NODE_OPTIONS=%q missing heap cap", recordedOptions)
	}
}
