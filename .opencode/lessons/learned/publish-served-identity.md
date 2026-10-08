# Publish the identity captured with the handle
**Date**: 2026-10-08 · **Change**: review-query-r10-r11-r13-r16-r18-r19 · **Category**: pattern
## What happened
A session reopened the engine under a shared lock and then read the manifest from
disk again to publish the generation. An external writer could commit a newer
generation in that window, so `status` reported G2 while pages served G1 — and the
similarity coverage shown belonged to the other graph.
## How to avoid
Capture generation, status, and coverage from the engine at open/reopen (while the
lock still pins the commit) and publish that snapshot. Compare the path only to
detect lag; never re-read it to describe what is served.
## Tags
#lesson #change-review-query-r10-r11-r13-r16-r18-r19 #pattern
