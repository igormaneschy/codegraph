// codegraph — a tiny, token-efficient code knowledge graph for AI agents.
//
// A Go reimplementation (MVP) of the ideas in DeusData/codebase-memory-mcp.
// See docs/ for the full design, the upstream reference, and the roadmap.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/Lordymine/codegraph/internal/bench"
	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/install"
	"github.com/Lordymine/codegraph/internal/mcp"
	"github.com/Lordymine/codegraph/internal/quality"
	"github.com/Lordymine/codegraph/internal/query"
	"github.com/Lordymine/codegraph/internal/securefile"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "index":
		err = cmdIndex(arg(2, "."))
	case "stats":
		err = cmdStats(arg(2, "."))
	case "changes":
		err = cmdChanges(arg(2, "."))
	case "install":
		err = cmdInstall()
	case "mcp":
		err = cmdMCP(arg(2, "."))
	case "bench":
		err = cmdBench(arg(2, "."))
	case "quality":
		err = cmdQuality(os.Args[2:])
	case "cli":
		err = cmdCLI(os.Args[2:])
	case "version", "--version", "-v":
		cmdVersion()
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func arg(i int, def string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return def
}

func usage() {
	fmt.Fprint(os.Stderr, `codegraph — code knowledge graph for AI agents

Usage:
  codegraph index <path>          Index a repo into the local graph store
  codegraph stats <path>          Show node/edge counts for a repo
  codegraph changes <path>        List source files changed since the last index
  codegraph install               Register codegraph as an MCP server in detected agents
  codegraph mcp   [path]          Serve the graph over MCP (stdio); default = cwd / $CLAUDE_PROJECT_DIR
  codegraph bench <path>          Re-index + measure token/tool-call/speed efficiency
  codegraph quality gen <repo> [outdir] [lang]   Generate the answer-quality question set
  codegraph quality score <dir>                  Grade filled truth+answers -> report.md
  codegraph cli   <tool> <path> <json>   Run one query tool (search|callers|callees|neighbors|similar|dead_code|get_architecture|snippet)
  codegraph version               Print binary path + build identity (verify fork vs stale install)

Store lives in ~/Library/Caches/codegraph/<project>-<root-digest>.db (macOS) or ~/.cache/codegraph/
`)
}

// cmdVersion prints enough identity to tell a fresh fork build from a stale
// system install (path, module, Go version, VCS revision when embedded).
func cmdVersion() {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fmt.Printf("codegraph\n  path:    %s\n", exe)
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("  build:   unknown")
		return
	}
	fmt.Printf("  module:  %s\n", info.Main.Path)
	fmt.Printf("  go:      %s\n", info.GoVersion)
	var rev, t string
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			t = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		line := "  vcs:     " + rev
		if t != "" {
			line += " (" + t + ")"
		}
		if modified {
			line += " dirty"
		}
		fmt.Println(line)
	}
	// Capability fingerprint: Ruby/Rails static CALLS landed in this fork.
	fmt.Println("  features: ruby-static-calls, rails-routes, go-vta, scip-typescript")
}

func storePath(repoRoot string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return cachePath(cache, repoRoot)
}

func cachePath(cacheRoot, repoRoot string) (string, error) {
	repoRoot, err := absoluteRepoRoot(repoRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheRoot, "codegraph")
	if err := securefile.MkdirAllPrivate(dir); err != nil {
		return "", fmt.Errorf("prepare private cache directory: %w", err)
	}
	project := index.ProjectName(repoRoot)
	digest := sha256.Sum256([]byte(filepath.ToSlash(repoRoot)))
	name := fmt.Sprintf("%s-%x.db", project, digest)
	return filepath.Join(dir, name), nil
}

func absoluteRepoRoot(root string) (string, error) {
	abs, err := index.ValidateRepositoryRoot(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return abs, nil
}

func openFor(root string) (*graph.Store, string, *index.Lock, error) {
	var err error
	root, err = absoluteRepoRoot(root)
	if err != nil {
		return nil, "", nil, err
	}
	project := index.ProjectName(root)
	sp, err := storePath(root)
	if err != nil {
		return nil, "", nil, err
	}
	readerLock, err := index.AcquireReaderLock(sp)
	if err != nil {
		return nil, "", nil, err
	}
	st, err := graph.Open(sp)
	if err != nil {
		_ = readerLock.Release()
		return nil, "", nil, err
	}
	return st, project, readerLock, nil
}

const (
	mcpStartupRetryDeadline       = 2 * time.Second
	mcpStartupRetryInitialBackoff = 5 * time.Millisecond
	mcpStartupRetryMaxBackoff     = 50 * time.Millisecond
)

// mcpStartupRetryHook is a package-local deterministic test seam invoked only
// after a real ErrIndexLocked result. Production startup has no hook.
var mcpStartupRetryHook func()

func openForWithRetry(root string) (*graph.Store, string, *index.Lock, error) {
	deadline := time.Now().Add(mcpStartupRetryDeadline)
	backoff := mcpStartupRetryInitialBackoff
	for {
		st, project, readerLock, err := openFor(root)
		if err == nil {
			return st, project, readerLock, nil
		}
		if !errors.Is(err, index.ErrIndexLocked) {
			return nil, "", nil, err
		}
		if mcpStartupRetryHook != nil {
			mcpStartupRetryHook()
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, "", nil, fmt.Errorf("MCP startup lock contention exceeded %v: %w", mcpStartupRetryDeadline, err)
		}
		wait := backoff
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		<-timer.C
		if backoff < mcpStartupRetryMaxBackoff {
			backoff *= 2
			if backoff > mcpStartupRetryMaxBackoff {
				backoff = mcpStartupRetryMaxBackoff
			}
		}
	}
}

func reopenStoreFor(st *graph.Store, dbPath string) (*index.Lock, error) {
	readerLock, err := index.AcquireReaderLock(dbPath)
	if err != nil {
		return nil, err
	}
	if err := st.Reopen(dbPath); err != nil {
		_ = readerLock.Release()
		return nil, err
	}
	return readerLock, nil
}

func reopenEngineFor(eng *query.Engine, dbPath string) (*index.Lock, error) {
	readerLock, err := index.AcquireReaderLock(dbPath)
	if err != nil {
		return nil, err
	}
	if err := eng.Reopen(dbPath); err != nil {
		_ = readerLock.Release()
		return nil, err
	}
	return readerLock, nil
}

const (
	mcpReopenRetryDeadline       = 2 * time.Second
	mcpReopenRetryInitialBackoff = 5 * time.Millisecond
	mcpReopenRetryMaxBackoff     = 50 * time.Millisecond
)

type mcpIndexHooks struct {
	beforeRun        func()
	beforeRunContext func(context.Context) error
	reopenAttempt    func()
}

func cmdIndex(root string) error {
	var err error
	root, err = absoluteRepoRoot(root)
	if err != nil {
		return err
	}
	sp, err := storePath(root)
	if err != nil {
		return err
	}
	res, err := index.RunAtomic(sp, root)
	debug.FreeOSMemory()
	if err != nil {
		return err
	}
	if res.Reused {
		fmt.Printf("unchanged %s — reused index (files=%d nodes=%d edges=%d status=%s)\n",
			res.Project, res.Files, res.Nodes, res.EdgesKept, res.Status)
		if res.Status == index.StatusDegraded {
			fmt.Printf("  resolver failed: %s\n", res.Resolver.Summary())
		}
		return nil
	}
	fmt.Printf("indexed %s\n  files=%d nodes=%d edges=%d (dropped %d unresolved)\n",
		res.Project, res.Files, res.Nodes, res.EdgesKept, res.EdgesDropped)
	if res.Status == index.StatusDegraded {
		fmt.Printf("  status=degraded; resolver failed: %s\n", res.Resolver.Summary())
	}
	if res.ScipScopes > 0 {
		line := fmt.Sprintf("  scip-typescript: %d scope(s), node heap cap %d MB",
			res.ScipScopes, res.ScipHeapCapMB)
		if res.ScipPeakRSS > 0 {
			line += fmt.Sprintf(", peak RSS %d MB", res.ScipPeakRSS/(1024*1024))
		}
		fmt.Println(line)
	}
	return nil
}

func cmdChanges(root string) (retErr error) {
	st, project, readerLock, err := openFor(root)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := readerLock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()
	defer st.Close()
	ch, err := index.DetectChanges(st, project, root)
	if err != nil {
		return err
	}
	if !ch.Any() {
		fmt.Println("no changes since last index")
		return nil
	}
	fmt.Print(ch.Summary())
	return nil
}

func cmdStats(root string) (retErr error) {
	st, project, readerLock, err := openFor(root)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := readerLock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()
	defer st.Close()
	n, e, err := st.Stats(project)
	if err != nil {
		return err
	}
	fmt.Printf("project=%s nodes=%d edges=%d\n", project, n, e)
	if manifest, manifestErr := index.ReadManifest(st.DBPath()); manifestErr == nil {
		fmt.Printf("status=%s\n", manifest.Status)
	} else {
		fmt.Printf("status=unknown (manifest unavailable: %v)\n", manifestErr)
	}
	return nil
}

// cmdInstall registers this binary as an MCP server in every detected agent
// (Claude Code, Codex, opencode, Grok CLI), and prints manual instructions for the rest.
// Prefer running the installed PATH binary (`codegraph install` after `make install`)
// so agents point at /usr/local/bin/codegraph, not a workspace build artifact.
func cmdInstall() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	fmt.Printf("registering MCP server binary: %s\n", bin)
	outs := install.Run(install.Agents(), bin)
	if len(outs) == 0 {
		fmt.Println("No supported agent detected on PATH (looked for: claude, codex, opencode, grok).")
	}
	for _, o := range outs {
		if o.Installed {
			fmt.Printf("✓ %s — registered codegraph\n", o.Agent)
			continue
		}
		if o.Err != nil {
			fmt.Printf("! %s — auto-register failed (%v); do it manually:\n%s\n", o.Agent, o.Err, o.Manual)
			continue
		}
		fmt.Printf("• %s — needs a manual step:\n%s\n", o.Agent, o.Manual)
	}
	fmt.Println("\n" + install.GenericManual(bin))
	return nil
}

func cmdMCP(root string) error {
	return serveMCP(root, os.Stdin, os.Stdout)
}

func serveMCP(root string, in io.Reader, out io.Writer) error {
	return serveMCPWithHooks(root, in, out, mcpIndexHooks{})
}

func serveMCPWithHooks(root string, in io.Reader, out io.Writer, hooks mcpIndexHooks) error {
	var err error
	root, err = absoluteRepoRoot(resolveRepo(root))
	if err != nil {
		return err
	}
	st, project, readerLock, err := openForWithRetry(root)
	if err != nil {
		return err
	}
	// P1 per-operation locks: a live session holds no lock while idle, so it
	// never blocks a writer. Readers take a brief shared lock only around
	// reopen; the startup lock above serves only the recovery check.
	if err := readerLock.Release(); err != nil {
		_ = st.Close()
		return err
	}
	sp := st.DBPath()
	eng := query.NewEngine(st, project, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := newMCPSession(eng, sp, root, project, ctx, hooks, nil)
	srv := mcp.NewServer(sess.engine(), in, out)
	srv.SetReadiness(sess.gate)
	if err := srv.RegisterTool("refresh",
		"Reindex the repository asynchronously and converge to the latest commit (deduped: a second call while updating reports in-progress instead of stacking work). Edit, then refresh, then query — no restart needed. Poll status for completion.",
		map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
			return sess.refreshAsync(), nil
		}); err != nil {
		_ = eng.Close()
		return err
	}
	if err := srv.RegisterTool("status",
		"MCP index state: state (ready/updating/degraded/failed/unavailable), served generation, last round counts, and whether another process committed a newer generation (call refresh to converge). Safe to call during updates.",
		map[string]any{}, nil, true, func(args json.RawMessage) (string, error) {
			return sess.statusText(), nil
		}); err != nil {
		_ = eng.Close()
		return err
	}

	sess.startInitial()
	serveErr := srv.Serve()
	cancel()
	sess.waitRound(30 * time.Second)
	closeErr := sess.close()
	return errors.Join(serveErr, closeErr)
}

// resolveRepo picks the repo to serve: an explicit path arg wins; otherwise
// CLAUDE_PROJECT_DIR (which Claude Code sets to the project root) when present; else
// the default. So both `codegraph mcp <path>` and a bare `codegraph mcp` work.
func resolveRepo(arg string) string {
	if arg != "" && arg != "." {
		return arg
	}
	if env := os.Getenv("CLAUDE_PROJECT_DIR"); env != "" {
		return env
	}
	return arg
}

// cmdBench reproduces the upstream's measurable headline (token + tool-call
// efficiency) plus our own indexing-speed number. It re-indexes the repo (timing
// it), then asks "who calls X" for the top call hubs and compares the graph
// against two grep-based baselines. Answer-quality (83% vs 92%) is NOT measured —
// that needs an LLM judge; this reports only deterministic numbers.
func cmdBench(root string) error {
	var err error
	root, err = absoluteRepoRoot(root)
	if err != nil {
		return err
	}
	st, project, readerLock, err := openFor(root)
	if err != nil {
		return err
	}
	eng := query.NewEngine(st, project, root)
	defer func() {
		_ = eng.Close()
		if readerLock != nil {
			_ = readerLock.Release()
		}
	}()

	// 1) Indexing speed (our win vs upstream's ~20 min on Windows). Time is
	// measured clean (no MemStats sampling in the loop, which would STW and skew
	// it); memory is read once after, as a footprint — not a sampled peak.
	sp, err := storePath(root)
	if err != nil {
		return err
	}
	t0 := time.Now()
	if err := eng.Close(); err != nil {
		return fmt.Errorf("close store before index: %w", err)
	}
	if err := readerLock.Release(); err != nil {
		readerLock = nil
		return fmt.Errorf("release store before index: %w", err)
	}
	readerLock = nil
	res, err := index.RunAtomic(sp, root)
	if err != nil {
		var reopenErr error
		readerLock, reopenErr = reopenEngineFor(eng, sp)
		if reopenErr != nil {
			return fmt.Errorf("index: %w (reopen store: %v)", err, reopenErr)
		}
		return err
	}
	elapsed := time.Since(t0)
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	readerLock, err = reopenEngineFor(eng, sp)
	if err != nil {
		return err
	}
	// 2) Token / tool-call efficiency over the top call hubs.
	hubs, err := eng.TopByInboundCalls(15)
	if err != nil {
		return err
	}
	corpus, err := bench.LoadCorpus(root)
	if err != nil {
		return err
	}
	var outs []bench.Outcome
	for _, q := range bench.QuestionsFromHubs(hubs) {
		o, err := bench.RunOne(eng, corpus, q)
		if err != nil {
			return err
		}
		o.IndexStatus = string(res.Status)
		outs = append(outs, o)
	}
	sum := bench.Summarize(outs)

	printBench(res, elapsed, m1.HeapInuse, outs, sum)
	return nil
}

func printBench(res index.Result, elapsed time.Duration, heapBytes uint64, outs []bench.Outcome, s bench.Summary) {
	fmt.Printf("# codegraph benchmark — %s\n\n", res.Project)
	fmt.Printf("method=%s (paged product wire format, every real page) · index status=%s\n\n",
		bench.Method, res.Status)
	if res.Status == index.StatusDegraded {
		fmt.Printf("index degraded; resolver failed: %s\n\n", res.Resolver.Summary())
	}

	fmt.Printf("## Indexing speed\n\n")
	fmt.Printf("files=%d nodes=%d edges=%d (dropped %d) · time=%s · %.0f files/s · heap=%dMB (footprint, not peak)\n\n",
		res.Files, res.Nodes, res.EdgesKept, res.EdgesDropped, elapsed.Round(time.Millisecond),
		float64(res.Files)/elapsed.Seconds(), heapBytes/(1024*1024))

	fmt.Printf("## Token efficiency — \"who calls X\" over %d call hubs\n\n", s.N)
	fmt.Printf("| symbol | callers | grep files | graph tok | win tok (×) | file tok (×) |\n")
	fmt.Printf("|---|--:|--:|--:|--:|--:|\n")
	for _, o := range outs {
		fmt.Printf("| `%s` | %d | %d | %d | %d (%.1f×) | %d (%.1f×) |\n",
			o.Question.Name, o.GraphResults, o.MatchFiles, o.Graph.Tokens,
			o.BaselineWin.Tokens, ratioOf(o.BaselineWin.Tokens, o.Graph.Tokens),
			o.BaselineFile.Tokens, ratioOf(o.BaselineFile.Tokens, o.Graph.Tokens))
	}

	fmt.Printf("\n## Summary\n\n")
	fmt.Printf("- **Tokens (median per query):** %.1f× vs grep+window · %.1f× vs grep+file\n",
		s.MedianRatioWin, s.MedianRatioFile)
	fmt.Printf("- **Tokens (total across set):** %.1f× vs grep+window · %.1f× vs grep+file  ← the \"10×\" headline\n",
		s.TotalRatioWin, s.TotalRatioFile)
	fmt.Printf("- **Tool calls (total):** graph %d vs baseline %d → %.1f× fewer\n",
		s.GraphCalls, s.BaselineWinCalls, s.CallRatioWin)
	fmt.Printf("- **Raw tokens:** graph=%d · grep+window=%d · grep+file=%d\n",
		s.GraphTokens, s.BaselineWinTokens, s.BaselineFileTokens)
}

func ratioOf(a, b int) float64 {
	if b == 0 {
		b = 1
	}
	return float64(a) / float64(b)
}

// cmdQuality drives the answer-quality harness:
//
//	codegraph quality gen   <repo> [outdir] [lang]   generate the question set
//	codegraph quality score <dir>                    grade filled truth+answers
//
// `gen` writes questions.json (+ truth/answers scaffolds) for the ultracode
// workflow to fill; `score` reads them back and writes report.md.
func cmdQuality(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: codegraph quality <gen|score>")
	}
	switch args[0] {
	case "gen":
		if len(args) < 2 {
			return fmt.Errorf("usage: codegraph quality gen <repo> [outdir] [lang]")
		}
		repo := args[1]
		outdir := "quality-run"
		if len(args) > 2 {
			outdir = args[2]
		}
		lang := "ts"
		if len(args) > 3 {
			lang = args[3]
		}
		return cmdQualityGen(repo, outdir, lang)
	case "score":
		if len(args) < 2 {
			return fmt.Errorf("usage: codegraph quality score <dir>")
		}
		return cmdQualityScore(args[1])
	default:
		return fmt.Errorf("unknown quality subcommand %q", args[0])
	}
}

func cmdQualityGen(repo, outdir, lang string) error {
	root, err := absoluteRepoRoot(repo)
	if err != nil {
		return err
	}
	st, project, readerLock, err := openFor(root)
	if err != nil {
		return err
	}
	defer func() {
		_ = st.Close()
		if readerLock != nil {
			_ = readerLock.Release()
		}
	}()

	// Index on demand so `gen` is self-contained.
	if n, _, _ := st.Stats(project); n == 0 {
		sp, err := storePath(root)
		if err != nil {
			return err
		}
		if err := st.Close(); err != nil {
			return fmt.Errorf("close store before index: %w", err)
		}
		if err := readerLock.Release(); err != nil {
			readerLock = nil
			return fmt.Errorf("release store before index: %w", err)
		}
		readerLock = nil
		if _, err := index.RunAtomic(sp, root); err != nil {
			var reopenErr error
			readerLock, reopenErr = reopenStoreFor(st, sp)
			if reopenErr != nil {
				return fmt.Errorf("index: %w (reopen store: %v)", err, reopenErr)
			}
			return err
		}
		readerLock, err = reopenStoreFor(st, sp)
		if err != nil {
			return err
		}
	}

	qs, err := quality.Generate(st, project, lang)
	if err != nil {
		return err
	}
	if len(qs) == 0 {
		return fmt.Errorf("no questions generated (is the repo indexed with CALLS edges?)")
	}

	if err := securefile.MkdirAllPrivate(outdir); err != nil {
		return err
	}
	// truth scaffold: one entry per structural question for the oracle to fill.
	var truth []quality.Truth
	for _, q := range qs {
		if q.Type != quality.TypeOpen {
			truth = append(truth, quality.Truth{ID: q.ID, Notes: "oracle: fill Items independently of the graph"})
		}
	}
	if err := writeJSON(filepath.Join(outdir, "questions.json"), qs); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outdir, "truth.json"), truth); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outdir, "answers.json"), []quality.Answer{}); err != nil {
		return err
	}
	meta := map[string]any{"repo": root, "project": project, "lang": lang, "questions": len(qs)}
	if err := writeJSON(filepath.Join(outdir, "meta.json"), meta); err != nil {
		return err
	}

	fmt.Printf("generated %d questions for %s -> %s/\n", len(qs), project, outdir)
	fmt.Printf("  questions.json  the tasks (run the ultracode workflow to fill truth.json + answers.json)\n")
	fmt.Printf("  then: codegraph quality score %s\n", outdir)
	return nil
}

func cmdQualityScore(dir string) error {
	if err := securefile.MkdirAllPrivate(dir); err != nil {
		return fmt.Errorf("prepare private quality directory: %w", err)
	}
	var qs []quality.Question
	var truth []quality.Truth
	var answers []quality.Answer
	if err := readJSON(filepath.Join(dir, "questions.json"), &qs); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(dir, "truth.json"), &truth); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(dir, "answers.json"), &answers); err != nil {
		return err
	}
	report := quality.Report(qs, truth, answers)
	if err := writePrivate(filepath.Join(dir, "report.md"), []byte(report)); err != nil {
		return err
	}
	fmt.Print(report)
	return nil
}

// writePrivate writes a quality-harness artifact with owner-only permissions.
// The helper writes a private temporary file and atomically replaces the
// destination entry, so a destination symlink is replaced rather than
// followed. Content, naming, and CLI flow remain unchanged.
func writePrivate(path string, data []byte) error {
	return securefile.WritePrivate(path, data)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, b)
}

func readJSON(path string, v any) error {
	b, err := securefile.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// cmdCLI: codegraph cli <tool> <path> <json-args>
func cmdCLI(args []string) (retErr error) {
	if len(args) < 2 {
		return fmt.Errorf("usage: codegraph cli <tool> <path> [json]")
	}
	tool, root := args[0], args[1]
	raw := "{}"
	if len(args) > 2 {
		raw = args[2]
	}
	var a struct {
		Query         string `json:"query"`
		Label         string `json:"label"`
		QualifiedName string `json:"qualified_name"`
		File          string `json:"file"`
		StartLine     int    `json:"start_line"`
		EndLine       int    `json:"end_line"`
		Limit         int    `json:"limit"`
		Cursor        string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return fmt.Errorf("bad json args: %w", err)
	}

	root, _ = filepath.Abs(root)
	st, project, readerLock, err := openFor(root)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := readerLock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, releaseErr)
		}
	}()
	eng := query.NewEngine(st, project, root)
	defer eng.Close()

	// Ref/snippet tools print one page plus the `#` trailer line (has_more +
	// cursor + generation). Both are already token-minimal — no JSON wrapper.
	var out string
	refText := func(p query.RefPage, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return p.WireText(), nil
	}
	switch tool {
	case "search":
		out, err = refText(eng.SearchPage(a.Query, a.Label, a.Limit, a.Cursor))
	case "callers":
		out, err = refText(eng.CallersPage(a.QualifiedName, a.Limit, a.Cursor))
	case "callees":
		out, err = refText(eng.CalleesPage(a.QualifiedName, a.Limit, a.Cursor))
	case "neighbors":
		out, err = refText(eng.NeighborsPage(a.QualifiedName, a.Limit, a.Cursor))
	case "similar":
		var page query.RefPage
		page, err = eng.SimilarPage(a.QualifiedName, a.Limit, a.Cursor)
		if err == nil {
			out = page.WireText()
			if page.Notice != "" {
				out = page.Notice + "\n\n" + out
			}
		}
	case "dead_code":
		out, err = refText(eng.DeadCodePage(a.Limit, a.Cursor))
	case "get_architecture":
		var arch query.Architecture
		arch, err = eng.Architecture(a.Limit)
		if err == nil {
			out = query.RenderArchitecture(arch)
		}
	case "snippet":
		var pg query.SnippetPage
		pg, err = eng.SnippetPage(a.File, a.StartLine, a.EndLine, a.Limit, a.Cursor)
		if err == nil {
			out = pg.WireText()
		}
	default:
		return fmt.Errorf("unknown tool %q", tool)
	}
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}
