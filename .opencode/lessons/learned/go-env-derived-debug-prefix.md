# Derived tool output can contain ephemeral identity
**Date**: 2026-10-08 · **Change**: review-inputs-r04-r05 · **Category**: tool
## What happened
Two consecutive go env GOGCCFLAGS outputs contained different random go-build
paths. Hashing the unfiltered output would invalidate every scan and prevent
stable handoff, despite identical compiler semantics.
## How to avoid
Inspect repeated official tool output before making it a certificate. Exclude
proven derived ephemeral fields while retaining their semantic inputs; test no-op.
## Tags
#lesson #change-review-inputs-r04-r05 #tool
