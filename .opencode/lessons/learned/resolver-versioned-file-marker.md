# Version semantic passes in file metadata

**Date**: 2026-07-21
**Change**: ruby-verified-static-calls
**Category**: pattern

## What happened

Hash-only no-op indexing would leave pre-resolver Ruby graphs unchanged after a new
semantic pass shipped, so users would not receive the new edges until editing source.

## How to avoid

Store a semantic-pass version on Ruby File nodes and invalidate the otherwise clean
Ruby scope once when the version changes.

## Tags

#lesson #change-ruby-verified-static-calls #pattern #incremental #migration
