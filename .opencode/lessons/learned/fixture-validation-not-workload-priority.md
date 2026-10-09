# Fixture validation does not choose workload priorities
**Date**: 2026-10-09 · **Change**: personal-macos-scope · **Category**: context

## What happened
Public pilots and synthetic resolver fixtures validate reproducibility and binding
contracts, but do not show which latency or memory problem the owner experiences.
An open optimization or unavailable metric alone is not a reason to implement it.

## How to avoid
Choose the owner's repository/task explicitly and measure the reported problem
in an isolated copy. Preserve conservative rebuilds; do not equate deferred input
certification or platform work with a completed correctness fix.

## Tags
#lesson #change-personal-macos-scope #context
