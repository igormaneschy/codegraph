# Validation contract — P5/P7 follow-up

## Behavior assertions (before implementation)

- [x] Internal scans/config expansion reuse a per-execution validated canonical root; public entry points still validate aliases, missing roots and non-directory roots.
- [x] Every source/config/dependency read still uses descriptor-based no-follow reads. Repository reobservations, snapshot hashes/link verification and exact integrity certification remain unchanged.
- [x] IMPORTS skips reading unsupported languages (Go), while supported TS/JS/Ruby sources still fail visibly on unsafe/unreadable inputs and emit the same edges.
- [x] Index results report sequential phase durations, successful staging writes/bytes, explicit rebuild/no-op decision reasons and actually reused resolver scopes, including no-op, failure and cancellation. Metrics never enter manifest/fingerprint/graph identity or contain raw environments/source bytes.
- [x] CLI index/bench and MCP status expose those metrics without changing existing counts, generation or trust status. SCIP peak RSS remains explicitly resolver-process-tree RSS, not indexer peak RSS.
- [x] Benchmarks compare representative config-heavy scanning and Go import collection before/after; unchanged strict no-op and rebuild graph outputs stay equivalent.

## Boundaries

No global path/content cache, no cross-run snapshot reuse, no omission of the validating repository scan. Source definitions/imports/similarity remain full passes. Files staged for Go environment observation are included in staging-write counters, alongside the resolver snapshot; byte counts mean successfully written payload, not filesystem disk occupancy. No rebuild/analysis-version bump for observational metrics alone.

## Quality gates

- [x] Focused regression tests, graph-digest/security/cancellation suites.
- [x] gofmt, module verification/tidy, build, vet, full tests, race, lint.
- [x] Real SCIP integration with race.
- Remote Linux CI is required before merge; its status is tracked on the PR rather than asserted by the local evidence above.
- [x] Self-review of production code and tests; architecture/roadmap/review evidence updated with explicit remaining work.
