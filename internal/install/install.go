// Package install registers the codegraph MCP server into the AI coding agents
// found on the machine. The "main" agents (Claude Code, Codex, opencode, Grok)
// are auto-registered — via their own add-CLI where one exists (safe: the agent
// owns its config format), or a careful config-file merge where it doesn't.
// Anything else is covered by a generic manual snippet (GenericManual).
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Agent is one AI coding agent codegraph can register itself with.
type Agent struct {
	Name    string
	Detect  func() bool             // is this agent installed on the machine?
	Install func(bin string) error  // auto-register; nil = manual only
	Manual  func(bin string) string // paste-ready fallback instructions
}

// Outcome reports what happened for one detected agent.
type Outcome struct {
	Agent     string
	Installed bool
	Err       error
	Manual    string // set when the user must act (no auto path, or it failed)
}

// Run registers codegraph (the binary at bin) into every detected agent. Undetected
// agents are skipped. A detected agent with an Install is auto-registered; if it has
// none, or its Install fails, its manual instructions are returned so the user can
// finish by hand.
func Run(agents []Agent, bin string) []Outcome {
	var out []Outcome
	for _, a := range agents {
		if a.Detect == nil || !a.Detect() {
			continue
		}
		o := Outcome{Agent: a.Name}
		if a.Install != nil {
			if err := a.Install(bin); err != nil {
				o.Err = err
				o.Manual = manualOf(a, bin)
			} else {
				o.Installed = true
			}
		} else {
			o.Manual = manualOf(a, bin)
		}
		out = append(out, o)
	}
	return out
}

func manualOf(a Agent, bin string) string {
	if a.Manual == nil {
		return ""
	}
	return a.Manual(bin)
}

// Agents is the built-in registry. The server is registered with no repo-path arg —
// it resolves the repo from $CLAUDE_PROJECT_DIR or its working directory at launch,
// so a single (user-scoped) registration serves any repo the agent opens.
func Agents() []Agent {
	return []Agent{
		{
			Name:    "Claude Code",
			Detect:  func() bool { return onPath("claude") },
			Install: func(bin string) error { return runCmd(ClaudeCommand(bin)) },
			Manual:  func(bin string) string { return "Run: " + strings.Join(ClaudeCommand(bin), " ") },
		},
		{
			Name:    "Codex",
			Detect:  func() bool { return onPath("codex") },
			Install: func(bin string) error { return runCmd(CodexCommand(bin)) },
			Manual:  func(bin string) string { return "Run: " + strings.Join(CodexCommand(bin), " ") },
		},
		{
			Name:    "opencode",
			Detect:  func() bool { return onPath("opencode") },
			Install: installOpencode,
			Manual:  opencodeManual,
		},
		{
			Name:    "Grok CLI",
			Detect:  func() bool { return onPath("grok") || fileExists(grokConfigPath()) },
			Install: installGrok,
			Manual:  grokManual,
		},
	}
}

// ClaudeCommand is the `claude mcp add` invocation: user scope (any repo), stdio
// transport, no repo arg (the server reads $CLAUDE_PROJECT_DIR at runtime).
func ClaudeCommand(bin string) []string {
	return []string{"claude", "mcp", "add", "--scope", "user", "--transport", "stdio", "codegraph", "--", bin, "mcp"}
}

// CodexCommand is the `codex mcp add` invocation. Codex stores it in
// ~/.codex/config.toml (user scope), so it applies to any repo.
func CodexCommand(bin string) []string {
	return []string{"codex", "mcp", "add", "codegraph", "--", bin, "mcp"}
}

// mergeOpencodeConfig adds the codegraph local server to an opencode config blob
// without clobbering the rest: existing top-level keys and other MCP servers are
// preserved. The input may be JSON or JSONC — opencode reads opencode.jsonc, so
// comments and trailing commas are accepted. A non-object or null document, or a
// non-object "mcp" value, is an error instead of a silent overwrite.
func mergeOpencodeConfig(existing []byte, bin string) ([]byte, error) {
	cfg, err := parseOpencodeConfig(existing)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg["$schema"]; !ok {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	mcp := map[string]any{}
	if raw, ok := cfg["mcp"]; ok && raw != nil {
		existingMCP, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("opencode config \"mcp\" must be a JSON object, found %T", raw)
		}
		mcp = existingMCP
	}
	mcp["codegraph"] = map[string]any{
		"type":    "local",
		"command": []string{bin, "mcp"},
		"enabled": true,
	}
	cfg["mcp"] = mcp
	return json.MarshalIndent(cfg, "", "  ")
}

// parseOpencodeConfig decodes a JSON or JSONC document into a top-level object.
// An empty document is a fresh config; anything that is not a JSON object is an
// error, so a malformed blob can never be replaced by a partial merge.
func parseOpencodeConfig(existing []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(stripJSONTrailingCommas(stripJSONComments(existing)), &value); err != nil {
		return nil, fmt.Errorf("opencode config is not valid JSON/JSONC: %w", err)
	}
	cfg, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("opencode config must be a JSON object, found %T", value)
	}
	return cfg, nil
}

// stripJSONComments removes // and /* */ comments so encoding/json can parse a
// JSONC document. Detection is string-aware, so "https://x" and "/*" inside a
// string survive untouched. Whitespace is otherwise preserved.
func stripJSONComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
				i++
			}
			i++ // land on the closing '/', or past the end when unterminated
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripJSONTrailingCommas removes a comma that is followed only by whitespace and
// a closing brace or bracket, which JSONC permits and encoding/json rejects.
func stripJSONTrailingCommas(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				out = append(out, ' ')
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func installOpencode(bin string) error {
	path := opencodeConfigPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		// Only a missing file means "fresh config"; a permission or I/O error must
		// never be treated as empty and overwrite the user's real config.
		return fmt.Errorf("read opencode config %q: %w", path, err)
	}
	merged, err := mergeOpencodeConfig(existing, bin)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// User-scoped agent config; owner-only perms are the safe default and the
	// agents read it as the same user. Atomic replace keeps an interrupted install
	// from truncating the config.
	return writeFileAtomic(path, merged, 0o600)
}

func opencodeManual(bin string) string {
	blob, _ := mergeOpencodeConfig(nil, bin)
	return "Add to " + opencodeConfigPath() + ":\n" + string(blob)
}

// opencodeConfigPath is the global opencode config file to merge into, honoring
// XDG_CONFIG_HOME (else ~/.config). It prefers an existing opencode.jsonc — opencode
// reads either, and writing a second opencode.json next to the user's real .jsonc
// would either be ignored or shadow their config. Defaults to opencode.json when
// neither exists.
func opencodeConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "opencode")
	if jsonc := filepath.Join(dir, "opencode.jsonc"); fileExists(jsonc) {
		return jsonc
	}
	return filepath.Join(dir, "opencode.json")
}

// #nosec G703 -- path is built internally from XDG_CONFIG_HOME (the user's own
// config dir) or os.UserHomeDir plus fixed component names; os.Stat is a read-only
// existence probe, never an execution or write target.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// grokCodegraphSection is the TOML table Grok CLI reads for a local stdio MCP server.
// No repo-path arg: Grok launches with the session cwd, and codegraph resolves that.
func grokCodegraphSection(bin string) string {
	return fmt.Sprintf(`[mcp_servers.codegraph]
command = %q
args = ["mcp"]
enabled = true
`, bin)
}

// mergeGrokConfig upserts [mcp_servers.codegraph] into a Grok config.toml blob
// without clobbering other tables (ai-memory, models, ui, …).
func mergeGrokConfig(existing []byte, bin string) []byte {
	section := strings.TrimRight(grokCodegraphSection(bin), "\n")
	text := string(existing)
	// Match the whole table: header through the line before the next [table] or EOF.
	re := regexp.MustCompile(`(?ms)^\[mcp_servers\.codegraph\]\s*\n(?:[^\[\n][^\n]*\n|\n)*`)
	if re.MatchString(text) {
		// Keep a blank line after the table so the next [section] stays readable.
		return []byte(re.ReplaceAllString(text, section+"\n\n"))
	}
	if len(strings.TrimSpace(text)) == 0 {
		return []byte(section + "\n")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + section + "\n")
}

func installGrok(bin string) error {
	path := grokConfigPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	merged := mergeGrokConfig(existing, bin)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// #nosec G703 -- path comes from grokConfigPath(): os.UserHomeDir plus the
	// fixed ".grok/config.toml" components, never from user-supplied input.
	// User-scoped agent config; owner-only perms are the safe default.
	return writeFileAtomic(path, merged, 0o600)
}

// writeFileAtomic replaces path with data through a same-directory temporary file
// and a rename, so an interrupted install cannot leave a truncated config. The
// temporary file is removed on every failure path.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (retErr error) {
	// A user may symlink a dotfile into their config dir. Resolve an existing
	// link so the atomic rename lands on its target and the link keeps working;
	// a missing path keeps its own location.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".codegraph-config-*")
	if err != nil {
		return fmt.Errorf("stage config write for %q: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set config mode for %q: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync config %q: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close config %q: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}
	return nil
}

func grokManual(bin string) string {
	return "Add to " + grokConfigPath() + ":\n" + grokCodegraphSection(bin)
}

// grokConfigPath is ~/.grok/config.toml (Grok CLI user config).
func grokConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok", "config.toml")
}

// GenericManual is the fallback for any agent codegraph doesn't auto-register: the
// stdio server command to wire into that agent's MCP config.
func GenericManual(bin string) string {
	return "For any other MCP-capable agent, register a stdio server:\n" +
		"  command: " + bin + "\n" +
		"  args:    [\"mcp\"]\n" +
		"  (the server uses $CLAUDE_PROJECT_DIR or its working directory as the repo)"
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runCmd executes a pre-built argv without a shell. argv is always constructed
// internally by ClaudeCommand/CodexCommand from fixed literal flags plus the
// running codegraph binary path (os.Executable in cmdInstall); no user input or
// environment data reaches it, and exec.Command never interprets metacharacters.
// #nosec G204 -- argv is program-built (fixed literals + own binary path), no shell
func runCmd(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
