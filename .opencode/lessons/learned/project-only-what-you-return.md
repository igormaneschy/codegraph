# Select only what the answer returns
**Date**: 2026-10-08 · **Change**: review-perf-p2-p3 · **Category**: pattern
## What happened
The compact ref tools selected every node column (including the properties JSON)
and unmarshalled it, only to return name, qualified name, label, file, and lines.
On a 500-caller hub that was 657 KB and 18k allocations to build 500 tiny refs.
## How to avoid
Give the hot read path its own projection (`RefNode` + a shared query builder) and
prove equivalence with a test comparing the compact path against the full-node
path across orderings and directions. Watch the *unskipped* work too: a deep
snippet page counted skipped lines by materializing a string per line; a chunked
skip keeps the digest and the line ceiling while dropping 94% of the allocations.
## Tags
#lesson #change-review-perf-p2-p3 #pattern
