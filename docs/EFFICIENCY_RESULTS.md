# Efficiency baseline — P0 results

Date: 2026-09-07. Status: P0 done; no runtime change in this unit.
Plan: `docs/EFFICIENCY_PLAN.md`. All commands below were run from the repo root
unless noted. Wall times are machine-specific observations, not CI thresholds.

## 1. Environment

| Item | Value |
| --- | --- |
| Commit | `1a4bd7e` (`docs(plan): add phased efficiency implementation plan`) |
| Worktree | clean except pre-existing `M CLAUDE.md`, `M CONTRIBUTING.md` (fork authorship) + untracked `.DS_Store`, `internal/.DS_Store` (left alone) |
| Go | `go1.27.0 darwin/arm64` (`go version`) |
| Node / npm | `v26.0.0` / `11.12.1` |
| scip-typescript | **not on PATH**; indexer auto-runs `@sourcegraph/scip-typescript@0.4.0` via npx (`internal/scip/run.go:21,126`); manifest records `scip_resolver_version: scip-typescript-bridge-v1@0.4.0` |
| Go resolver | `go-vta-resolver-v2`; Ruby `ruby-analysis-1`; manifest v2 / schema `nodes-edges-fts5-v1` / `analysis-v1` / `discovery-v2` |
| OS | Darwin 25.6.0 arm64 (MacBook Air) |
| HW | 8 CPUs, 16 GiB RAM (`hw.memsize 17179869184`, `hw.ncpu 8`) |
| Build | `go build -o /tmp/codegraph-p0 ./cmd/codegraph` → 25 MiB, `CGO_ENABLED=1` default toolchain |
| Live processes | two user-owned `codegraph mcp` (installed `~/.local/bin/codegraph`) hold the self-repo reader lock; self-repo reindex was **not** attempted (would contend with the live DB) |
| Ruby | `ruby 4.0.0 +PRISM [arm64-darwin25]`, bundler `4.0.3` |

## 2. Evidence check (E1–E8 vs checkout `1a4bd7e`)

All eight hold; only line numbers drifted. E1 reproduced live (see §5).

- E1 — `cmd/codegraph/main.go` `serveMCPWithHooks` keeps `readerLock` until shutdown; `internal/index/lock.go` shared/exclusive per-DB lock.
- E2 — `Neighbors` default limit 500 (`store.go`, "500 covers real hubs"); `Search` default 25; no byte caps; `Snippet` reads the whole file then slices (`securefile.ReadFile` + `strings.Split`).
- E3 — `internal/similar/similar.go` `edgesFromSigs`: per-bucket `O(k²)` pair loops feeding an unbounded `seen` map + edge slice.
- E4 — `internal/index/manifest.go` snapshot stages every source file + manifest inputs + `node_modules`/`vendor` trees into a private dir.
- E5 — `internal/bench/bench.go` `graphCost` queries `Callers/Callees(qn, 200)` while the product defaults to 500.
- E6 — `Engine.DeadCode` materializes all `FunctionsWithoutInboundCalls` before applying `limit`.
- E7 — `Store.InsertEdges` rebuilds the full QN→id map per non-empty call (comment notes the prior per-edge subquery fix).
- E8 — `changedScopesWithTSDependencies` (`incremental.go`) invalidates **all** TS scopes on any TS/JS source transition, by design.

## 3. Fixtures (private temp, deterministic, no secrets)

Base: `/tmp/codegraph-p0-fixtures` (276 files). Regenerate with the Python block
in this section's history (fixed contents, no randomness): Go hub module, TS
monorepo (`shared` + dependent `app-a` + independent `app-b`, `pnpm-workspace.yaml`),
minimal Ruby app (`config/routes.rb` + model), clones corpus, big file.

| Fixture | Shape | Source bytes |
| --- | --- | --- |
| `go-hub` | `go.mod` + 61 files; `Hub()` + 600 callers (60 files × 10) | ~248 KiB |
| `ts-mono` | 3 packages, 3 tsconfigs, `references` + `paths` | ~44 KiB |
| `ruby-app` | `routes.rb` (get/post/resources) + 1 model w/ absolute-const self-call | ~8 KiB |
| `clones` | 200 files × 6 identical-shape funcs = 1200 near-clones | ~800 KiB |
| `bigfile` | `big.ts`: 5000 funcs + one 200 000-char line (`LONG`) | ~480 KiB |
| `deadsmall` | 1-file Go module, unexported `dead1`/`main2` (dead_code positive control) | <1 KiB |
| self repo | 115 tracked source files (Go/TS/JS/Ruby) | stats-only (live MCP lock) |

## 4. Index measurements (cold / no-op / edit / config)

`time /tmp/codegraph-p0 index <fixture>`; `stats` after each run.

| Fixture | Cold | No-op (2nd run) | Nodes / Edges | DB |
| --- | --- | --- | --- | --- |
| `go-hub` | 0.69 s | 0.12 s | 662 / 1201 | 604 KiB |
| `ts-mono` | 4.42 s (scip 3 scopes via npx) | 0.03 s | 9 / 6 | 72 KiB |
| `ruby-app` | 0.06 s | 0.02 s | 9 / 8 (2 dropped) | 72 KiB |
| `clones` | 32.5 s | — (see §6) | 1400 / **720 600** | **145 MiB** |
| `bigfile` | 2.67 s | 0.75 s | 5003 / 5002 | 3.1 MiB |
| self repo | not measured (live lock) | — | 1104 / 1090 (stats) | 940 KiB |

- Single-file edit (`go-hub` `c00.go` + comment): 0.86 s rebuild — full Go-module VTA re-run, as designed (scope = module).
- `touch` of a tsconfig (mtime only): **no-op** — content hashing, not mtime, gates rebuild (invariant holds).
- Real tsconfig content change (`app-a`: +`noEmit`): 10.2 s, scip re-ran **all 3 scopes** — confirms E8 conservative invalidation also fires on config-only change in one scope.
- Broken tsconfig (`app-b` invalid JSON): hard error `parse TypeScript config ...`, previous graph preserved (`stats` still healthy 9/6); restoring the file returns to no-op. Fail-closed OK.
- Staging copy volume was **not** instrumented (gap for P6): snapshot copies ≈ source bytes + manifest inputs per run; no `node_modules` present in fixtures.

## 5. Query baseline (go-hub, warm, n=20 each)

- `search "Hub"`: p50 27.9 ms, p95 30.9 ms, min 24.8, max 59.5.
- `callers(hub.go.Hub)`: p50 29.7 ms, p95 62.5 ms, min 21.2, max 74.8. (Per-process CLI spawn included; first span pays binary+DB open.)
- Hub enumeration: `limit=2000` → **600 callers**, 28 320 bytes (~7 080 est. tokens, 1 round-trip). Default (no limit) → **500 lines, silently truncated, no `has_more`/continuation**. P2 acceptance case confirmed.
- Go QN note: single-file-package symbols surface file-scoped (`hub.go.Hub`, `c00.go.Caller0001`); `callees(c00.go.Caller0001)` → `Hub`. Task shape search→callers→snippet = 3 round-trips.
- `snippet big.ts` with **no range dumps the whole file: 487 855 bytes**; the single `LONG` line alone returns 200 023 bytes. No byte/line cap (E2/P2 case).
- `dead_code go-hub` → empty (all symbols exported → entry-point filter; correct per current rule, shows filter breadth). Positive control `deadsmall` → `dead1`, `main2`, as expected.
- MCP time-to-first-query after an edit (`go-hub`, background reindex): handshake immediate, first non-`indexing` search after **~1.0 s**.
- Two parallel `stats` readers: both OK in 0.02 s.
- External writer during live MCP (E1 live): `index` fails fast `rc=1` in 0.03 s — `error: index already in progress: <db>`; no deadlock, but no coordinated update either (P1 starting point).

## 6. Adversarial / resource notes

- `clones`: 1200 identical-shape functions → **720 600 `SIMILAR_TO` edges** (≈1500× source bytes → 145 MiB DB). Textbook E3 quadratic blowup; P4 budget/interrupt unit is justified. Rebuild peak RSS (`/usr/bin/time -l`): **631 930 880 bytes (~603 MiB)**. Small-index RSS (`deadsmall`): 18.7 MiB max.
- `bigfile` no-op costs 0.75 s (integrity gate scales with DB/nodes — 5003 nodes in 1 file).
- Cancellation: SIGINT 1 s into a forced `clones` rebuild via the persistent-shell harness hung the harness (`wait` never returned; had to abandon the call — no repo state harmed, graph stayed healthy, no `.building` leftovers). Manual cancellation is therefore **not proven** by this baseline; repo-side coverage (`internal/index/cancellation_test.go` — per-entry/per-edge cancel tests, all passing incl. `-race`) stands in. P1 must add a robust two-process cancel/writer test instead of shell-background tricks.
- `codegraph bench` was **not** run on any live-MCP DB (per plan); E5 (cap 200 vs product 500) is a code-reading finding, P3 will re-measure with equivalent answers.

## 7. Validation

- `go build ./cmd/codegraph`: OK. `go vet ./...`: clean.
- `go test ./... -count=1`: all packages OK **except one flake** — `TestRunScipContextTerminatesForkedDescendant` (`internal/scip`) failed once (`invalid PID ... parent.pid: ""`), then passed twice on immediate retry with no code change. Pre-existing timing-sensitive test; P0 changed no source (worktree §1). Recommend P1+ owners treat it as flaky (retry-with-backoff or PID-ready gate) rather than a product bug.
- `go test -race ./... -timeout 20m -count=1`: **all 13 packages OK** (~7 min; `internal/index` 316 s dominates). This supersedes the plan's note about the old 120 s external timeout.
- Benchmarks for isolated algorithms: none exist (`grep func Benchmark internal/` empty) — P3/P4 should add them (similarity adversarial, dead_code streaming, InsertEdges map reuse) rather than timing only end-to-end.

## 8. Gates for conditional units (initial read, to be confirmed by P1–P5 numbers)

- P6 (staging): justified only if staging-copy cost matters — fixtures show no `node_modules`; needs a fixture **with** dependency trees + copy-byte instrumentation first.
- P7 (TS incremental): current behavior measured (any TS change → all scopes; 10.2 s for a 1-line config change in a 3-file monorepo). Selective invalidation work starts only with the ownership model + rebuild-equivalence proof the plan demands.
- P8 (MCP concurrency): p50 ~30 ms sequential queries show no measured contention yet; needs a mixed-load p95 experiment before any goroutine work.

## 9. Reproduction

```bash
go build -o /tmp/codegraph-p0 ./cmd/codegraph
# fixtures: regenerate /tmp/codegraph-p0-fixtures per §3 (script in session history)
/tmp/codegraph-p0 index /tmp/codegraph-p0-fixtures/go-hub
/tmp/codegraph-p0 stats /tmp/codegraph-p0-fixtures/go-hub
/tmp/codegraph-p0 cli callers /tmp/codegraph-p0-fixtures/go-hub '{"qualified_name":"hub.go.Hub","limit":2000}' | wc -l   # expect 600
/tmp/codegraph-p0 cli callers /tmp/codegraph-p0-fixtures/go-hub '{"qualified_name":"hub.go.Hub"}' | wc -l              # expect 500 (silent truncation)
/tmp/codegraph-p0 cli snippet /tmp/codegraph-p0-fixtures/bigfile '{"file":"big.ts"}' | wc -c                            # expect 487855 (no cap)
go vet ./... && go test ./... -count=1
```

Next: P1 (coordinated MCP update) and P2 (budgeted responses + continuation) per plan order; P1/P2 investigations may run in parallel but must integrate serially (both touch MCP/query). Do **not** hand `store.go` edits to P2 and P5 concurrently.

## P1 — coordinated MCP update (done 2026-09-07, commit pending)

Design (chosen up front, recorded in `docs/ARCHITECTURE.md` before coding):
per-operation locks + generation revalidation. No lifetime reader locks; the
shared lock is taken only around reopen. No watcher; explicit async `refresh`
(deduped per process) + `status` (read-only, reports lag, never mutates).
Generation = manifest `graph_content_digest` (short 12). During `updating`
tools answer the state, never mixed generations. One session mutex serializes
queries against close/reopen (closed engine → actionable error, never panic).

Behavior deltas vs P0 baseline (all measured live on the P0 fixtures):

- External `codegraph index` against a **live** MCP session now succeeds
  (P0: `index already in progress` in 0.03 s). Session `status` then reports
  `lag=disk holds newer generation <D>; call refresh to converge`; `refresh`
  converges with no restart on either side (verified: `deadsmall` gen
  `38e3…` → `91f0…`, new symbol queryable).
- Second edit after ready: `refresh` → new generation, new symbol searchable,
  same process (test + live smoke on `go-hub`: no-op refresh `reused=true`,
  generation stable across comment-only edits — digest covers logical graph).
- Sustained contention (>2.5 s held exclusive lock, two real processes):
  second writer fails fast with `index already in progress`, succeeds after
  release — never two writers, no permanent block.
- Refresh colliding with an active writer declines **without closing the
  engine** (probe-first): the session keeps serving the old graph with a
  `refresh deferred … retry refresh` notice. Found by
  `TestSession_NeverTwoWriters` failing pre-probe (round left the session
  unqueryable); fixed in-unit.
- Contended refresh that already closed the engine reopens the previous graph
  and serves it as `failed` with the failure as context (ported
  `TestMCPBackgroundIndex_…` → `TestSessionRefresh_…`, same barrier style).

Tests (all deterministic barriers/channels; sleeps only as the held load):

- `cmd/codegraph/session_test.go`: SecondEditRefreshWithoutRestart,
  TwoSessionsConverge (no restart), NeverTwoWriters, RefreshDedup (build runs
  once), QueryNeverUsesClosedEngine, TwoProcesses_WriterVsLiveSession and
  TwoProcesses_WriterHoldsOverTwoSeconds (helper-process via `TestMain`
  `CODEGRAPH_TEST_CHILD=tryindex`).
- `internal/mcp`: `QueryEngine` interface + `RegisterTool` seam
  (`TestServer_ExtraToolsRegistry`: tools/list inclusion, ungated vs gated,
  collision rejection); gate contract change (non-empty notice always
  prepended; clean ready is empty) — two pinned tests updated to the new
  contract, none weakened.
- Validation: `go vet ./...` clean; `go test ./...` all green;
  `go test -race ./cmd/codegraph/ ./internal/mcp/ ./internal/query/` green.
  Pre-existing scip flake (`TestRunScipContextTerminatesForkedDescendant`)
  did not recur in these runs.
- Known gap: lock/replacement behavior executed on Unix only. No new
  platform-specific calls were added (all locking via the existing
  `index.Lock` abstraction), but Windows/Unix dual execution the plan asks
  for was not possible from this machine.

Next: P2 (budgeted responses + continuation); P1/P2 touch MCP/query, so
integrate serially per plan. `store.go` stays untouched by P1 (P2/P5 must not
edit it concurrently).

## P2 — budgeted responses + continuation (done 2026-09-07, commit pending)

Everything query-shaped is now a page with a `#` trailer
(`has_more/cursor/generation`); truncation never looks exhaustive. Budgets
(`internal/query/budget.go`, shared CLI+MCP): 500 refs / 200 snippet lines /
32 KiB text per page, bytes prevail, giants clamp to 2000 refs / 2000 lines
before any allocation, negatives and incoherent ranges are actionable errors.

- Hub >500 (`go-hub`, 600 callers): default page 1 = 500 refs +
  `has_more=true` + cursor; full walk = 2 pages, 600 unique, zero
  duplicates/omissions, every qualified_name intact. P0's silent 500-cut and
  P0's 600-line/28 KiB single dump are both gone as behaviors.
- Snippet: bare `{"file":"big.ts"}` (P0: 487 855 bytes) now returns 200 lines
  / ~11 KiB + `has_more=true`. The 200 KB single `LONG` line comes back whole,
  marked `long_line=true`, `has_more=false` — never split. Lossless
  reassembly under adversarial budgets (2 lines / 6 bytes pages) is pinned by
  test; multibyte runes never split (whole-line pages); a file modified
  between pages fails the next page (`changed between pages`) instead of
  shifting lines.
- Cursors bind to query + generation: cross-tool / cross-page-size reuse and
  stale-generation continuations are rejected with restart orientation (unit
  tests, incl. a forged post-refresh cursor). Store paging is
  `ORDER BY qualified_name LIMIT/OFFSET` over the immutable generation; the
  `both`-direction UNION (not UNION ALL) still dedups.
- `dead_code` pages too (default now shares the 500/page budget; streaming
  the candidate set stays P5's job — documented on `DeadCodePage`).
- Bench (`graphCost`) walks every real page in product wire format (trailers
  included), one counted call per page — the E5 fixed-200 cap is gone as a
  cost shortcut. Full P3 methodology (hub-selection bias, bytes/4 note,
  amortized trajectories) stays P3's job.
- Removed `graph.Snippet` (whole-file reader) so no caller can fall back to
  unbounded reads; `SnippetPaged` + confinement tests cover the seam.
- Validation: `go vet ./...` clean; `go test ./...` all green (13 packages);
  `go test -race` green for `query/mcp/graph/cmd` (new: `page_test.go` — validation, clamp, cursor
  binding × tool/size/generation; `snippet_page_test.go` — lossless, long
  line, change detection, end-bound; `TestEngine_Callers_PagesEnumerateHubFully`
  — 600-caller walk); MCP E2E (status→search page→cursor→page 2, negative
  limit → `-32000` with guidance). Race for touched packages + full suite
  still to run at wrap-up.
- Known gap (unchanged): Windows/Unix dual execution from this machine.

## P4 — resource-bounded similarity (done 2026-09-07, commit pending)

`internal/similar`: `Limits` (default 250k pairs / 50k edges, env-overridable,
never unlimited) enforced before the unbounded `seen`/edge growth; buckets
processed in sorted order over QN-sorted docs so a binding budget keeps a
deterministic stable prefix; ctx honored between pairs (canceled pass errors,
previous graph kept). `Coverage` (complete/partial/omitted + counts) travels
in `Result` and the manifest; `SimilarVersion` (algo+budget) joins the no-op
fingerprint. `similar` answers and `status` carry the notice when incomplete.

- Adversarial (`clones`, 1200 identical-shape funcs): 720 600 edges / 145 MiB /
  32.5 s / 603 MiB peak (P0) → 51 200 edges / ~10 MiB / ~3 s / 195 MiB peak,
  `status=healthy`, manifest `similar.status=partial (docs=1200, pairs
  250000 capped, edges 50000 capped)`. No-op reuses with coverage; a budget
  change rebuilds (no cross-budget certification); pre-P4 DBs rebuild once
  (new required manifest field).
- Determinism: same-budget reruns identical; wider budgets extend the prefix
  (never reshuffle); shuffled input identical; two separate processes/DBs
  produce byte-identical `similar` pages. Found live mid-unit: pipeline fed
  docs in map order, so the first cut gave different prefixes per process —
  fixed by QN-sorting at the pass boundary, proven by the byte-identical rerun.
- Cancel: pre-canceled fails fast; 1 ms timeout vs ~4.5M pairs fails
  deterministically mid-run. `similar` on a partial graph prepends the
  raise-the-budget-and-reindex notice; non-similar tools stay bare.
- Threshold sensitivity pinned: rename-only clone 0.85 (≥0.7), bodies with a
  few changed constants 0.23 — trigram shingles amplify small edits, which is
  why fixtures must differ (almost) only by name.
- P4.5 (exact-clone grouping): deferred — the budget already contains the
  blowup, so no new graph model is justified in this unit.
- Validation: 6 similar-unit + 4 pipeline + 2 query + 1 MCP + 1 session-status
  tests new; full suite + race at wrap-up.
- Known gap (unchanged): Windows/Unix dual execution from this machine.

## P5 — local query/insert optimizations (done 2026-09-07, commit pending)

Two separate, measured changes; transactions, endpoints, dropped/duplicate
accounting and project isolation preserved (existing suites assert exact
counts and pass unchanged).

- P5a (dead_code streaming): `FunctionsWithoutInboundCalls` (materialize-all)
  replaced by `Store.DeadCodeCandidates` (LIMIT/OFFSET batches, same order)
  with the entry-point filter looping batches query-side — an
  entry-point-heavy repo costs extra cheap indexed batches, never a false end
  of results. `DeadCodePage(50)` over 4000 candidates: ~5.9k allocs/run
  against a 20k bound (batch-proportional); full streamed walk == expected
  dead set, no duplicates, cursors terminate with `-`.
- P5b (phase QN→ID map): `InsertEdges` reuses a Store-scoped map while the
  node set is stable instead of rescanning per call; `InsertNodes` /
  `ReplaceProject` / `Reopen` / `Close` invalidate explicitly (all node writes
  go through those methods — verified by search; reuse reads never insert).
  Late nodes resolve, projects stay isolated, post-wipe ids relink, counting
  preserved. Per repeated call over 10k nodes, eliminated: ~1.5 MB + ~50k
  allocs + the ~19 ms full scan (this machine); steady state per 500-edge
  batch is ~248 KB / 7.7k allocs (tx + execs only).
- Validation: new `deadcode_page_test.go` (store) + `deadcode_stream_test.go`
  (query: equivalence, starvation, alloc bound, benchmark) +
  `insert_map_test.go` (late nodes, isolation, wipe, counting, benchmark);
  full suite + race at wrap-up.
- Known gap (unchanged): Windows/Unix dual execution from this machine.

## Gates P6–P8 (evaluated 2026-09-07; P6+P7 implemented, P8 kept sequential)

- **P8 (concurrency): gate FAILS → sequential kept, no code change.** Mixed
  load on MCP (`go-hub`: search/callers/neighbors/dead_code): sequential
  p50 6.8 ms / p95 23.6 ms, 0 errors; 8 clients × 10 calls serialized: wall
  637 ms, p50 59.5 ms / p95 96.9 ms, 80/80 ok. No relevant blocking for the
  real pattern (one agent session, sequential/mildly parallel calls); status
  and handshake are already ungated (P1), slow ops are paged (P2). Concurrency
  (goroutines + serialized writes + per-request ctx + SQLite limits) is not
  justified by measurement — per plan, sequential stays.
- **P6 (staging): gate PASSES → implemented.** Fixture with 24 MB / 2000-file
  `node_modules`: staging cost 15.1 s vs 0.05 s without deps — and the copy is
  not the cost: per-file breakdown (10 KB) is raw write 0.88 ms,
  `WritePrivate` 7.0 ms (fsync-dominated), secure mkdir 0.36 ms. Fix:
  `securefile.WritePrivateTemp` (identical symlink/permission/identity checks,
  no fsync — crash durability is pointless for a discarded, re-stageable
  snapshot) + snapshot-only `copyResolverFile` uses it; mkdir batching
  deliberately NOT done (per-file mkdir re-verifies parents — caching would
  weaken mutation detection). Result: staging 15.1 s → ~4–6.7 s (hot-file
  delta 7.37 → 2.33 ms, 3.2×), identical resolver output (same
  nodes/edges/callers on identical sources), mutation/symlink suites green.
  Remaining per-file ~2 ms is verified reads/writes — sharing scan reads
  (P6.2) not justified yet. E2E on the tiny fixture stays scip-noise-dominated
  (21→25 s across runs) — reported, not claimed.
- **P7 (TS incremental): gate PASSES → implemented** (`tsdeps.go`).
  Ownership model: owning scope + transitive project-reference dependents +
  observed direct importers (old-graph IMPORTS); Modified-only `.ts`
  transitions narrow, everything else (added/deleted, configs, unverifiable
  tsconfigs, root-loose files, nil store) keeps invalidate-all. Soundness:
  Modified files can't change resolvability (same file set + configs, enforced
  by the manifest gate which widens the rest); re-export/overload shapes
  covered by digest-equivalence fixtures. Policy versioned in the manifest
  (`ts_invalidation_version`, one rebuild of old DBs). Live: independent
  app-b edit re-runs 1 scope (was 3), app-a+shared reused; policy recorded.
  P7 acceptance: 4 digest-equivalence cases (independent/dependent/shared/
  shared-types edits: incremental digest == full-rebuild digest) + narrowing
  unit tests + fallback test + preserved conservative pins.
- Known gap (unchanged): Windows/Unix dual execution from this machine.

## P3 — equivalent-answer benchmark (done 2026-09-07, commit pending)

`internal/bench`: page size parameterized (`graphCostPages`; product default
500 ships), cost = every real page in product wire format, 1 call/page.
Outcome carries `Oracle`/`Complete`/`IndexStatus`; `RunOneWithOracle` requires
the exact caller set — same question semantics both sides.

- Independent oracle (`oracle.go`, go/parser+go/ast — different frontend from
  the tree-sitter+VTA pipeline): 600-caller Go fixture (+ method caller,
  comment/string decoys) → graph returns exactly the oracle set, over 2 pages
  (`TestGraphCompletenessAgainstIndependentOracle`). Decoys excluded both sides.
- No truncation win (`TestPageSizeNeverManufacturesACheaperAnswer`): identical
  121-caller set at page sizes 500/50/7; smaller pages cost more calls and
  (trailers charged) no fewer tokens.
- Trajectory (`trajectory.go`, temp fixtures only — never mutates a real repo):
  cold index → query → edit → stale count → reindex → re-query, plus
  amortized wall time/query for k=1/10/100. Measured on a 600-caller hub:
  index 1.32 s, reindex 1.57 s, stale=1 file, Q 7248 tok/2 calls/0.027 s,
  601→602 results, amortized 2.91 s → 0.32 s → 0.06 s, status healthy.
- `codegraph bench` now stamps `method=paged-wire-v1` + index status, and
  prints the resolver failure when degraded instead of a clean number.
  Live run on the P0 `go-hub` fixture: 1 hub question, 600 callers,
  graph 7131 tok/2 calls vs grep+window 19823/62 calls → 2.8× tokens,
  31× fewer calls. Honest and expected: tiny one-line functions make grep
  windows overlap, so the ratio compresses — documented as hub-selection bias
  in BENCHMARK.md (real-repo 15–16× numbers stay tagged `tsv-uncapped-v0` and
  must not be trend-compared with `paged-wire-v1` runs).
- QUALITY.md now tells responders to walk every page and distinguishes the two
  oracles (quality-harness LLM/F1 vs bench go/ast recall).
- Validation: `go test ./internal/bench/` green (4 new tests); full suite +
  race at wrap-up.
