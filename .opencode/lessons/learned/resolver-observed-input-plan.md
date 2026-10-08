# Stage the observed plan, not the live dependency tree
**Date**: 2026-10-08 · **Change**: review-inputs-r04-r05 · **Category**: pattern
## What happened
Securely copying a dependency tree did not prove it matched the repository scan:
installed bytes and nested trees were absent from identity, and late additions
could enter the snapshot. A typed file/directory/link plan now binds both phases.
## How to avoid
Observe admitted bytes and topology first; stage only that plan with digest/link
checks. Unknown external coverage must disable reuse, not borrow lockfile trust.
## Tags
#lesson #change-review-inputs-r04-r05 #pattern
