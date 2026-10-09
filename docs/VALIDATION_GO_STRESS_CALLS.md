# Validation contract — meaningful Go stress CALLS

Written before implementation, 2026-10-09. Source base: PR #23 / `f4f7771`.
Owner scope remains personal macOS-only; documentation PR #24 stays separate
and is not merged by permission to continue this change.

## Behavior

- [x] `TestStress_Go_VTA_LargeModule` must require a healthy, non-reused cold
  result with an attempted/successful Go resolver scope; degraded/stale or
  missing/failed/reused scope outcomes cannot count as a valid stress result.
- [x] For the existing n-file synthetic module, the independent exact oracle is
  `stress/fI.go.FnI → stress/f((I+1)%n).go.Fn((I+1)%n)` and
  `stress/fI.go.FnI → stress/fI.go.localI`, for every I. Require all and only
  these 2*n stored CALLS, including the cycle wrap and the n=1 self-call.
- [x] DEFINES/IMPORTS/SIMILAR_TO totals, high node/edge counts, missing CALLS,
  an extra or substituted same-count CALLS edge, wrong source file/project and
  store iteration errors fail the oracle. No truncating query/count-only check.
- [x] Existing 280-file fixture runs the real Go resolver on macOS and passes
  the exact oracle and store integrity. Node is not introduced as a requirement.
- [x] Keep the existing memory sampler, heap ceiling and skip policy. Collect
  oracle/integrity evidence after the measured indexing block; do not increase
  the ceiling or claim a memory/performance gain from test execution times.

## Validation / limits

- [x] Record semantic red regressions using the legacy total-edge predicate
  before replacing it; new tests must demonstrate structural-only and wrong
  same-count graphs previously accepted.
- [x] Test health/scope failures, exact call set, self/cycle boundaries and store
  errors with named fixtures/fakes. No global resolver hooks need changing.
- [x] Local formatting/modules/build/vet/default/full race/coverage/lint pass;
  targeted 280-file stress run passes on macOS. Existing opt-in real SCIP and
  Node contracts remain intact, without adding any platform/benchmark matrix.
- [x] Self-review, review/roadmap evidence and two lessons.
- [ ] Separate PR with exact-head remote checks. No merge without authorization.

This is test-only hardening, not a runtime or graph identity change. The legacy
stress test still measures `Run`'s shared indexing/memory pipeline, not the strict
`RunAtomic` freshness/publication gate; do not relabel it as certification of
that gate or every external input. Existing production-path tests cover those
separate contracts. Public/self/TS opt-in stress cases are not broadened here;
never run self-indexing on the checkout served by MCP.

## Recorded evidence

- `/tmp/cg-go-stress/red.log`: extracted legacy predicate accepted missing/wrong
  CALLS and invalid outcomes; negative regressions fail before replacing it.
  `red-sql-legacy.log` additionally mutates only the test oracle back to that
  predicate via a Go build overlay: a real six-node/four-DEFINES SQL graph is
  accepted with zero CALLS, so the regression correctly fails. Overlay is not
  a production indexer GOFLAGS setting and is not committed.
- `focused.log` and `race-focused-final.log`: exact 560 CALLS in the real 280-file
  module, healthy attempted/successful cold scope and store integrity. Tiny real
  `RunAtomic` modules n=1/n=2 verify self-call/cycle boundaries through the same
  independent oracle; separate literal fake oracles reject same-count substitutions,
  duplicates, missing/extra edges, wrong source file/project and invalid statuses.
- `gates.log` ends ALL_GATES_PASSED: modules/format/build/vet/full default/full race/
  coverage, real SCIP integration, lint and all 26 Node contracts. Final default,
  focused affected race and lint were repeated after adding the small boundary test
  (`default-final.log`, `race-focused-final.log`, `lint-final.log`). Test-only helpers
  are not part of Go production statement coverage; no coverage-percentage claim
  is made for the oracle.
- Self-review code/tests PASS. Corpus/sampler/ceiling/skip policy and all runtime,
  dependency, CI, release and analysis/input versions unchanged. No source copied
  from private projects, no .env read and no served-checkout self-indexing.

