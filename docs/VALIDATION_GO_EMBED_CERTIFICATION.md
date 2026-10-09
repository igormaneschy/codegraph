# Validation contract — certified no-op for local Go embed inputs

Written before implementation, 2026-10-09. Source base: `main` / `fd165f1`.

## Problem and motivation (measured)

- On `AutoTradersOMQS-GO` (1029 Go files, 2 files with real `//go:embed`
  directives) every unchanged refresh rebuilt: **19.3–19.8 s wall, 2.6–2.9 GiB
  RSS** (n=3), with invalidation `go-embed-inputs-unobserved` (+
  `manifest-untrusted`). Dependency inputs are already certified
  (`resolver-inputs-v5`), so the embed reason is the last blocker for that
  repository.
- The 2c transport already observes the selected local assets: pattern parsing
  from real comments (Go grammar), driver-oracle equality against
  `go list -json` for literal/glob/directory/quoted/`all:`, no-follow reads,
  symlink/`.env` rejection, membership and bytes mutation detection, and
  late-staging failure. The reason was kept as policy ("transporting local
  assets does not prove complete program closure"); other uncertified inputs
  (workspace/GOPATH/cgo/TS runtime) keep their own independent reasons.

## Behavior

- [ ] `go-embed-inputs-unobserved` is set only when a detected directive is NOT
  fully handled by the local transport: source parse failure, missing `embed`
  import, or no valid pattern arguments. A file whose directive set parsed with
  the embed import keeps its transport-observed assets (hashed into the
  resolver input plan) and no longer blocks no-op.
- [ ] The transport is re-derived on every scan: selected-asset bytes,
  membership, links and pattern changes flow into the plan and the manifest
  fingerprint, so any change to an observed embed input forces a rebuild.
- [ ] No other reason changes: workspace/GOPATH/GOCACHEPROG, real cgo, external
  caches, TS runtime and unavailable environments still block no-op/CALLS reuse
  independently.
- [ ] No schema or identity change: existing manifests that still carry the
  reason are rejected by the fingerprint and rebuilt once; the plan version
  stays `resolver-inputs-v5`.

## Validation

- [ ] Red regressions: an embed fixture with a real directive certifies no-op on
  the second run; editing, adding or removing a selected asset forces a rebuild
  or fails closed; a directive without the embed import, an invalid pattern and
  a parse-error source keep the reason; the driver-oracle selection tests stay
  green.
- [ ] Before/after on the `AutoTradersOMQS-GO` clone: unchanged refresh
  19.3–19.8 s / ~2.7 GiB → certified no-op (target ≤ ~2 s, low RSS) with
  identical graph counts; the codegraph clone stays unaffected.
- [ ] Gates: formatting/build/vet, full default, race (index), lint; remote
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

- (to be filled after implementation: before/after logs, gate logs)
