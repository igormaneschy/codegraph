# Inject the observed runtime, don't just hash it
**Date**: 2026-10-08 · **Change**: review-ts-runtime-r04 · **Category**: pattern
## What happened
Hashing Node/npx and the process settings identifies the TS resolver runtime, but
resolving later from live defaults lets a late mutation or PATH swap change what
actually ran. Injecting the captured variables and launcher into every SCIP scope,
then re-hashing the executables before and after the invocation, closes that gap.
## How to avoid
Persist only a typed digest, keep effective settings in memory, pass them
explicitly to the child, and verify executable identity on both sides of the call.
When the external npm closure is unobserved, record a no-reuse reason instead of
trusting the pinned package version.
## Tags
#lesson #change-review-ts-runtime-r04 #pattern
