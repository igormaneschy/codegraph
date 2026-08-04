# codegraph — architecture (our Go design)

A small, token-efficient code knowledge graph for AI agents. Inspired by
DeusData/codebase-memory-mcp (see `UPSTREAM.md`), but deliberately scoped down to
**our stack** (TypeScript/JS + Go + NestJS) and a maintainable Go codebase.

## Design principles

1. **The storage is trivial; the value is in the edges.** Two tables (`nodes`,
   `edges`) + FTS5 — same as upstream. We do not over-invest here.
2. **Token-efficiency by construction.** Every query returns a *compact ref*
   (`name + qualified_name + label + file + line`), **never source code**. The
   agent calls `snippet` only when it must read code. That selectivity is the
   entire point — it's where the 10× token saving comes from.
3. **Borrow the type checker, don't rebuild it.** Upstream re-implemented type
   resolution in C ("Hybrid LSP") across 9 languages — months of work. Our key
   bet: **delegate call resolution to the language's own batch indexer** —
   `scip-typescript` for TS/JS, `go/packages` + `go/callgraph` for Go (the libs
   gopls itself calls), read in-process. Same type-checker accuracy as interactive
   LSP but in one pass, with no long-lived server to babysit — and we skip the
   hardest part of the port.
4. **Honest precision.** Unresolved edges are dropped (endpoints must exist in
   the graph), so we never fabricate a call edge. Better a missing edge than a
   wrong one.
5. **Small dependency surface.** Pure-Go SQLite (`modernc.org/sqlite`), stdlib MCP
   server. The heavy deps are tree-sitter (cgo — the binary needs `CGO_ENABLED=1` +
   gcc) for M1 and the SCIP bindings + `go/packages` for M2; scip-typescript is a
   build-time tool (Node), not linked into the binary.

## Storage (internal/graph)

Mirrors upstream exactly so a future `.db` is shape-compatible:

```sql
nodes(id, project, label, name, qualified_name, file_path,
      start_line, end_line, properties JSON, UNIQUE(project, qualified_name))
edges(id, project, source_id, target_id, type, properties JSON,
      UNIQUE(source_id, target_id, type))
nodes_fts  -- FTS5(name, qualified_name, label, file_path) → BM25
-- indexes on edges(source, target, type, +composite) and nodes(label, name, file)
```

`Store` (internal/graph/store.go) is the only thing that touches SQL:
`InsertNodes` (keeps FTS in sync), `InsertEdges` (resolves QN→id, drops
unresolved), `Search` (BM25), `Neighbors` (in/out/both, the basis for
callers/callees), `Snippet` (reads file lines), `Stats`, `FileHashes`,
`ForEachCallEdge` (streaming CALLS for incremental reuse), `ReplaceProject`,
`DBPath`, `Reopen`, `Checkpoint` (finalizes WAL before an atomic file
replacement), `ValidateIntegrity` (exact SQLite/FTS/properties/endpoint
validation), `LogicalGraphDigest` (deterministic content digest), and
`BeginReadSnapshot`/`EndReadSnapshot` (pin a WAL read transaction — used by tests
to prove a live reader vetoes `RunAtomic`'s replacement while holding the WAL).

## Indexing pipeline (internal/index)

One atomic core (`runAtomicContext`) backs both entry points:

- **`RunAtomicContext(dbPath, root)`** — the CLI/MCP **strict production path**
  (`RunAtomic` is its non-cancellable form). Builds into `dbPath+.building` plus a
  sidecar manifest at `dbPath+.manifest.json` (written via tmp+fsync+rename), and
  installs both only after exact validation, WAL checkpoint, and a two-file commit.
  The live graph is never erased: a failure before the commit preserves the previous
  graph+manifest pair, while a failure or cancellation after the manifest-first
  install can leave the new manifest next to the old graph — an identity mismatch
  that forces a rebuild on the next run (fail-closed). Cleanup of `.building` /
  `.manifest.building` is attempted even on failure; cleanup errors are reported
  and any leftovers are reconciled at the next start. A non-blocking per-database
  lock (`dbPath+.lock`; shared for readers, exclusive for the writer) serializes
  competing indexers — CLI vs MCP, same process or separate — and excludes readers
  for the brief two-file commit window.
- **`Run(store, root)`** — tests and direct store use. Closes the caller's store,
  runs the same atomic core with **strict freshness disabled** (lighter no-op gate,
  no post-build integrity pass), then reopens the store. It is not the production
  certification path: production no-ops are certified only by `RunAtomic`'s strict
  manifest+integrity gate.

```
prepareIndexingContext(store, root, strict)
  scanRepositoryContext         one candidate-aware walk feeding discovery, resolver
                                scopes, and manifest inputs from a single observation;
                                soft-ignored dirs stay traversable so a config below
                                an ignored dir is still fingerprinted; tsconfig
                                extends/references are expanded (missing/circular/
                                escaping → error)
  validateRepositoryObservation re-scan until two consecutive scans agree (bounded
                                retries); an unstable repository fails closed
  no-op gate (strict)           freshManifestFor: analysis/input identity matches,
                                graph file identity (native: dev/ino on Unix, file
                                indices on Windows, metadata fallback) matches,
                                logical graph digest matches, and
                                Store.ValidateIntegrity passes — the exact validator
                                is the LAST operation before a no-op is certified;
                                any miss → full rebuild
  changed scopes                per-scope CALLS reuse decision; a manifest/identity
                                mismatch invalidates ALL scopes (no old-edge reuse)
  reuseFrom = main              only when the live graph is a trusted snapshot; a
                                freshness miss keeps the live handle out of reuseFrom
                                (build from scratch; old graph untouched)
runPipelineContext(store, in)
  ReplaceProject                wipe project nodes/edges/FTS on the .building store
  → indexDefinitionsBatched     tree-sitter defs in bounded batches (memory.MaxWorkers)
  → collectImportsStreaming     IMPORTS edges flushed per file (TS/JS)
  → resolveTSCalls              scip-typescript for changed tsconfig scopes
  → resolveGoCalls              go/packages + VTA for the changed Go scope
  → resolveRubyCalls            ruby-static literal subset for the changed Ruby scope
  → resolver report             ValidateExpected — the report must cover exactly the
                                expected scopes; HasFailures → ResolverFailure
                                (existing graph preserved, status stale) or an
                                explicit degraded structural-only first graph
  → insertReusedCallEdges       unchanged scopes' CALLS from reuseFrom, in batches
  → resolveSimilarFromSpans     SIMILAR_TO from function spans (skipped on low-RAM)
  memory.Gate()                 between every heavy phase — returns freed pages to the OS
post-build (strict path only)
  validateStoreIntegrity        exact validator on the built store
  LogicalGraphDigest            deterministic content digest (no rowids/inode/WAL)
  Checkpoint + Close            WAL finalized before the file is installed
  graphIdentity + writeManifestFile    sidecar via tmp + fsync + rename
  two-file commit               main Checkpoint/Close → manifest replace FIRST →
                                commitBuiltIndex (SQLite sidecars removed, platform
                                replace = the cancellation linearization point)
  cleanup (defer)               remove .building/.manifest.building; release lock
```

### Freshness & integrity (sidecar manifest)

The graph tables are unchanged; freshness is a sidecar contract. A valid run commits
`dbPath+.manifest.json` (manifest v2, schema `nodes-edges-fts5-v1`) next to the graph,
recording: analysis identity (schema/analysis/discovery/resolver versions), the
canonical root, a **fingerprint of every graph-affecting sidecar input** (path +
sha256, sorted) — configuration, dependency, topology, and ignore files; source
files are tracked separately, by per-file hashes kept on their nodes and compared
during change observation — the **graph file identity** (native platform identity:
dev/ino on Unix, file indices on Windows, file metadata fallback — stable across
ordinary WAL writes, changes only when a different database file is installed), the
**logical content digest** (`LogicalGraphDigest` over nodes/edges with canonical
properties), and the status/resolver report. Fingerprinted inputs include every
`tsconfig*.json` /
`jsconfig*.json` (with `extends`/`references` expanded; package references resolve
through repository-local `node_modules` only, never outside the repository),
`go.mod`/`go.sum`/`go.work`, `package.json` + lockfiles, `Gemfile`/`Gemfile.lock`,
`.ruby-version`, workspace manifests (`pnpm-workspace.yaml`, `turbo.json`, `nx.json`,
`lerna.json`, `rush.json`, `workspace.json`), and the ignore rules themselves
(`.gitignore`, `.cbmignore`) — a topology change can alter resolver scopes without
touching a single source file.

Freshness is **fail-closed**: a missing, malformed, version-mismatched, or
identity-mismatched manifest is an ordinary freshness miss that forces a rebuild,
never a certified no-op. The no-op gate runs the **exact validator** — SQLite
`PRAGMA integrity_check`, FTS5's own integrity-check, nodes↔FTS row-id
correspondence, FTS postings compared against a freshly rebuilt index, valid JSON
`properties`, and project-consistent edge endpoints — as the last operation before
certifying. Resolver reports must cover **exactly the expected scopes**
(`ValidateExpected`): a healthy manifest recording a failed resolver scope is
rejected, and a degraded manifest must record a failure. A repository that keeps
mutating during the validating re-scan fails closed after the bounded retries. The
linearization point is explicit: mutations after it are not claimed impossible —
they are observed by the next run. No probabilistic validation is involved anywhere.
As a point-in-time fact, `go test -race ./... -timeout 20m` passed after the
FTS-optimized integrity validator landed; that single run is not a guarantee for
future or timeout-less executions.

`ExtractDefinitions` (definitions.go + treesitter.go) parses each file with the
official **tree-sitter** (cgo, one parser per goroutine) and emits `File`/`Function`/
`Method`/`Class`/`Interface`/`Type`/`Enum`/`Variable` nodes + `DEFINES` edges — with
real end lines, `is_exported`, and class/method decorators. `ResolveImports`
(imports.go) resolves relative TS/JS imports to File nodes → `IMPORTS` edges (package
and unresolved imports drop). `ResolveCalls` (calls.go) emits `CALLS` edges via the M2
batch indexers — scip-typescript for TS/JS (`internal/scip`) and go/packages + a VTA
call graph for Go (`internal/gocalls`) — dropping callees that aren't known graph symbols.
Incremental (M3, incremental.go): `DetectChanges` gates a no-op when nothing changed, and
a re-index re-resolves only the changed scopes, reusing the stored CALLS edges of the rest
via `forEachReusableCallEdge` + batched `insertReusedCallEdges`. Ruby File nodes carry a
`ruby_analysis_version`; changing it forces one full Ruby analysis rebuild even when source
hashes are unchanged, so parser/resolver upgrades are visible without requiring an edit.

### Memory budget (internal/memory)

Indexing auto-tunes at process start from installed RAM (and WSL detection): worker
count, definition batch size, Go `debug.SetMemoryLimit`, scip-typescript
`--max-old-space-size`, and optional `SkipSimilar` on constrained hosts.
`CODEGRAPH_*` env vars override for debugging only — users need not set anything.
`memory.Gate()` runs between pipeline phases (and after each scip scope) to hand freed
heap back to the OS, which matters for the long-running MCP server after a large index.

M4 enrichment: `ResolveSimilar` (similar.go) emits `SIMILAR_TO` near-clone edges from a
MinHash signature + LSH banding over each function's token shingles (`internal/similar`,
no embeddings). The definitions pass also stamps McCabe cyclomatic complexity onto each
Function/Method (`complexity.go`, one tree-sitter subtree walk) into `properties.complexity`.
The Go/TS call resolvers credit calls inside closures to the enclosing named function and
keep recursive self-edges — recall fixes that took intra-repo callers to ~100% (see
`docs/QUALITY.md`).

M5: the definitions pass also emits **`Route` nodes** from NestJS decorators
(`routes.go`) — `@Controller` base + `@Get/@Post/...` path → `<VERB> <path>`, located at
the handler. `get_architecture` (`query/architecture.go`) aggregates the stored graph
into a one-shot repo map (languages, node/edge counts, top packages, complexity/call
hotspots) — the orientation call. `HTTP_CALLS` (client → route) is deferred to M6: it
would be heuristic string matching, not type-checker-delegated.

Ruby source (`.rb`, `.rake`, `.ru`, `.rbi`) uses the official tree-sitter Ruby grammar.
The definitions pass emits explicit `Module`, `Class`, `Constant`, and instance
(`Owner#method`) or singleton (`Owner.method`) `Method` nodes. It does not infer
Rails-generated methods or metaprogrammed declarations. Literal `get`/`post`/`put`/
`patch`/`delete`/`options` calls in `config/routes.rb` emit `Route` nodes; literal
hash-rocket verb forms and `root "controller#action"` emit routes too. Only calls
inside `Rails.application.routes.draw` with non-empty, non-interpolated literal paths
are accepted. The extractor also recognizes raw `head` calls, but Rails 8 exposes that
verb through `match ... via:` rather than a direct DSL method, which remains out of
scope. Literal `namespace` and `scope` path prefixes compose with
nested routes. Resourceful, interpolated, and dynamically prefixed routes are
deliberately skipped until a Rails-aware resolver can validate them. Non-nested
`resources` and `resource` declarations expand their literal `only`, `except`, `path`,
and `path_names` options through a fixed REST table; literal `%i[...]` action lists
are accepted. Nested and shallow resources stay
out of scope because their parent parameter names require Rails-compatible inflection.
Ruby also emits a deliberately narrow static `CALLS` subset: an absolute constant
receiver (for example, `::Payments::Gateway.authorize`) maps only to one explicit
repository singleton-method declaration. The resolver runs after definitions are
stored, attributes the source location to its containing method span, and tags the
edge `resolver=ruby-static`, `confidence=high`. Lexical constants, variables,
instance receivers, bare calls, `send`, and duplicate declarations are dropped; an
optional type-aware resolver remains future work for broader coverage.
Literal Ruby `require_relative` calls resolve to existing repository-local `.rb` files
as `IMPORTS` edges in a Ruby-specific pass. Literal `require` resolves only through
paths explicitly proven in `config/application.rb`: Rails must enable
`config.add_autoload_paths_to_load_path` and append a literal `Rails.root.join(...)`
path to `config.autoload_paths`. Package, dynamic, and ambiguous load paths remain
excluded.
Literal Rails `to: "controller#action"` values emit `HANDLES` edges from Route nodes to
conventionally located controller methods. Literal root targets and hash-rocket verb
targets use the same mapping. Namespace and `scope module:` blocks retain their route
nodes but drop `HANDLES` until their controller context is modeled; the same applies
to `scope controller:`. Missing, dynamic, and malformed targets drop.

## Query layer (internal/query)

`Engine` exposes the agent-facing operations: `Search`, `Callers`, `Callees`,
`Neighbors`, `Similar`, `DeadCode` (each returning `[]Ref`), `Architecture` (the repo
map — languages/counts/packages/hotspots, rendered compactly), `Snippet`, and
`DetectChanges`. `Close`/`Reopen` wrap the underlying `Store` — the MCP server closes
before a background `RunAtomic` (Windows file lock), then reopens the committed graph.
This is the contract both the CLI and the MCP server use, so behavior is identical
across entry points. Relationship queries default to limit 500 (a hub can have hundreds
of callers — a low cap would silently truncate the answer).

## MCP server (internal/mcp)

Minimal stdio JSON-RPC 2.0 (newline-delimited — the MCP convention), stdlib only.
Handles `initialize`, `tools/list`, `tools/call`. Tools: `search`, `callers`,
`callees`, `neighbors`, `similar`, `dead_code`, `snippet`, `detect_changes`. Swap for
`github.com/mark3labs/mcp-go` if it grows.

The `mcp` command (M5) auto-indexes in a background goroutine on startup and gates
tool calls behind a readiness check (`Server.SetReadiness`) — the handshake answers
immediately, tools report "indexing" until the graph is built, never a half-written
store. On index failure, `RunAtomic` leaves the previous graph on disk; the server
attempts to reopen it and becomes ready only if the reopen succeeds, prepending the
failure status to every tool response (stale-data context); if the reopen itself
fails, the server reports the error and stays not ready. Do not run `codegraph index`
on the same repo while MCP is auto-indexing — both contend for the same store file.
The repo is resolved from `$CLAUDE_PROJECT_DIR` (set by Claude Code) or cwd, so one
registration serves any repo. `codegraph install` (`internal/install`) registers the
server into detected agents — Claude Code/Codex via their add-CLI, opencode via a
config-file merge — and prints a manual snippet for the rest.

## CLI (cmd/codegraph)

```
codegraph index   <path>               atomic build (RunAtomic; no-op only when the
                                       strict manifest+integrity gate certifies freshness)
codegraph stats   <path>               node/edge counts
codegraph changes <path>               files changed since the last index
codegraph install                      register the MCP server into detected agents
codegraph mcp     <path>               serve MCP over stdio (auto-indexes in background)
codegraph cli     <tool> <path> <json> run one query tool (no MCP)
```

Store path: `~/.cache/codegraph/<project>.db`. Project slug derived from the
absolute repo path (matches upstream convention).

## Package layout

```
cmd/codegraph/        CLI entrypoint + subcommands (index/stats/mcp/bench/quality/cli)
internal/graph/       model.go (Node/Edge/labels/edge-types) + store.go (SQLite)
internal/index/       discover.go, path.go, manifest.go, resolver.go, lock.go (+ platform files), atomic_helpers.go, definitions.go + treesitter.go + complexity.go + routes.go, imports.go, calls.go, ruby_calls.go, similar.go, incremental.go, prepare.go, pipeline.go
internal/memory/      auto-tuned indexing RAM budget + Gate() between phases
internal/scip/        scip-typescript runner + SCIP→CALLS attribution (TS/JS, M2)
internal/gocalls/     go/packages + VTA call graph → CALLS (Go, M2; cha.go = generics-safe)
internal/similar/     MinHash signature + LSH banding → SIMILAR_TO near-clone edges (M4)
internal/query/       query.go (Engine → compact Refs)
internal/mcp/         server.go (stdio JSON-RPC + auto-index readiness gate)
internal/install/     register the MCP server into detected agents (M5)
internal/bench/       token/tool-call/speed benchmark harness
internal/quality/     answer-quality harness (question gen + scoring)
docs/                 UPSTREAM.md, ARCHITECTURE.md, ROADMAP.md, QUALITY.md, BENCHMARK.md
_upstream/            shallow clone of the original (gitignored, reference only)
```

## What we deliberately are NOT building (yet)

158 languages (→ just our stack), the in-binary embeddings + `semantic_query`
(was the 20-min bottleneck; MinHash/LSH is enough for SIMILAR_TO), the full
Cypher engine (→ fixed query shapes cover ~90% of agent use), C-style arena
allocators (Go GC + goroutines is simpler and fast enough at our repo scale).
