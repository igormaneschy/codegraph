# Validation contract — dead-code streaming pagination

Written before implementation, 2026-10-09. Source base: `main` / `f47bbca`.

## Problem (measured)

- On `AutoTradersOMQS-GO` (3532 raw candidates, 47 accepted after entry-point
  filtering) `dead_code` took **0.47–0.85 s**, while search/callers/neighbors/
  snippet stayed at 0.01–0.03 s. The cost scaled with the scanned candidates
  (limit 1 → 0.05 s, limit 50 → 0.47 s), not with a fixed overhead.
- The paginator pulls raw batches of 256 with `LIMIT/OFFSET`; every batch
  re-executes the ordered candidate query (scan by project, correlated
  `NOT EXISTS` probes on `idx_edges_target_type`, temp b-tree sort of all
  matches) at ~30 ms; 14 batches ≈ 0.42 s, plus ~60 ms of Go-side row scan and
  property decoding. SQL microbenchmarks: one full ordered query = **0.030 s**;
  batch 0 = 0.028 s; batch 13 = 0.030 s; the same query without `ORDER BY` =
  0.009 s. The sort+scan is repeated per batch, not the pagination itself.
- 98.7% of the raw candidates are entry points filtered in Go (`isEntryPoint`),
  so the page must read nearly the whole raw set to emit its refs.

## Behavior

- [ ] One ordered query per served page; candidates stream row by row and the
  iteration stops as soon as `pageSize+1` filtered refs are collected (sentinel
  error), closing the rows.
- [ ] Identical answer, order and `has_more`/cursor semantics: filtered offset,
  raw positions (`RawOff` resume) and pre-optimization cursors keep working.
- [ ] Memory bounded by visited rows plus the page, never by the candidate
  total; the store never materializes a batch slice.
- [ ] Store API: `ForEachDeadCodeCandidate(project, offset, visit)`; visitor
  errors propagate unchanged; a negative offset fails; exhaustion past the end
  is empty, not an error.
- [ ] No schema, identity, resolver or wire-format change; `dead_code` remains a
  candidate list with unchanged filtering rules.

## Validation

- [ ] Existing query tests stay green: full-answer equivalence, entry-point
  starvation, old cursor and tied positions, bounded allocs (comment updated to
  stream semantics — allocations stay proportional to visited rows).
- [ ] Store test rewritten for the streaming API: full-walk order/coverage,
  past-end empty, negative offset failure, and early stop (a visitor error stops
  iteration and propagates).
- [ ] Before/after measurement on the `AutoTradersOMQS-GO` clone (target
  ≤ ~0.15 s for the 47-ref page) and a regression check on the codegraph clone
  (~0.02 s before); gates: formatting/build/vet, full default, race
  (query+graph), lint; remote exact-head CI is the merge gate.

## Limits and non-goals

- The per-page sort stays O(matches): ~30 ms floor on this repository.
- No SQL pushdown of the entry-point filters (single source of truth stays in
  Go), and no per-generation candidate cache in this cut.
- No change to which symbols are reported or to the dead-code caveats.

## Recorded evidence

- (to be filled after implementation: before/after logs, gate logs)
