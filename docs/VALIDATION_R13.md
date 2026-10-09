# Validation contract — R13 strict identity and complete evaluation

Written before implementation. Historical benchmark numbers remain labelled as
legacy `name-v1`; this change makes no new real-repository quality claim.

## Behavior assertions

- [x] Default scorer `qualified-name-v1` matches exact, case-sensitive, project-stripped repository QNs for callers/callees. Same name in different files/owners and Ruby instance/singleton methods remain distinct; no basename/name fallback or project-prefix guessing.
- [x] Strict definitions require a canonical repository-relative full path and positive declaration line, matched exactly. `name-v1` is explicit opt-in and retains legacy name normalization and basename/±3-line definition matching.
- [x] Both methods validate a nonempty question set with unique nonempty IDs and supported types; one truth per question; exactly one answer per requested question×mode. Default modes are graph+baseline, graph-only requires explicit selection. Missing/duplicate/unknown IDs, truths, modes and answers fail before aggregation.
- [x] Structural items must be explicit arrays (empty is valid for calls, missing/null is not evidence). Strict malformed identities/locations fail with ID/mode/item context. Definitions have one truth location; multiple guessed answer locations cannot receive perfect definition credit. Open questions require independent nonempty rubric notes, answer text and finite 0..1 judge score.
- [x] Costs are nonnegative and aggregation cannot overflow; valid scores/costs and markdown order are deterministic regardless of truth/answer ordering. Reports label the scorer and selected modes and safely escape metadata.
- [x] CLI invalid runs fail nonzero without replacing an existing report. Valid reports retain private atomic writes. Generated scaffold remains explicitly unfilled until an independent oracle supplies truth.
- [x] Graph answer producer reads exact QNs from TSV column 4 (legacy column 2 only by explicit option), deduplicates by selected identity, counts UTF-8 output bytes and every invocation, and finishes only at a valid terminal trailer. Missing/malformed trailers, nonprogressing cursors, generation changes and page-cap exhaustion fail without publishing partial answers.
- [x] Workflow oracle/baseline derive identities from source, never consult the graph. Graph hints select QNs and keep the same page limit for continuations. Failed/null oracle/responder/judge or incomplete pipeline results are errors, not fabricated empty answers/zero judge scores.

## Scope

Evaluation methods/validation/producers/docs only. No resolver, graph schema,
analysis identity or freshness changes. No LLM runs or historical results silently
converted into strict results. A call-only deterministic graph run must use an
explicit call-only question/truth set and `--modes graph`; completeness is never
inferred from whatever answers happen to be present.

## Gates

- [x] Tests for homonyms, methods/case/Ruby identity, partial/empty sets, location identity, all invalid-run classes, cost/judge boundaries, report/CLI preservation and producer pagination/failure behavior.
- [x] gofmt, module verify/tidy diff, build, vet, full tests, race and lint.
- [x] Node producer/workflow tests in the pinned-Node integration job; default Go suite/build remain Node-independent.
- [x] Real SCIP integration with race.
- [ ] Remote Linux PR checks before merge.
- [x] Code/test self-review, architecture/quality/roadmap/review records and lessons updated with limitations.

## Local evidence

Red regression log: `/tmp/cg-r13/red.log`. Full/default/coverage/build/vet/modules,
full race plus final changed-package race, real SCIP integration, lint and 16 Node
producer/workflow tests passed (`/tmp/cg-r13/`). Quality statement coverage: 96.9%.
Real synthetic Go→CLI→Node→CLI fixture retained 720 same-named methods across
2 pages against source-generated truth (strict F1 100%; no real-repo quality claim).
Code/test self-review: PASS; historical figures remain labelled legacy.
