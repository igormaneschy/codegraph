# Version scoring identity, not just the producer
**Date**: 2026-10-08 · **Change**: review-quality-r13 · **Category**: pattern

## What happened
Pagination returned complete answers, but legacy normalization still gave different
files/owners named `Run` perfect agreement. Strict QNs/full declaration locations
are now versioned separately from explicit historical name/basename scoring.

## How to avoid
Pin identity semantics in the report and test homonyms, case and member kinds.
Never silently rescore historical results under a different method or derive truth
from the graph; keep symbol suffixes opaque (quoted TS names can contain `/`).

## Tags
#lesson #change-review-quality-r13 #pattern
