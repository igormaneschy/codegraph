# Validation contract — personal macOS workflow baseline

Written before this validation round (2026-10-09). Governing scope:
[PERSONAL_MACOS_SCOPE.md](PERSONAL_MACOS_SCOPE.md). This milestone documents the
owner's decision and verifies existing behavior; it does not implement a resolver,
optimization, platform port or benchmark expansion.

## Assertions

- [x] Agent guidance, roadmap, architecture and benchmark follow-ups agree that
  Windows and other-OS benchmarks are outside current milestones; historical
  evidence and existing Linux CI/release availability are not rewritten.
- [x] Personal scope is durably recorded for this project, not as a global rule
  for unrelated repositories. Existing safety and explicit-merge gates remain.
- [x] On macOS, existing real-Go fixture oracles verify binding changes, certified
  unchanged behavior and rebuild equality; unobserved inputs keep no-reuse reasons.
- [x] Existing session tests verify edit/refresh without restart, coherent served
  generation, serialized query/reopen and recovery, using isolated roots.
- [x] Existing pagination/snippet tests verify full enumeration and reject stale
  cursors/content changes, rather than silently truncating results.
- [x] Opt-in real SCIP fixture tests verify changed bindings and incremental/rebuild
  equality on macOS. No hosted project or served checkout is manually indexed.
- [x] Results distinguish mocked coordinator coverage from real resolver fixtures;
  functional durations are not promoted to workload latency or performance gains.

## Quality / exit

- [x] Markdown links/diff and scope consistency reviewed; no runtime, CI, release,
  dependency or analysis/input-version changes.
- [x] Local build/vet/default tests and focused race workflow tests pass; existing
  Node harness and pinned real SCIP integration pass. Formatting/modules/lint pass.
- [ ] Reviewable docs-only PR with exact-head remote checks; no merge without approval.
- [x] Next optimization waits for owner-selected real repository/workload and a
  measured problem. Private repositories/configuration are not selected implicitly.

A fixture success is not a benchmark of the owner's workload or an assertion
that all possible external inputs are certified. Outstanding review findings
remain documented, with priority changed by the owner's operating scope.

## Evidence — 2026-10-09

Source/runtime base: `f4f77712cea9ca3fec86756a8c1dfbff9c74f1ab` (PR #23 merged),
Darwin/arm64, Go 1.27.0, Node 26.0.0, cgo enabled, production SCIP pin 0.4.0.
Only guidance/docs change in this round. Logs: `/tmp/cg-personal-macos/platform.txt`
and `/tmp/cg-personal-macos/gates.log` (ends `ALL_GATES_PASSED`).

| Coverage | Command / evidence | Boundary |
|---|---|---|
| Normal suite and tooling | `go mod verify`, `go mod tidy -diff`, `gofmt -l .`, build/vet/default tests, golangci-lint | Default Go tests remain Node-independent |
| Session lifecycle | `go test -race ./cmd/codegraph -run '^TestSession_' -count=1 -v` | Isolated real Go index/session fixtures plus injected coordination/publication cases; not a user MCP latency benchmark |
| Go freshness/admission/recovery | Focused race tests: tag change, external dependency no-reuse, GOFLAGS, prior-policy refresh, degraded retries and failed Go/TS publication | Real Go binding/rebuild oracles; failure seams are injected where needed, not claims of every external failure mode |
| Complete query/snippet pages | Focused race tests in `internal/query` and `internal/graph`: query/generation cursors, full hub walk, config changes, snippet content/replacement/oversize bounds | Deterministic fixture contracts, not whole-repository recall |
| Real TypeScript bindings | `go test -race -tags integration ./internal/index -run '^TestTSInvalidation_RealResolver' -count=1 -v -timeout 10m` | Four scenarios: transitive reexport, alias, add/delete shadowing; left→right expected CALLS and fresh-rebuild equality, 3 scopes each |
| Existing Node contracts | `node --test eval/*.test.js` | 26 pass, no skips; no new benchmark/platform harness |

No runtime regression was observed in these existing fixtures; no optimization
was selected from their execution times. Self-review preserves historical figures,
Linux automation/releases and unimplemented-platform/input limitations. Two lessons
and pinned project rule `_rules/personal-macos-scope.md` record the decision.

Next measurement is deliberately pending owner selection of a real repository and
a concrete task/pain point. No other project's files, `.env`, served graph, local
agent configuration or installed binary were changed.
