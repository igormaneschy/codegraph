package index

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGoControlCommand struct{ executable string }

func newFakeGoControlCommand(t *testing.T, body string) fakeGoControlCommand {
	t.Helper()
	executable := filepath.Join(securityPhysicalTempDir(t), "fake-go")
	// #nosec G306 -- owner-only test launcher must be executable.
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeGoControlCommand{executable: executable}
}

func (fake fakeGoControlCommand) command(ctx context.Context) *exec.Cmd {
	// #nosec G204 -- executable is created by this named fake in an owned temp directory.
	return exec.CommandContext(ctx, fake.executable, "env", "-json")
}

func TestGoControlInputsAreStructuredAndRedactErrors(t *testing.T) {
	fake := newFakeGoControlCommand(t, `printf '%s' '{"GOFLAGS":"--toolexec=/operator-wrapper","GOCACHEPROG":"/operator-cache"}'`)
	inputs, err := observeGoControlInputs(context.Background(), fake.command(context.Background()))
	if err != nil || inputs["GOFLAGS"] != "--toolexec=/operator-wrapper" || inputs["GOCACHEPROG"] != "/operator-cache" {
		t.Fatalf("inputs=%v err=%v", inputs, err)
	}
	for _, body := range []string{`printf null`, `printf '[]'`, `printf '%s' '{"GOCACHEPROG":3}'`, `printf '%s' 'private-token'; exit 2`} {
		invalid := newFakeGoControlCommand(t, body)
		_, err := observeGoControlInputs(context.Background(), invalid.command(context.Background()))
		if err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatalf("invalid controls not rejected/redacted: %v", err)
		}
	}
}

func TestGoControlInputsRetainMissingToolAndCancellationBoundaries(t *testing.T) {
	command := exec.CommandContext(context.Background(), "/never-installed-go")
	inputs, err := observeGoControlInputs(context.Background(), command)
	if err != nil || len(inputs) != 0 {
		t.Fatalf("unavailable tool inputs=%v err=%v", inputs, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := newFakeGoControlCommand(t, `exit 0`)
	if _, err := observeGoControlInputs(ctx, fake.command(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled controls err=%v", err)
	}
}
