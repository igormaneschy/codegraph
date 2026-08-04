# codegraph — roadmap

Milestones, smallest-useful-first. Each one ships something runnable.

## M0 — Scaffold ✅ (done)

- Two-table SQLite store + FTS5, mirroring upstream schema.
- File discovery (hard-ignores + `.cbmignore`), language detection (Go/TS/JS).
- Regex definitions pass → `File`/`Function`/`Method`/`Class` nodes + `DEFINES` edges.
- Parallel per-file extraction (NumCPU goroutines).
- Query engine (search / callers / callees / neighbors / snippet) → compact Refs.
- Minimal MCP stdio server + CLI (`index`, `stats`, `mcp`, `cli`).
- **Proof:** `codegraph index .` on itself → 9 files, 75 nodes, 66 edges; `search`
  returns correct file+line refs.

## M1 — Real ASTs via tree-sitter ✅ (done)

Replaced the regex extractor with the **official** `github.com/tree-sitter/go-tree-sitter`
(cgo) + Go/TS/TSX grammars (not smacker — it had drifted since ~2023).

- Accurate node boundaries (true `end_line`), receiver/owner-qualified names so
  homonyms disambiguate, `is_exported`, `is_test`.
- Full TS surface: Function/Method/Class + Interface/Type/Enum/Variable, abstract
  classes, function expressions. Decorators on classes AND methods (NestJS
  `@Injectable`/`@Controller`/`@Get`/`@Post`) — captured generically as strings,
  so any decorator framework (Angular, TypeORM) comes for free.
- `IMPORTS` edges (TS/JS): relative specifiers resolved File→File; package/unresolved
  imports dropped (honest precision). Go imports deferred (package-level model).
- Build needs cgo: gcc on PATH + `CGO_ENABLED=1` (WinLibs mingw-w64 on this machine).
- **Result vs upstream on ajuda-aqui (857 files, 3.1s):** Interface 393=393, Type
  141=141, decorators 363=363, Method 1052≈1053, Class 343≈344. Divergences
  (Function, Variable, File) are deliberate scope (top-level only, ignored dirs).
- **Scope left out (not bugs):** nested functions/callbacks, property decorators
  (`@Column`), Go interface-vs-struct (both → Class).

Design note: **codegraph indexes the LANGUAGE (TS/JS/Go), not frameworks.** All of
NestJS/Next/Electron/RN/Expo/Fastify/Tauri are TS/JS — symbols/imports/calls work
for all of them with zero per-framework code. Framework semantics (HTTP routes,
IPC, DI) are an optional, pluggable pass added only on real need.

## M2 — CALLS edges (the big one) ✅ (done)

The hard, valuable part — done via BATCH indexers (NOT interactive LSP), which give
the same type-checker precision in one pass with far less Go code and no long-lived
process to babysit:

- **Go → in-process** (`internal/gocalls`), no gopls subprocess:
  `golang.org/x/tools/go/packages` (LoadAllSyntax) + `go/callgraph` (CHA — sound on
  libraries without a `main`). These are the libs gopls itself calls.
- **TS/JS → `scip-typescript` (batch)** (`internal/scip`) → read `index.scip`
  in-process via `github.com/scip-code/scip/bindings/go/scip`. SCIP emits reference
  occurrences, not call edges → an enclosing-range attribution pass (`BuildEnclosing`
  + `CallEdges`) turns them into caller→callee CALLS. One scip run per tsconfig
  subproject (monorepo), or at the root for a single-package repo.
- `internal/index/calls.go` `ResolveCalls` wires both, best-effort per subproject;
  unresolved callees are dropped (filtered to known Function/Method QNs) and edges
  carry `resolver`/`confidence` tags so compiler-resolved edges supersede heuristics.

**Exit criteria — MET:** `callers`/`callees` correct on the ajuda-aqui
validation-codes module, the 4 same-named `getActiveCode` disambiguated (commits
13ab98f, 8eb84f9).

**Go precision — RESOLVED.** TS/JS scores ~89% (scip is precise). Go started at ~79%
(CHA over-approximates interface/func-value dispatch); fixed by **VTA** refining CHA
plus loading test files (`packages Tests:true`) in `internal/gocalls`. Now ~88% mean /
85% callers / 92% callees, scored **intra-repo** — stdlib/dep callees are excluded from
the truth because the graph drops them by design, exactly as the upstream does and as
its own benchmark grades them (PARTIAL, not FAIL). See `docs/QUALITY.md`.

**NestJS DI blind spot (any resolver):** `@Inject('TOKEN')`, `useClass`/`useFactory`
bindings live in a `providers` array and are invisible to scip and every other
resolver — they'd need a dedicated framework-semantic pass (deferred, M5).

## M3 — Incremental indexing ✅ (done)

Re-indexing is cheap to repeat — the expensive whole-project CALLS pass (scip /
go+VTA) no longer re-runs when it doesn't have to.

- **Per-file content hash** (sha256) on the File node; `DetectChanges` compares the
  files on disk against it → Changed/Added/Deleted.
- **No-op when unchanged** — `Run` skips the whole pipeline if nothing changed
  (cobra: 1.77s full → 0.06s reused, ~29×); the win scales with the CALLS cost.
- **Scope-gated CALLS** — a scope is one tsconfig-project (scip) or the Go module
  (go+VTA). A re-index re-resolves only the scopes whose files changed and reuses the
  stored edges of the rest (read before the wipe via `Store.CallEdges`, kept by
  `scopeOf`/`changedScopes`). Editing one app of a monorepo no longer re-runs scip for
  every other app. The full-index path is unchanged (a never-indexed project marks
  every scope changed → nothing reused).
- **`detect_changes` tool + `codegraph changes <repo>`** — report the change set
  (compact TSV) so an agent can tell whether the graph is stale before trusting it.

Honest limit: true *per-file* CALLS incrementality is impossible (the resolvers are
whole-scope), so the granularity is the scope, not the file — the realistic win.

## M4 — Similarity + light enrichment ✅ (done)

- **`SIMILAR_TO`** via MinHash + LSH over token shingles (`internal/similar`) — near-clone
  detection, no embeddings/model. Wired into the pipeline (threshold 0.7) and surfaced
  by the **`similar`** query/tool. cobra found +231 real near-clones (the cross-shell
  `Gen*CompletionFile` cluster).
- **Cyclomatic complexity** (McCabe) in `properties.complexity` on every Function/Method,
  from the tree-sitter subtree (`internal/index/complexity.go`). Cognitive/loop-depth
  deferred (YAGNI); the hotspots query that reads this is M5. 
- **Dead-code hint** — `dead_code` query: Function/Method with zero inbound `CALLS`,
  minus entry points (exported, decorated, main/init, tests). A candidate list (recall-
  bounded), not a delete list; on cobra it pinpoints `appendIfNotPresent`, which cobra's
  own source marks unused-and-removable.

**Bonus — the dead-code hint exposed a call-graph recall hole, and fixing it lifted Go
quality.** Calls written inside closures (cobra's `Run: func(){...}`) and recursive
self-calls were dropped, so they showed as false dead code. Crediting closure calls to
the enclosing named function (`ssa.Function.Parent()`) + keeping recursive self-edges
(while `dead_code` ignores them) took cobra callers **85→100%** (mean 88→94%), zero
false positives. Also raised the relationship-query default limit 50→500 so hub answers
aren't silently truncated. See `docs/QUALITY.md`.

## M5 — MCP polish + distribution ✅ (done)

The graph becomes a tool you actually use, in any repo, from any agent.

- **Auto-index on serve** — `codegraph mcp` indexes in a background goroutine and
  gates tool calls behind a readiness check (`Server.SetReadiness`), so a freshly-
  registered server "just works" on any repo (no manual `index` step); M3's no-op keeps
  it cheap every launch. Resolves the repo from `$CLAUDE_PROJECT_DIR` or cwd.
- **`codegraph install`** — registers the MCP server into detected agents: Claude Code
  & Codex via their add-CLI (user scope, any repo), opencode via a config-file merge
  that preserves existing config; a manual snippet for the rest (`internal/install`).
- **`get_architecture`** — one-shot repo map from graph aggregates: languages, node/
  edge counts, top packages by symbol, and hotspots (most complex functions — reads
  the M4 `properties.complexity` — + most-called hubs). Community detection deferred
  (YAGNI — dir grouping gives the structure cheaply).
- **HTTP Route nodes** — NestJS `@Controller` + `@Get/@Post/...` → `Route` nodes named
  `<VERB> <path>`, located at the handler (`internal/index/routes.go`). Surfaced via
  `search --label Route` and counted by get_architecture.

Dogfooded: registered into Claude Code/Codex/opencode and used live on two real repos —
a Go app (aurelia) and a TS/Next monorepo (LuminaSoft). That caught and fixed three
real issues: MCP `required:null` (broke tools/list), the opencode config path, and
**discovery not honoring `.gitignore`** (it indexed a repo's vendored Go module cache —
124k nodes / ~4 min — until fixed; now 5k / 12s). Both stacks resolve calls correctly
(cn/Button in TS, NewRegistry/ClassifyAPIError in Go); the only residual noise is
committed generated/built code (Prisma client, minified bundles), handled by one
`.cbmignore` line — no heuristic needed. See `docs/QUALITY.md`.

## Post-M5 hardening ✅ (v0.2.0)

Production fixes shipped after dogfooding on large repos and long-running MCP sessions:

- **Atomic index builds** — `RunAtomic` writes to `*.building` and renames on success;
  CLI `index` and MCP background index use this path so a failed re-index never wipes
  the previous graph.
- **Memory-budget indexing** (`internal/memory`) — auto-tuned workers, batch size, Go
  heap limit, and scip Node heap cap from installed RAM; `memory.Gate()` between phases;
  batched definitions + streaming imports; SIMILAR skipped on low-RAM hosts.
- **CALLS reuse gated by freshness** — unchanged scopes' edges are streamed from the
  live graph store (`reuseFrom`) into the `.building` store in batches; a freshness
  miss (unreadable/mismatched manifest, digest or integrity failure) disables reuse
  entirely, so old CALLS are never certified against a newer repository.
- **MCP failure recovery** — on index failure the server attempts to reopen the
  intact graph and becomes ready only if the reopen succeeds, prepending the failure
  status to responses (stale-data context); if the reopen fails, it reports the error
  and stays not ready. Closes the live DB handle before `RunAtomic` (Windows file
  lock).
- **`Store`/`Engine` `Reopen`** — canonical reopen after atomic commit; `ScopesRun`
  counts only successful scip scopes.

## Post-M5 hardening — freshness & integrity ✅

The sidecar manifest turns "nothing changed" from a guess into a certified no-op:

- **Sidecar manifest** (`<db>.manifest.json`, v2) — records analysis identity
  (schema/analysis/discovery/resolver versions), the canonical root, a fingerprint of
  every graph-affecting **sidecar input** (path + sha256, sorted) — configuration,
  dependency, topology, and ignore files; source files are tracked separately by
  per-file hashes on their nodes, compared during change observation — the graph
  file identity (platform-native: dev/ino on Unix, file indices on Windows, metadata
  fallback; changes only when a different database file is installed), the logical
  content digest, and the status/resolver report. Written via tmp+fsync+rename and
  committed **before** the graph replace; a crash between the two is detected by the
  identity mismatch on the next run (fail-closed, never a certified no-op).
- **Input/topology fingerprint** — `tsconfig*.json`/`jsconfig*.json` (with
  `extends`/`references` expanded, package references resolved only through repo-local
  `node_modules`), `go.mod`/`go.sum`/`go.work`, `package.json` + lockfiles,
  `Gemfile`/`Gemfile.lock`, `.ruby-version`, workspace manifests (`pnpm-workspace`,
  `turbo`, `nx`, `lerna`, `rush`, `workspace.json`), and the ignore rules themselves.
  A topology change alters resolver scopes without touching any source file, so these
  are fingerprinted even below soft-ignored directories.
- **Stable bounded observation** — discovery, resolver scopes, and manifest inputs
  come from one candidate-aware walk; a validating re-scan retries up to a bounded
  number of times and fails closed on an unstable repository (no no-op, no rebuild
  from a half-observed repo).
- **Exact no-op gate** — `RunAtomic` certifies a no-op only when the fingerprint,
  graph identity, `LogicalGraphDigest`, and `Store.ValidateIntegrity` (SQLite
  `integrity_check`, FTS5 integrity-check, nodes↔FTS row-id correspondence, FTS
  postings vs a rebuilt index, JSON properties, edge endpoints) all pass, with the
  exact validator as the last operation. Mutations after the linearization point are
  not claimed impossible — they are caught by the next run. As a point-in-time fact,
  `go test -race ./... -timeout 20m` passed after the FTS-optimized integrity
  validator landed; that single run is not a guarantee for future or timeout-less
  executions. No freshness metric beyond the real gates is claimed.
- **Resolver reports are complete** — every committed manifest covers exactly the
  resolver scopes the repository implies (`ValidateExpected`); a healthy manifest
  with a failed scope is rejected, and a degraded manifest must record a failure. A
  resolver failure preserves the previous graph (status stale via `ResolverFailure`)
  or commits an explicit degraded structural-only first graph (partial CALLS
  removed) — never a healthy graph hiding a failure.
- **Index lock + replacement recovery** — non-blocking per-database lock (shared
  readers / exclusive writer) serializes CLI vs MCP indexers and excludes readers
  during the two-file commit; interrupted replacements are recovered on the next
  open; cancellation gates stop before the linearization point and cleanup of
  `.building`/`.manifest.building` is attempted even on failure — cleanup errors are
  reported and any leftovers are reconciled at the next start.

## Current focus - Ruby and Rails

Ruby and Rails are the next product priority. The completed M1 structural baseline,
the staged implementation plan, evaluation gates, and non-goals are maintained in
[`RUBY_ROADMAP.md`](RUBY_ROADMAP.md). M6 remains deferred until that roadmap reaches
its release gate.

## M6 — deferred from M5 (do when proven)

- ⬜ **`HTTP_CALLS`** (client call-site → `Route`). Deferred deliberately: unlike the
  rest of the graph it is **not** type-checker-delegated — it's heuristic string
  matching (extract a URL from `fetch`/`axios`/`HttpClient` calls, match against route
  patterns). Dynamic URLs (`/users/${id}`) make it low-recall, and a wrong match
  violates honest precision ("a missing edge beats a wrong one"). Revisit only if a
  high-precision, literal-anchored version proves worth it.
- ⬜ Optional: committable `graph.db.zst` team artifact (zstd snapshot + bootstrap).

## Ruby M1 — structural indexing + verified static calls ✅

- Ruby discovery covers `.rb`, `.rake`, `.ru`, and `.rbi`.
- The official tree-sitter Ruby grammar emits explicit `Module`, `Class`, `Constant`,
  instance-method, and singleton-method definitions. Ruby metaprogramming remains out
  of scope for this structural pass.
- Literal HTTP verb routes in `config/routes.rb` become `Route` nodes. Resourceful,
  namespaced, and interpolated routes are deferred rather than guessed.
- Absolute-constant singleton calls emit `CALLS` only when a unique explicit
  repository target exists. Broader Ruby calls still require an optional semantic
  resolver.

## Stretch / maybe-never (YAGNI unless proven)

- On-device embeddings + `semantic_query` (heavy; was the upstream bottleneck).
- Full Cypher engine (fixed query shapes cover ~90%).
- 158 languages, IaC/K8s indexing, cross-repo `CROSS_*` edges, 3D graph UI.

## Open design questions

- LSP session management: one long-lived server per language vs per-index spawn?
- TS monorepo: one tsserver per tsconfig, or project-references aware?
- Edge confidence scoring (upstream scores HTTP matches) — worth it for CALLS?
- Store per-project files vs one multi-project DB (upstream supports both).
