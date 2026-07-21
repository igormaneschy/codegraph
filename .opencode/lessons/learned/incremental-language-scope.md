# Incremental Language Scope

**Date**: 2026-07-21
**Change**: ruby-m1-structural
**Category**: anti-pattern

## What happened

Adding a discovered language without a distinct incremental scope can invalidate an
unrelated resolver, causing unnecessary re-indexing in mixed-language repositories.

## How to avoid

Map every new source extension to its own scope before adding its resolver, even when
the initial pass emits no CALLS edges.

## Tags

#lesson #change-ruby-m1-structural #anti-pattern #incremental #indexing
