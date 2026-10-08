# TS reuse requires a complete dependency proof
**Date**: 2026-10-07 · **Change**: review-freshness-r03-r06-r12 · **Category**: pattern
## What happened
References plus one-hop relative IMPORTS missed TypeScript bindings propagated through aliases and reexports.
A real SCIP oracle reproduced an old `left` callee after the program selected `right`, despite a healthy incremental result.
## How to avoid
Invalidate all TS/JS scopes on any source transition until the resolver supplies a complete dependency model; version this policy.
Require exact expected CALLS and healthy status alongside incremental/rebuild digests, with real-resolver tests isolated in CI.
## Tags
#lesson #change-review-freshness-r03-r06-r12 #pattern
