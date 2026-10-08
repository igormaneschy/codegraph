# FreeOSMemory already collects; don't pay for it twice
**Date**: 2026-10-08 · **Change**: review-perf-p1-p4-p6-p7 · **Category**: pattern
## What happened
`memory.Gate` called `runtime.GC()` and then `debug.FreeOSMemory()`. The latter
already forces a collection before returning pages, so every gate paid two
stop-the-world cycles. The similarity pass also gated once per file, so a
many-small-file repo paid a GC thousands of times to retain almost nothing.
## How to avoid
Read what the stdlib function actually does before stacking a manual step on top.
Bound child output with a tail buffer instead of `bytes.Buffer` when only a short
tail is shown, and sum RSS over the process tree when the real worker is a child
of the launched shim.
## Tags
#lesson #change-review-perf-p1-p4-p6-p7 #pattern
