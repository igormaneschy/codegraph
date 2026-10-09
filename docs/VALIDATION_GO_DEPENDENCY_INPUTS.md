# Validation contract — certified no-op for unchanged Go dependency inputs

Written before implementation, 2026-10-09. Source base: `main` / `ba1279c`.
Owner scope remains personal macOS-only (`PERSONAL_MACOS_SCOPE.md`).

## Problem and motivation (measured, 2026-10-09)

- An unchanged refresh of this repository (225 files, 22 loaded modules, 62 in the
  build list) always rebuilt: **4.6–5.1 s wall and ~1.4–1.7 GiB peak RSS**,
  because `go.mod` has `require` entries, the Go dependency bytes were unobserved,
  and `manifestFingerprintFor` rejects no-op when `NoReuseReasons` is non-empty.
- Two of the three recorded reasons in this repository were heuristic false
  positives: `bytes.Contains(content, "\"C\"")` matched the string literal in
  `internal/index/go_flags.go:86` and `bytes.Contains(content, "//go:embed")`
  matched fixture strings in tests.
- Diagnostic costs on macOS/M1 (warm cache, clone of this repo):
  - `go list -m all`: 0.015–0.07 s (62 modules; 21 actually loaded);
  - `go list -deps -compiled [-test]`: ~0.55 s; precise dependency file set:
    **483 files / ~17.3 MB**, hash ~0.11 s;
  - broad alternative (all `.go` of build-list module dirs): 4.6k files /
    ~283 MB / ~1.1 s — dominated by unused cross-platform files;
  - cheap-but-weak alternative (module ziphash files only): ~0.125 s, certifies
    distributed hashes, not the extracted bytes the build reads.

## Behavior (implemented)

- [x] When a Go repository has module dependencies, the freshness observation
  records a `GoDependencyInputs` digest covering the dependency source files the
  Go build actually consumes under the effective environment and build tags: for
  every dependency package listed by `go list -deps -compiled -test ./...`, its
  `CompiledGoFiles` plus cgo `CgoFiles`/`CFiles`/`CXXFiles`/`HFiles`/`SFiles`.
  Files under `GOROOT` (toolchain identity) and under the repository root
  (source hashing) are excluded; generated cgo files under `GOCACHE` are derived.
- [x] The digest is canonical (sorted path, sha256) and recorded in the manifest
  as part of `resolver-inputs-v5`; any change to the file set, a module version,
  or any byte changes the digest. `go list` runs offline/read-only
  (`GOPROXY=off`, `-mod=readonly` when `-mod=mod` is effective): missing cache or
  network need is a fail-closed reason, never a mutation or a silent skip.
- [x] `go-dependency-inputs-unobserved` is no longer set merely because `go.mod`
  has `require`/`replace` entries. It is set when the observation cannot be
  completed: no Go tool, `go list` failure, unreadable/missing file, or unstable
  observation. The observation only runs when no cheaper reason already blocks
  no-op.
- [x] No-op certification requires the stored digest to equal the current digest;
  an unchanged repository reaches `decision=noop` with the exact integrity
  validator as the last operation, unchanged from today.
- [x] Uncertified reasons (workspace/GOPATH/GOCACHEPROG/vendor gaps) still disable
  no-op and CALLS reuse; nothing was relaxed by this change.
- [x] Heuristic precision: `go-cgo-external-inputs-unobserved` and
  `go-embed-inputs-unobserved` come from parsed source (imports/comments), not
  `bytes.Contains`. A real `import "C"` or a real `//go:embed` directive still
  sets the reason; literals inside strings/raw strings/fixtures no longer do.
  A parse failure keeps the candidate bytes' verdicts (conservative).
- [x] Vendor mode: vendored repositories skip the module-list probe (`go list -m
  all` cannot compute `all` against a vendor directory) and resolve vendored
  packages in place; the existing vendor-tree plan files certify the bytes, and
  the dependency reason is cleared when that observation succeeds.

## Validation (executed)

- [x] Red regressions (fixture module with `replace` to a temp module outside the
  root): unchanged repository is a certified no-op with identical graph/CALLS;
  edit, add or remove a dependency file, or remove the dependency directory →
  rebuild or fail-closed, never no-op; unreadable dependency file → fail-closed;
  vendored fixture: vendor bytes change → rebuild; literal `"C"`/`//go:embed` in
  sources no longer block, a real cgo import keeps the reason; parser and
  `parseGoListDependencyFiles` unit cases; `-mod=vendor`/`-mod=mod` argument
  derivation.
- [x] Local gates: formatting/modules/build/vet, full default and race suites,
  real SCIP integration, lint; remote exact-head CI is the merge gate.
- [x] Before/after measurement on the disposable clone (macOS/M1, n=3):
  - unchanged run reaches `decision=noop`: **1.09–1.12 s wall, ~48 MB peak RSS**
    (before: rebuild 4.6–5.1 s, 1.4–1.7 GiB) — ~4.3× faster, ~30× less memory;
  - rebuild path (edit → refresh): **5.97 s** vs 4.80 s before (+24%, above the
    ≤15% target). Breakdown: +0.6 s first enumeration+hashing; +0.3 s exact
    digest/integrity certification now reached in `freshManifestFor` (previously
    short-circuited by the uncertified reason, pre-existing behavior for
    certified repositories); +0.3 s second enumeration in the handoff re-scan
    (`stableResolverHandoff` re-observes the repository by design). Accepted:
    the no-op goal is met with far less memory; memoizing the handoff
    enumeration stays a documented follow-up, not part of this cut.
- [x] Identity: `resolver-inputs-v5` forces a rebuild of v4 certificates; no
  schema change beyond the manifest field; `ARCHITECTURE.md` updated.

## Limits and non-goals

- Real embeds with directives remain conservative (full embed certification
  stays open, as recorded for 2c).
- Workspace (`go.work`), GOPATH mode, GOCACHEPROG and external Go cache stay
  conservative.
- The C compiler binary/toolchain identity is not separately digested: cgo files
  are hashed and `CC`/`PATH`/`CGO_ENABLED` are in the environment digest. The
  remaining gap is documented; its graph impact is confined to `C.*` references
  that have no graph endpoints.
- No whole-module-tree hashing: only files the build consumes; unused
  cross-platform files stay out of the digest.
- Rebuilds run the enumeration twice (initial scan + handoff re-observation);
  the handoff re-scan is a deliberate drift check. A cached-enumeration
  optimization remains a possible later bounded change.
- No new reindexing/reuse behavior for TS/JS or Ruby.

## Recorded evidence

- Unit/integration tests in `internal/index/go_dependency_inputs_test.go` and
  `internal/index/go_source_specials_test.go` (red cases listed above).
- Before/after logs: disposable clone of this repository at `ba1279c`, new
  binary `dd63842` (branch `review-go-dependency-inputs`), macOS 27.0.1, Apple
  M1, 16 GiB, Go 1.27.0; n=3 per scenario, warm caches after the first run.
  No-op: 1.12/1.09/1.09 s, RSS 49.5/47.8/48.1 MB, `decision=noop`. Rebuild:
  5.97/5.96/6.02 s, RSS 1.65/1.66/1.74 GiB. Baseline (old binary): no-op never
  certified (rebuild 5.07/4.99/4.55 s, RSS 1.52/1.60/1.62 GiB); edit → refresh
  4.80/5.40/4.68 s.
- Local gate logs are kept outside the checkout under the session's temp
  directory.
