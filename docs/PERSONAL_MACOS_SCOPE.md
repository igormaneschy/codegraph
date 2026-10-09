# Current scope — personal use on macOS

Decision: 2026-10-09, confirmed by Igor after PR #23. This fork is currently a
personal tool used exclusively on macOS. This decision governs the next milestones;
it is not a redesign, new platform-support promise or waiver of correctness.

## Priorities

1. Validate the daily query → edit → refresh → query workflow on macOS: expected
   CALLS, complete pagination, coherent generations, recovery and index integrity.
2. Measure a real inconvenience on a repository used by the owner, in an exclusive
   disposable copy, before choosing an optimization. Choose the repository with
   the owner; public pilots and synthetic fixtures are not representative by default.
3. Fix the demonstrated bottleneck in a bounded change with before/after evidence
   and equal complete answers. Do not implement every open review item just because
   it exists in the backlog.
4. Extend R04/R05 external-input certification selectively when it unlocks useful
   safe reuse or fixes a concrete correctness problem in that workflow. Complete
   certification of every supported environment is not a prerequisite for use.

## Deferred, not incomplete release gates

| Work | Current disposition | Reconsider when |
|---|---|---|
| Windows handles/ACL/runtime/native tests | Outside current milestones; runtime remains unsupported and unpublished | Owner needs to use Windows |
| Linux/Windows benchmark matrices | Deferred; not a gate for macOS work | Actual deployment/usage moves to that platform |
| Broad medium/large/monorepo matrices | Not mandatory coverage | Such repositories represent the owner's workload |
| macOS SCIP child-process memory instrumentation | Conditional; existing unavailable value stays explicit | Observed memory pressure needs that diagnosis |
| Search `(rank, id)` keyset | Conditional, not the automatic next implementation | Deep search paging is measurably costly in use |
| Deep staging/content reuse | Conditional, with an input-identity proof | Measured staging cost warrants the complexity |
| Full Go/npm/SCIP external-input closure | Incremental, demand-driven backlog | A bounded subset has demonstrated benefit or correctness need |

Reclassification is not closure: Windows support and complete R04/R05 coverage
have **not** been implemented. Existing benchmark artifacts retain their original
method, limitations and platform; do not reinterpret them as personal-workload
results or general performance gains.

## What remains mandatory

- Honest CALLS, exact integrity validation, no-follow confinement, observed-input
  snapshot transport, atomic publication and preservation/recovery of a valid graph.
- Uncertified inputs must keep conservative rebuild/no-reuse decisions. Accepting
  that cost is different from allowing stale CALLS or skipping hashes.
- No reading `.env` without explicit admission; no private source/settings/secrets
  in reports. Never manually index the checkout while MCP serves the same root.
- Keep the existing Linux CI, tests, race detection, lint and security gates. CI is
  useful regression automation, not a requirement to expand Linux product work
  and not a substitute for validation on the Mac actually used.
- Do not remove cross-platform code or existing release artifacts speculatively.
  Linux/macOS release availability is historical/current distribution information;
  development priority is macOS. Windows remains unsupported.
- Keep changes reviewable and validate the exact PR head. Merge still requires
  explicit authorization; permission to continue implementation is not merge approval.

## Measurement policy

Use macOS, the owner's actual usage and controlled disposable roots. Record
commit/toolchain/hardware, warm-cache policy, scenario, repetition count, complete
query counts and decisions/reasons. Measure index/refresh latency, query latency,
Go allocations, self RSS and staging work as needed for the reported pain point.
Self RSS excludes children; unavailable SCIP tree RSS is not measured zero. No
Linux measurement is needed simply to fill that field.

Existing Cobra/Zustand pilot measurements provide reproducibility and cost clues,
not proof of a problem in the owner's projects. A local fixture smoke run establishes
behavior, not benchmark significance, whole-repository recall or personal fitness.
Do not add a new metrics framework, platform matrix or speculative cache first.

See [ROADMAP.md](ROADMAP.md), [BENCH_MATRIX.md](BENCH_MATRIX.md) and
[VALIDATION_MACOS_WORKFLOW.md](VALIDATION_MACOS_WORKFLOW.md).
