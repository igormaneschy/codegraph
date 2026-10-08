# Identify Node without launching it
**Date**: 2026-10-08 · **Change**: review-ts-runtime-r04 · **Category**: tool
## What happened
Running `node --version` to identify the runtime executes `NODE_OPTIONS` preloads
and reads user config before any trust decision. Resolving the launcher with
LookPath/EvalSymlinks and hashing the bytes via `securefile.OpenRead` identifies it
without execution.
## How to avoid
For identity, prefer path plus content digest over a version probe. Keep the digest
opaque and never serialize the raw settings that produced it.
## Tags
#lesson #change-review-ts-runtime-r04 #tool
