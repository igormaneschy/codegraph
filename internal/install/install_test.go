package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOpencodeConfigPath_PrefersExistingJsonc pins that the installer merges into
// the config file opencode actually reads: if the user has an opencode.jsonc, target
// THAT (don't strand the registration in a second opencode.json the agent ignores);
// otherwise default to opencode.json.
func TestOpencodeConfigPath_PrefersExistingJsonc(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// No file yet → default to .json.
	if got := opencodeConfigPath(); filepath.Base(got) != "opencode.json" {
		t.Errorf("with no existing config, path = %q, want opencode.json", got)
	}

	// A pre-existing .jsonc must win.
	ocDir := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonc := filepath.Join(ocDir, "opencode.jsonc")
	if err := os.WriteFile(jsonc, []byte(`{"instructions":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := opencodeConfigPath(); got != jsonc {
		t.Errorf("with an existing opencode.jsonc, path = %q, want %q", got, jsonc)
	}
}

// TestRun_InstallsDetectedAndCollectsManual pins the flow: a detected agent with an
// Install runs it (Installed); a detected agent without Install yields Manual text;
// an undetected agent is skipped entirely.
func TestRun_InstallsDetectedAndCollectsManual(t *testing.T) {
	var did []string
	agents := []Agent{
		{
			Name:    "auto",
			Detect:  func() bool { return true },
			Install: func(bin string) error { did = append(did, "auto:"+bin); return nil },
			Manual:  func(bin string) string { return "manual auto" },
		},
		{
			Name:   "manualOnly",
			Detect: func() bool { return true },
			Manual: func(bin string) string { return "paste for " + bin },
		},
		{
			Name:    "absent",
			Detect:  func() bool { return false },
			Install: func(bin string) error { t.Fatal("undetected agent must not install"); return nil },
		},
	}

	outs := Run(agents, "/usr/bin/codegraph")

	if !slices.Equal(did, []string{"auto:/usr/bin/codegraph"}) {
		t.Fatalf("auto agent should have installed once, got %v", did)
	}
	by := map[string]Outcome{}
	for _, o := range outs {
		by[o.Agent] = o
	}
	if o := by["auto"]; !o.Installed {
		t.Errorf("auto outcome = %+v, want Installed", o)
	}
	if o := by["manualOnly"]; o.Installed || o.Manual == "" {
		t.Errorf("manualOnly outcome = %+v, want manual text and not installed", o)
	}
	if _, ok := by["absent"]; ok {
		t.Errorf("undetected agent must be skipped")
	}
}

// TestClaudeCommand / TestCodexCommand pin the CLI registrations. Both register a
// stdio server with no repo-path arg — the server reads $CLAUDE_PROJECT_DIR or its
// cwd at runtime, so one registration serves any repo. Claude uses user scope.
func TestClaudeCommand(t *testing.T) {
	got := ClaudeCommand("/opt/codegraph")
	want := []string{"claude", "mcp", "add", "--scope", "user", "--transport", "stdio", "codegraph", "--", "/opt/codegraph", "mcp"}
	if !slices.Equal(got, want) {
		t.Errorf("ClaudeCommand =\n %v\nwant\n %v", got, want)
	}
}

func TestCodexCommand(t *testing.T) {
	got := CodexCommand("/opt/codegraph")
	want := []string{"codex", "mcp", "add", "codegraph", "--", "/opt/codegraph", "mcp"}
	if !slices.Equal(got, want) {
		t.Errorf("CodexCommand =\n %v\nwant\n %v", got, want)
	}
}

// TestMergeOpencodeConfig pins the opencode JSON merge: it adds the codegraph local
// server WITHOUT clobbering the user's other config (other top-level keys and other
// MCP servers survive). opencode has no add-CLI, so this file merge is the auto path.
func TestMergeOpencodeConfig(t *testing.T) {
	// From empty: creates the mcp.codegraph local entry.
	out, err := mergeOpencodeConfig(nil, "/opt/codegraph")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	cg := got["mcp"].(map[string]any)["codegraph"].(map[string]any)
	if cg["type"] != "local" || cg["enabled"] != true {
		t.Errorf("codegraph entry = %v, want type=local enabled=true", cg)
	}
	cmd := cg["command"].([]any)
	if len(cmd) != 2 || cmd[0] != "/opt/codegraph" || cmd[1] != "mcp" {
		t.Errorf("command = %v, want [/opt/codegraph mcp]", cmd)
	}

	// Into an existing config: preserve the user's model setting and other server.
	existing := []byte(`{"model":"anthropic/claude","mcp":{"other":{"type":"local","command":["other"],"enabled":true}}}`)
	out2, err := mergeOpencodeConfig(existing, "/opt/codegraph")
	if err != nil {
		t.Fatal(err)
	}
	var got2 map[string]any
	if err := json.Unmarshal(out2, &got2); err != nil {
		t.Fatal(err)
	}
	if got2["model"] != "anthropic/claude" {
		t.Errorf("merge clobbered top-level keys: %v", got2)
	}
	mcp := got2["mcp"].(map[string]any)
	if _, ok := mcp["other"]; !ok {
		t.Errorf("merge dropped the user's existing 'other' server: %v", mcp)
	}
	if _, ok := mcp["codegraph"]; !ok {
		t.Errorf("merge did not add codegraph: %v", mcp)
	}
}

// TestMergeGrokConfig pins the Grok TOML merge: upsert [mcp_servers.codegraph]
// without dropping sibling tables (ai-memory, models, …).
func TestMergeGrokConfig(t *testing.T) {
	out := string(mergeGrokConfig(nil, "/opt/codegraph"))
	if !strings.Contains(out, `command = "/opt/codegraph"`) || !strings.Contains(out, `args = ["mcp"]`) {
		t.Fatalf("empty merge missing codegraph section:\n%s", out)
	}

	existing := []byte(`
[cli]
installer = "internal"

[mcp_servers.ai-memory]
url = "http://127.0.0.1:49374/mcp"
enabled = true

[mcp_servers.codegraph]
command = "/old/codegraph"
args = ["mcp", "/wrong/repo"]
enabled = true

[models]
default = "grok-4.5"
`)
	out2 := string(mergeGrokConfig(existing, "/opt/codegraph"))
	if !strings.Contains(out2, `[mcp_servers.ai-memory]`) {
		t.Errorf("merge dropped ai-memory:\n%s", out2)
	}
	if !strings.Contains(out2, `default = "grok-4.5"`) {
		t.Errorf("merge dropped models:\n%s", out2)
	}
	if strings.Contains(out2, "/old/codegraph") || strings.Contains(out2, "/wrong/repo") {
		t.Errorf("merge left stale codegraph values:\n%s", out2)
	}
	if !strings.Contains(out2, `command = "/opt/codegraph"`) {
		t.Errorf("merge did not set new binary:\n%s", out2)
	}
	// Exactly one codegraph table.
	if n := strings.Count(out2, "[mcp_servers.codegraph]"); n != 1 {
		t.Errorf("want 1 codegraph table, got %d:\n%s", n, out2)
	}
}

// TestMergeOpencodeConfig_AcceptsJSONC pins R14: opencode.jsonc is the file the
// installer prefers, so its comments and trailing commas must parse. Comment
// markers inside string values must survive untouched.
func TestMergeOpencodeConfig_AcceptsJSONC(t *testing.T) {
	existing := []byte(`{
  // opencode user config
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude", /* inline block */
  "mcp": {
    "other": { "type": "local", "command": ["other"], "enabled": true, },
  },
  "instructions": ["url://with//slashes", "/* not a comment */"],
}`)
	out, err := mergeOpencodeConfig(existing, "/opt/codegraph")
	if err != nil {
		t.Fatalf("JSONC config must parse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("merged output is not JSON: %v", err)
	}
	if got["model"] != "anthropic/claude" {
		t.Errorf("merge clobbered model: %v", got)
	}
	mcp, _ := got["mcp"].(map[string]any)
	if _, ok := mcp["other"]; !ok {
		t.Errorf("merge dropped the user's other server: %v", mcp)
	}
	if _, ok := mcp["codegraph"]; !ok {
		t.Errorf("merge did not add codegraph: %v", mcp)
	}
	instr, _ := got["instructions"].([]any)
	if len(instr) != 2 || instr[0] != "url://with//slashes" || instr[1] != "/* not a comment */" {
		t.Errorf("comment-like string values were mangled: %v", instr)
	}
}

// TestMergeOpencodeConfig_RejectsNonObjectDocuments pins that a null/array/scalar
// blob is an error, never a nil-map panic or a silent overwrite.
func TestMergeOpencodeConfig_RejectsNonObjectDocuments(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"null", "null"},
		{"array", "[]"},
		{"string", `"nope"`},
		{"number", "42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mergeOpencodeConfig([]byte(tc.doc), "/opt/codegraph"); err == nil {
				t.Fatalf("%s must be rejected, not merged", tc.name)
			}
		})
	}
	if _, err := mergeOpencodeConfig([]byte(`{"mcp":"nope"}`), "/opt/codegraph"); err == nil {
		t.Fatal(`a non-object "mcp" value must be rejected, not clobbered`)
	}
}

// TestInstallOpencode_PropagatesReadErrors pins that only a missing config is
// treated as empty: an unreadable path must fail, not be overwritten.
func TestInstallOpencode_PropagatesReadErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// A directory at the config path makes os.ReadFile fail with EISDIR, not ENOENT.
	if err := os.MkdirAll(filepath.Join(dir, "opencode", "opencode.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installOpencode("/opt/codegraph"); err == nil {
		t.Fatal("an unreadable config path must fail instead of being treated as empty")
	}
}

// TestInstallOpencode_MergesIntoExistingJSONC pins the end-to-end install: the
// existing .jsonc wins, its user keys survive, the result is owner-only, and no
// staging file is left behind.
func TestInstallOpencode_MergesIntoExistingJSONC(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	ocDir := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ocDir, "opencode.jsonc")
	if err := os.WriteFile(path, []byte("{\n  // keep me\n  \"model\": \"anthropic/claude\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installOpencode("/opt/codegraph"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("installed config is not JSON: %v", err)
	}
	if got["model"] != "anthropic/claude" {
		t.Errorf("install clobbered the user's model: %v", got)
	}
	if _, ok := got["mcp"].(map[string]any)["codegraph"]; !ok {
		t.Errorf("codegraph was not installed: %v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 0600", perm)
	}
	entries, err := os.ReadDir(ocDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "opencode.jsonc" {
		t.Errorf("staging files left behind: %v", entries)
	}
}

// TestWriteFileAtomic_ReplacesWithoutTempLeftovers pins the atomic writer used by
// both config installers.
func TestWriteFileAtomic_ReplacesWithoutTempLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := writeFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatalf("content=%q err=%v, want second", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// TestWriteFileAtomic_PreservesSymlinkTarget pins that a dotfile-style symlinked
// config is written through (the link survives), not replaced by a regular file.
func TestWriteFileAtomic_PreservesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeFileAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("symlink target content=%q err=%v, want new", data, err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("atomic write replaced the symlink instead of following it")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "real.json" {
		t.Fatalf("temp file left behind: %v", entries)
	}
}
