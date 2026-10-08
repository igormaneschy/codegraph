# A degraded graph is not a freshness certificate
**Date**: 2026-10-07 · **Change**: review-freshness-r03-r06-r12 · **Category**: anti-pattern
## What happened
A first degraded index was reused forever when sources stayed unchanged, so repairing the resolver did not recover CALLS.
Successful scopes had also lost their edges during the structural-only commit; retrying only the failed scope would remain incomplete.
## How to avoid
Keep degraded graphs readable but exclude them from no-op and CALLS reuse, retrying every applicable scope on explicit refresh.
Test transient failure, repeated failure preserving the old graph, external repair, all expected CALLS, and a later healthy no-op.
## Tags
#lesson #change-review-freshness-r03-r06-r12 #anti-pattern
