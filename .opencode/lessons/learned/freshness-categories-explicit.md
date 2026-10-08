# "No changes" must name what it compared
**Date**: 2026-10-08 · **Change**: review-query-r10-r11-r13-r16-r18-r19 · **Category**: pattern
## What happened
`detect_changes` compared only source files, so editing `tsconfig.json`, `go.mod`,
or an ignore file returned "no changes since the last index" even though a rebuild
was required — the public message was broader than the check.
## How to avoid
Give the answer explicit categories. Compare the recorded sidecar inputs (paths and
hashes come from the last manifest) and emit a distinct `config` line, and say
plainly what is *not* compared (environment identity). An empty answer must mean
"nothing in the compared set changed", not "the index is fresh".
## Tags
#lesson #change-review-query-r10-r11-r13-r16-r18-r19 #pattern
