# Reproducible indexing matrix — production-matrix-v1

This is an opt-in **production indexing** pilot, not a benchmark against another
version or an LLM evaluation. `codegraph bench` remains compatible. Contract:
[VALIDATION_BENCH_MATRIX.md](VALIDATION_BENCH_MATRIX.md).

## Reproduce

Build with cgo and the Go version in `go.mod`. Use **exclusive disposable clones**,
never a checkout served by MCP. The runner rejects its own checkout; it cannot
detect every other running server. Stop any server using the pilot roots.

```sh
go build -o /tmp/codegraph-perf-codegraph ./cmd/codegraph
mkdir -p /tmp/codegraph-perf
git clone https://github.com/spf13/cobra.git /tmp/codegraph-perf/cobra
git -C /tmp/codegraph-perf/cobra checkout 40b5bc1437a564fc795d388b23835e84f54cd1d1
git clone https://github.com/pmndrs/zustand.git /tmp/codegraph-perf/zustand
git -C /tmp/codegraph-perf/zustand checkout c87a5d69ad2662be99a5caa34ba1b9027b6842a4
(cd /tmp/codegraph-perf/cobra && go mod download)
(cd /tmp/codegraph-perf/zustand && npx --yes pnpm@8.15.0 install --frozen-lockfile --ignore-scripts)
```

The npm version matches Zustand's pinned `packageManager`. Install scripts are
disabled. Dependency setup/downloads are not matrix timings. SCIP TypeScript is
pinned to 0.4.0 by the production bridge; prime its npm cache before benchmarking
if you want the same warm-cache policy. Node is needed for this opt-in runner and
TS resolution, not for the default Go build/test suite.

Create an admitted plan with physical paths (macOS `/tmp` is usually an alias):

```sh
node - <<'JS'
const fs = require('node:fs')
const plan = JSON.parse(fs.readFileSync('eval/perf-pilot.json', 'utf8'))
for (const dataset of plan.datasets) dataset.root = fs.realpathSync(dataset.root)
fs.writeFileSync('/tmp/codegraph-perf-plan.json', JSON.stringify(plan, null, 2))
JS
node eval/perf-matrix.js /tmp/codegraph-perf-plan.json /tmp/codegraph-perf-results \
  /tmp/codegraph-perf-codegraph --allow-edits
```

No arbitrary plan commands, clone or dependency install are run by the matrix.
Git HEAD, tracked cleanliness, admitted tracked edit paths and SHA-256s are
checked. Tracked `.env*` inputs are rejected without reading them. Do not introduce
private environment files. The public pilot does not need them.

The runner appends explicit source/config suffixes via the no-follow Go worker.
It retains original bytes in an owner-only backup outside the checkout and
restores in `finally`. Restoration is armed **before** sending an edit request,
so a lost JSON reply is not a lost restore instruction. Changed originals/backups
or symlinks fail closed; cleanup failure retains the working directory/backup for
manual recovery. This is not an atomic compare-and-swap editor for shared mutable
repositories: exclusive disposable ownership is required. SIGINT/SIGTERM wait for
worker shutdown, then restore; SIGKILL/machine loss cannot run `finally`. On failure
inspect retained `*-source-edit-*` directories and their `original` file; do not
force overwrite a concurrently modified input. The prior `matrix.json` stays
untouched unless the complete run succeeds. Successful scratch databases are
removed; the final raw+summary artifact is a private atomic replacement.

## Scenarios and clocks

For each dataset and repetition, separate private databases measure:

1. **cold**: no codegraph DB; OS/compiler/npm caches are warm, not flushed.
2. **unchanged**: seed healthy DB, then run the real refresh without an edit.
3. **source-edit**: seed healthy DB, append two functions with one explicitly
   expected CALLS edge, refresh, and verify exact repository-qualified identities.
4. **config-edit**: seed healthy DB, append a valid comment to go.mod/tsconfig.
   This changes input identity without pretending it changes bindings.
5. **degraded-recovery**: first observe degraded output (Go undefined call / TS
   hidden Node via empty PATH), restore inputs/runtime, and require healthy recovery.

Each operation runs in a fresh Go worker process. Only `RunAtomicContext` is timed
for index wall time and Go `TotalAlloc`/`Mallocs` deltas. Setup, graph integrity,
logical digest comparison, complete query walks and the separate fresh production
reference rebuild are excluded. Every measured graph must equal its reference.
Query clocks include walking/serializing all pages, not index time or store oracle
enumeration. An enumeration checks ordered exact QNs against the same graph;
only the added one-edge probe is an independent source-binding assertion. It does
not establish whole-repository CALLS recall or answer quality.

`RUSAGE_SELF` reports the actual OS high-water RSS of the fresh indexer worker up
to the end of indexing, including startup and excluding child processes. It is
not total memory across all processes. Go allocation totals exclude cgo/children.
Production SCIP process-tree RSS is sampled separately on Linux; on macOS its
zero is labelled **unavailable**, not zero memory. No global memory cap is changed.

Raw `matrix.json` retains every sample, phase, reason, query count/page/byte/digest,
edit evidence, equivalence check and observed-input/environment digest. It never
stores original bytes or raw environment settings. Reports carry binary SHA/build,
Go and bridge versions, platform/CPU and pinned dataset revisions. Provenance does
not close uncertified external Go/npm inputs. p50/p95 use nearest rank with the
sample count; at n=3, p95 is simply the largest observed sample, not significance.

## Pilot evidence

Raw artifact: [`eval/results/perf-pilot-darwin-arm64.json`](../eval/results/perf-pilot-darwin-arm64.json),
SHA-256 `75ab03f74aee1898d38593a23cc41ccbe82907cf819f299f9741bd41d8117fb2`.
macOS/arm64 Apple M1 (8 CPUs), Go 1.27.0, Node 26.0.0, bridge SCIP 0.4.0;
production binary SHA/build recorded in the artifact. It was built from the
reviewed working tree (build metadata says main + dirty), not a release.
Warm dependency caches; **n=3 per row, 30 measured operations**, no statistical
comparison with another version. Setup/reference processes are additional.

| Dataset/scenario | Wall p50 / p95 (s) | Go alloc p50 (MB) | Self RSS p50 (MB) | Staging p50 (MB) |
|---|---:|---:|---:|---:|
| Cobra cold | 3.092 / 3.256 | 889.3 | 596.9 | 0.511 |
| Cobra unchanged | 2.792 / 3.725 | 889.4 | 608.5 | 0.511 |
| Cobra source edit | 2.690 / 6.134 | 889.5 | 603.3 | 0.511 |
| Cobra config edit | 2.927 / 3.111 | 890.0 | 610.5 | 0.511 |
| Cobra degraded recovery | 2.503 / 2.694 | 889.9 | 614.8 | 0.511 |
| Zustand cold | 35.875 / 49.842 | 3271.4 | 78.8 | 142.328 |
| Zustand unchanged | 34.710 / 37.009 | 3284.8 | 81.1 | 142.328 |
| Zustand source edit | 31.202 / 42.750 | 3284.8 | 82.5 | 142.328 |
| Zustand config edit | 35.632 / 37.463 | 3285.0 | 82.2 | 142.328 |
| Zustand degraded recovery | 31.467 / 34.305 | 3284.6 | 80.0 | 142.328 |

MB is decimal. All operations were healthy **rebuilds**, including unchanged:
Cobra reports uncertified dependency/cgo inputs; Zustand reports
`ts-runtime-inputs-unobserved`. No certification was disabled to obtain a no-op.
All 30 digests matched fresh production rebuilds; both degraded seeds were
observed, and the three source-edit repetitions per dataset verified the exact
independent probe edge. Native query minima reject empty-target artifacts:
Cobra callers 132 refs / 19 pages, callees 18 / 3, search 310 / 45; Zustand
callers 4 / 4, callees 4 / 2, search 2 / 2. Queries are deliberately paged, not
an exhaustive recall oracle. Initial exploratory empty-target queries prompted
the minima; those exploratory artifacts are not the final checked-in run.

Zustand stages ~142 MB / 14,576 payloads per run; observation, handoff and staging
are prominent phases. This motivates measurement-led closure/reuse work, not
skipping certification. macOS SCIP tree RSS is unavailable and explicitly labelled.
Variance is substantial (e.g. Cobra source-edit p95); thermal scheduling, OS
activity and cache state are uncontrolled. Earlier exploratory runs differed,
so do not treat these figures as a regression/gain or significance estimate.
Two small repositories are not the medium/large/monorepo matrix. They establish
reproducibility and expose production costs, not universal performance gains.

## Next bounded work

Per [PERSONAL_MACOS_SCOPE.md](PERSONAL_MACOS_SCOPE.md), choose an owner-used
repository/workflow before the next measurement, use an exclusive disposable copy,
and measure on macOS. Linux/Windows matrices are deferred; large/monorepo coverage
is needed only when representative of actual use. Keep the existing Linux CI,
without adding cross-platform benchmark jobs. Historical pilot data stays unchanged.

Use existing wall/allocation/self-RSS/staging diagnostics to investigate a concrete
problem first. macOS SCIP child-RSS instrumentation is conditional on observed
memory pressure, not a reason to benchmark elsewhere. Public pilot/fixture success
is not personal-workload or general performance evidence. Search keyset, deeper
staging reuse and bounded external-input certification need a demonstrated benefit
and their own contracts. Do not skip hashes, copy unobserved dependencies or force
a no-op to improve the chart.
