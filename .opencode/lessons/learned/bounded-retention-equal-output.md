# Bound retention during the scan, not after it
**Date**: 2026-10-08 · **Change**: review-perf-p1-p4-p6-p7 · **Category**: pattern
## What happened
The similarity pass appended every emitted edge (up to `MaxPairs` = 250k) and only
then sorted and truncated to `MaxEdges` = 50k. On a clone bomb it held ~140 MB to
keep 500 edges.
## How to avoid
Keep a bounded max-heap of the retained keys as you scan: push until full, then
replace the root whenever a smaller key arrives. The retained set is exactly the
sorted prefix the collect-then-truncate produced, so output and coverage are
byte-identical — only the memory bound changes (from `MaxPairs` to `cap`). Pin
that equivalence with a reference test rather than a version bump.
## Tags
#lesson #change-review-perf-p1-p4-p6-p7 #pattern
