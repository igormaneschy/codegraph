# Validation contract — reproducible performance matrix

Written before code. This milestone measures production behavior; it does not
optimize resolvers or weaken no-op/input/integrity certification.

## Behavior

- [x] Versioned `production-matrix-v1` plan pins each prepared, disposable public checkout by full commit SHA; Git HEAD/clean tracked inputs and unique dataset IDs are checked before work. No automatic clone/install or arbitrary plan commands. Explicit edit admission is required; the served checkout must not be used.
- [x] Matrix has five separately prepared scenarios: cold database, unchanged refresh, source edit, config edit and degraded recovery. Every repetition starts with independent private databases; setup/reference rebuilds are not timed as measured operations. "Cold" means cold codegraph DB, not cold OS/compiler/npm caches.
- [x] Unchanged refresh reports the actual `noop`/`rebuild` decision and uncertified reasons. TS/external Go inputs must not be forced into a no-op for nicer numbers.
- [x] Source/config edits require exact input bytes/digests and a canonical confined regular file. Originals are restored in finally, including graceful cancellation/failure; no-follow writes reject symlink redirection and restore rejects changed bytes/backups. Exclusive disposable ownership is required: this is not an atomic CAS editor against all concurrent regular-file replacements; SIGKILL/machine loss requires manual recovery. No .env reads or dependency mutation. Data outside admitted disposable roots/private output is untouched.
- [x] Degraded seed is explicitly observed (Go source diagnostic or TS missing Node), then recovery must publish healthy output. A normal failure is never counted as success. Each final graph must pass integrity and match a fresh production rebuild on the same final source/config/environment.
- [x] Source probe has independently specified expected CALLS; full paginated callers/callees/search walks preserve exact QNs/order against store enumeration, reject nonprogress/generation changes/page exhaustion, and meter every page/UTF-8 byte. Query equivalence is not claimed as independent whole-repo recall.
- [x] Fresh worker process per index operation exposes structured Result.Metrics, measured RunAtomic wall time, Go TotalAlloc/Mallocs deltas and OS RUSAGE_SELF peak RSS. RSS includes worker startup but not child processes; sampled SCIP process-tree peak is separate. Go allocation deltas are not cgo/child allocations. Measurements end before graph validation/query walks.
- [x] Raw JSON retains all repetitions, counts/status/decisions/reasons/phase/staging/RSS/query data and equivalence evidence. Summary groups only the same dataset/scenario and reports sample count and nearest-rank p50/p95; low-sample p95 is descriptive, not significance. No universal improvement or real-repo answer-quality claim.
- [x] Provenance records dataset commit/input digests, codegraph binary SHA/build version, platform/architecture/CPU count, Go/Node/SCIP versions and warm dependency-cache policy; no raw environment, auth/config values or source bytes in reports.
- [x] Invalid plan/matrix/worker output or failed equivalence aborts nonzero, restores edits and does not replace a prior final report. Output files are private atomic replacements. Timeouts/cancellation stop workers; setup failures are visible.

## Scope / pilot

Opt-in Node orchestration and a Go benchmark worker; default Go build/tests remain
Node-independent. Existing `bench` output stays compatible. Prepared pilot:
Cobra v1.9.1 (`40b5bc1437a564fc795d388b23835e84f54cd1d1`) and Zustand v5.0.1
(`c87a5d69ad2662be99a5caa34ba1b9027b6842a4`). These are two small public repos,
not the full medium/large/monorepo matrix. No LLM run, automatic dependency
installation, OS-cache flush, external-input certification or Windows support.

## Gates

- [x] Admission, stats, complete walks, error/cancellation/restoration and report-preservation regression tests; worker/schema/CLI tests without Node in the Go suite.
- [x] Local Node matrix tests (26 total evaluation/matrix/worker tests; pinned Node remote CI remains the final gate); Go formatting/modules/build/vet/default/race/coverage/lint and real SCIP integration.
- [x] End-to-end pinned Go+TS pilot with raw reproducible artifacts and limitations; code/test self-review, architecture/roadmap/benchmark/review docs and two lessons.
- [ ] Remote PR checks green; merge only after explicit authorization.
