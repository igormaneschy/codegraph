# Validation contract — certified no-op for local Go embed inputs

Written before implementation, 2026-10-09. Source base: `main` / `fd165f1`.

## Problem and motivation (measured)

- On `AutoTradersOMQS-GO` (1029 Go files, 2 files with real `//go:embed`
  directives) every unchanged refresh rebuilt: **19.3–19.8 s wall, 2.6–2.9 GiB
  RSS** (n=3), with invalidation `go-embed-inputs-unobserved` (+
  `manifest-untrusted`). Dependency inputs are already certified
  (`resolver-inputs-v5`), so the embed reason was the last blocker for that
  repository.
- The 2c transport already observes the selected local assets: pattern parsing
  from real comments (Go grammar), driver-oracle equality against
  `go list -json` for literal/glob/directory/quoted/`all:`, no-follow reads,
  symlink/`.env` rejection, membership and bytes mutation detection, and
  late-staging failure. The reason was kept as policy ("transporting local
  assets does not prove complete program closure"); other uncertified inputs
  (workspace/GOPATH/cgo/TS runtime) keep their own independent reasons.

## Behavior (implemented)

- [x] `go-embed-inputs-unobserved` is set only when a detected directive is NOT
  fully handled by the local transport: source parse failure, missing `embed`
  import, or no valid pattern arguments. A file whose directive set parsed with
  the embed import keeps its transport-observed assets (hashed into the
  resolver input plan) and no longer blocks no-op.
- [x] The transport is re-derived on every scan: selected-asset bytes,
  membership, links and pattern changes flow into the plan and the manifest
  fingerprint, so any change to an observed embed input forces a rebuild.
- [x] No other reason changes: workspace/GOPATH/GOCACHEPROG, real cgo, external
  caches, TS runtime and unavailable environments still block no-op/CALLS reuse
  independently.
- [x] No schema or identity change: existing manifests that still carry the
  reason are rejected by the fingerprint and rebuilt once; the plan version
  stays `resolver-inputs-v5`.
- [x] The dependency module-list probe (`go list -m all`) became opportunistic:
  it needs the complete module graph, which an offline cache may not have
  (modules required but never imported). A probe failure now falls through to
  the authoritative per-package enumeration instead of blocking reuse; the
  enumeration still fails closed on its own. Found while validating this
  change: with the embed reason cleared, the dependency observation ran for the
  first time on `AutoTradersOMQS-GO` and hit exactly this case.

## Validation (executed)

- [x] Red regressions: an embed fixture with a real directive certifies no-op on
  the second run; editing, adding or removing a selected asset forces a rebuild;
  a directive without the embed import, an invalid pattern and a parse-error
  source keep the reason; the driver-oracle selection tests stay green; a
  fixture with an unimported `require` (probe failure) still certifies through
  the enumeration fallback and no-ops unchanged. The pre-existing
  `TestGoEnvironment_UnobservedExternalDependenciesCannotCertifyReuse` case was
  strengthened to an **imported**, unobservable dependency: an unimported
  require no longer blocks by design, but a real build input that cannot be
  enumerated still keeps reuse blocked.
- [x] Before/after on the `AutoTradersOMQS-GO` clone: unchanged refresh
  **19.3–19.8 s / 2.6–2.9 GiB → 7.04–7.13 s / 67.8–69.1 MB** certified no-op
  (n=3), with identical graph counts (`files=1029 nodes=11188 edges=34896`,
  0 dropped). The no-op cost is dominated by scale, not this change: exact
  integrity certification 3.87 s + observation (source hashing, dependency
  enumeration) 3.23 s. The codegraph clone stays unaffected.
- [x] Gates: formatting/build/vet, full default, race (index), lint; remote
  exact-head CI is the merge gate.

## Limits and non-goals

- Certifies the local embed input transport only, not program closure beyond it
  (workspace, external inputs and the TS runtime stay conservative).
- The union of directives in inactive sources stays conservative (a superset:
  extra rebuilds are possible, missed inputs are not).
- Directive comments outside compiler-honored positions are over-included by
  the transport (extra observation only; no missed inputs).
- No performance claim beyond the no-op; the rebuild path is unchanged.

## Recorded evidence

- Tests in `internal/index/go_embed_inputs_test.go` (no-op certification, asset
  edit/add/remove invalidation, unhandled directives) and
  `internal/index/go_dependency_inputs_test.go` (probe fallback), plus the
  existing driver-oracle/security suites.
- Before/after logs: AutoTradersOMQS-GO clone at `4cc8dd6`, branch build,
  macOS/M1, warm caches; migration rebuild 22.1 s (`manifest-untrusted` only),
  then 7.04/7.13/7.11 s no-op runs at 67.8/69.1/68.1 MB peak RSS. Local gate
  logs stay outside the checkout under the session's temp directory; remote CI
  is the merge gate.
