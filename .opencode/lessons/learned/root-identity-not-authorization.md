# Root identity is not read authorization
**Date**: 2026-10-08 · **Change**: review-perf-p5-p7 · **Category**: pattern
## What happened
Each referenced config repeated physical-root spelling discovery, dominating the config-heavy scan.
A per-run validated root cut that cost, but cannot certify subsequent reads: a root can be replaced after validation.
## How to avoid
Carry canonical identity internally, not cached permission. Keep no-follow reads/reobservations/hash checks and test a root replaced by an external symlink.
## Tags
#lesson #change-review-perf-p5-p7 #pattern
