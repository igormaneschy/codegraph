# Bind a pagination cursor to content, not size
**Date**: 2026-10-08 · **Change**: review-query-r10-r11-r13-r16-r18-r19 · **Category**: pattern
## What happened
A snippet continuation compared only `st_size`. Rewriting `one\ntwo\nthree\n` as
`X\nY\ntwo\nthree\n` (same 14 bytes) made the next page return `Y` as if it were
the continuation — silently mixing two file versions and shifting lines.
## How to avoid
Carry a digest of the bytes the page actually read (a whole-file sha256 computed in
the same pass via a tee) and reject a continuation whose digest differs. Size,
inode, and mtime can be a fast reject but are not proof. Change the cursor version
so old size-only cursors are rejected with orientation to restart.
## Tags
#lesson #change-review-query-r10-r11-r13-r16-r18-r19 #pattern
