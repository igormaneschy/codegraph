# Continue by key, not by offset
**Date**: 2026-10-08 · **Change**: review-perf-p2-p3 · **Category**: pattern
## What happened
Neighbor pages used `ORDER BY n.qualified_name LIMIT ? OFFSET ?`. The plan was
already index-driven, but SQLite still had to join, sort, and then discard every
row before the offset: page 500 at offset 4500 on a 5000-caller hub cost 4.39 ms
versus 2.33 ms for the first page.
## How to avoid
Put the last served ordering key in the cursor and filter `> ?` in the query, so
the sort input is only the rows after the cursor. Check the plan first
(`EXPLAIN QUERY PLAN`): here the existing `idx_edges_target_type` was already
used, so no composite index was needed. Version the cursor so an old
offset-based token is rejected with orientation to restart instead of silently
repeating page one.
## Tags
#lesson #change-review-perf-p2-p3 #pattern
