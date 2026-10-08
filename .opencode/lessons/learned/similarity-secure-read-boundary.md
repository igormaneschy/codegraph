# Enrichment phases share the secure source boundary
**Date**: 2026-10-07 · **Change**: review-storage-security-r02-r09-r17 · **Category**: pattern
## What happened
Definitions used secure reads, but similarity reopened paths with `os.ReadFile`, accepting a symlink swapped between passes.
Ruby fixtures exposed the gap without an external resolver or snapshot; FIFO and regular-source cases cover rejection and useful output.
## How to avoid
Reuse `securefile.ReadFile` in every source-consuming phase, including enrichment APIs.
Test leaf/parent replacement after extraction and require typed errors, no edges, and no complete coverage on failure.
## Tags
#lesson #change-review-storage-security-r02-r09-r17 #pattern
