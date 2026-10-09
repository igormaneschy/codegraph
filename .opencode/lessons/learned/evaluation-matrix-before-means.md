# Admit the evaluation matrix before computing means
**Date**: 2026-10-08 · **Change**: review-quality-r13 · **Category**: anti-pattern

## What happened
Missing modes/truths, duplicate rows and failed agents could disappear into maps,
empty sets or zero judges. Admission now requires the declared question×mode
matrix and explicit known-empty evidence before any aggregation/publication.

## How to avoid
Treat missing/null as failure, not []. Page caps/cursor cycles must fail rather
than publish partial answers. Test the actual CLI argv/artifacts, not only helper
mocks: a tool-first/repo-first assumption was caught before publication.

## Tags
#lesson #change-review-quality-r13 #anti-pattern
