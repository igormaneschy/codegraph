# No-op and failed runs still do observable work
**Date**: 2026-10-08 · **Change**: review-perf-p5-p7 · **Category**: context
## What happened
A Go no-op creates private metadata views to observe the effective environment, even without resolver staging.
MCP cancellation/reopen failure previously discarded the attempted result, hiding useful last-round work diagnostics.
## How to avoid
Count successful writes at the copy boundary, including observation views. Capture attempted-round metrics before publication and keep them separate from served identity; label sampled resolver RSS rather than claiming indexer peak RSS.
## Tags
#lesson #change-review-perf-p5-p7 #context
