# Keep stress oracles outside the memory measurement
**Date**: 2026-10-09 · **Change**: go-stress-calls · **Category**: pattern

## What happened
Strengthening a memory stress test adds graph reads, expected-identity sets and
integrity checks. Including those allocations in the existing indexing sampler
would silently change the workload and tempt an unjustified ceiling adjustment.

## How to avoid
Run the exact oracle/integrity checks after PeakHeap stops; keep corpus, sampler,
ceiling and skip policy unchanged. Distinguish Run's shared memory pipeline from
strict RunAtomic certification, which needs separate production-path evidence.

## Tags
#lesson #change-go-stress-calls #pattern
