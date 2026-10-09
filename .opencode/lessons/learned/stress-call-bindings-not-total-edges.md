# Stress CALLS need bindings, not aggregate edge volume
**Date**: 2026-10-09 · **Change**: go-stress-calls · **Category**: anti-pattern

## What happened
The Go stress predicate used EdgesKept, so a graph containing only DEFINES could
pass. Even a CALLS count alone accepts wrong same-count bindings or a nonhealthy
resolver outcome; a real structural SQLite fixture reproduced the false positive.

## How to avoid
Require a healthy attempted/successful cold scope and an independent exact CALLS
identity set. Include same-count wrong bindings, missing/extra calls and terminal
failure cases; never treat structural volume as proof of semantic resolution.

## Tags
#lesson #change-go-stress-calls #anti-pattern
