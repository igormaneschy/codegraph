# Validation contract — certified no-op for unchanged Go dependency inputs

Written before implementation, 2026-10-09. Source base: `main` / `ba1279c`.
Owner scope remains personal macOS-only (`PERSONAL_MACOS_SCOPE.md`).

## Problem and motivation (measured, 2026-10-09)

- An unchanged refresh of this repository (225 files, 22 loaded modules, 62 in the
  build list) always rebuilds: **4.6–5.1 s wall and ~1.4–1.7 GiB peak RSS**,
  because `go.mod` has `require` entries, the Go dependency bytes are unobserved,
  and `manifestFingerprintFor` rejects no-op when `NoReuseReasons` is non-empty.
- Two of the three recorded reasons in this repository are heuristic false
  positives: `bytes.Contains(content, "\"C\"")` matches the string literal in
  `internal/index/go_flags.go:86` and `bytes.Contains(content, "//go:embed")`
  matches fixture strings in tests.
- Diagnostic costs on macOS/M1 (warm cache, clone of this repo, shell
  single-stream SHA-256; a parallel in-process observer is expected to be faster):
  - `go list -m all`: 0.015–0.07 s (62 modules; 21 actually loaded);
  - `go list -deps -compiled [-test]`: ~0.55 s; precise dependency file set:
    **483 files / ~17.3 MB**, hash ~0.11 s;
  - broad alternative (all `.go` of build-list module dirs): 4.6k files /
    ~283 MB / ~1.1 s — dominated by unused cross-platform files
    (e.g. `modernc.org/sqlite` per-OS generated sources);
  - cheap-but-weak alternative (module ziphash files only): ~0.125 s, but it
    certifies distributed hashes, not the extracted bytes the build reads.

## Behavior

- [ ] When a Go repository has module dependencies, the freshness observation
  records a `GoDependencyInputs` digest covering the dependency source files the
  Go build actually consumes under the effective environment and build tags: for
  every dependency package listed by `go list -deps -compiled -test ./...`, its
  `CompiledGoFiles` plus cgo `CgoFiles`/`CFiles`/`CXXFiles`/`HFiles`/`SFiles`.
  Files under `GOROOT` (toolchain identity) and under the repository root
  (source hashing) are excluded.
- [ ] The digest is canonical (sorted `module@version`, path, sha256) and recorded
  in the manifest; any change to the file set, a module version, or any byte
  changes the digest. `go list` runs offline/read-only (`GOPROXY=off`,
  `-mod=readonly`): missing cache or network need is a fail-closed reason, never
  a mutation or a silent skip.
- [ ] `go-dependency-inputs-unobserved` is no longer set merely because `go.mod`
  has `require`/`replace` entries. It is set when the observation cannot be
  completed: no Go tool, `go list` failure, unreadable/missing file, unstable
  observation, or enumeration of zero dependency files when dependencies exist.
- [ ] No-op certification requires the stored digest to equal the current digest;
  an unchanged repository reaches `decision=noop` with the exact integrity
  validator as the last operation, unchanged from today.
- [ ] Uncertified reasons (workspace/GOPATH/GOCACHEPROG/vendor gaps) still disable
  no-op and CALLS reuse; nothing is relaxed by this change.
- [ ] Heuristic precision: `go-cgo-external-inputs-unobserved` and
  `go-embed-inputs-unobserved` come from parsed source (imports/comments), not
  `bytes.Contains`. A real `import "C"` or a real `//go:embed` directive still
  sets the reason; literals inside strings/raw strings/fixtures no longer do.
  A parse failure keeps the reason (conservative).
- [ ] Vendor mode: `vendor/modules.txt` becomes a fingerprinted manifest input;
  vendored dependency bytes remain observed through the existing dependency
  roots, and the dependency reason is cleared when that observation succeeds.

## Validation

- [ ] Red regressions (fixture module with `replace` to a temp module outside the
  root, so dependency bytes are controllable without touching the real cache):
  - unchanged repository → second run is a certified no-op with identical
    graph/CALLS;
  - edit, add or delete a dependency file → rebuild (digest change or
    fail-closed), never no-op;
  - remove the dependency directory → fail-closed reason, no stale no-op;
  - unreadable dependency file → error/reason, no no-op;
  - `go.mod`/`go.sum` change and GOFLAGS/tags change → rebuild (existing inputs
    and env digest);
  - real `import "C"` / real `//go:embed` in root sources → reason kept; literal
    occurrences → reason cleared;
  - cgo dependency package files are part of the digest;
  - vendor fixture: vendor bytes change → rebuild.
- [ ] Local gates: formatting/modules/build/vet, full default and race suites,
  real SCIP integration, lint; remote exact-head CI is the merge gate.
- [ ] Before/after measurement on the disposable clone (n≥3, same machine):
  unchanged run reaches `decision=noop`; wall and peak RSS recorded; the rebuild
  path cost regression stays ≤ ~15% (target no-op ≤ ~1.5 s with no go/packages
  load).
- [ ] Identity: `resolver-inputs-v5` forces a rebuild of v4 certificates; no
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
- No cached-enumeration optimization in this cut (`go list` runs on each
  observation); it can be a later bounded change if the no-op cost still matters.
- No new reindexing/reuse behavior for TS/JS or Ruby.

## Recorded evidence

- (to be filled after implementation: red logs, before/after measurements, gate
  logs; local evidence under a dedicated temp directory, never in the checkout)
