# Freeze the effective environment after observation
**Date**: 2026-10-08 · **Change**: review-inputs-r04-r05 · **Category**: pattern
## What happened
Hashing process flags alone misses persisted Go configuration; hashing settings
without injecting them leaves a race before go/packages reads live defaults.
The effective observation now uses verified metadata and explicit Config.Env.
## How to avoid
Persist only identity, freeze effective values in memory, and pass them explicitly.
Use a late environment mutation regression with positive expected CALLS.
## Tags
#lesson #change-review-inputs-r04-r05 #pattern
