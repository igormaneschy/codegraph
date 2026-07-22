# Version Ruby analysis in file metadata

**Date**: 2026-07-21
**Change**: ruby-verified-static-calls
**Category**: pattern

## What happened

Hash-only no-op indexing would leave Ruby graphs unchanged after a parser or resolver
upgrade, so users would not receive new nodes or edges until editing source.

## How to avoid

Store one Ruby analysis version on Ruby File nodes and invalidate the otherwise clean
Ruby scope once when it changes.

## Tags

#lesson #change-ruby-verified-static-calls #pattern #incremental #migration
