# Ignored inserts retain the previous connection rowid
**Date**: 2026-10-07 · **Change**: review-storage-security-r02-r09-r17 · **Category**: anti-pattern
## What happened
`INSERT OR IGNORE` followed by `LastInsertId` attached duplicate-node terms to another node's FTS row.
Test-first fixtures reproduced false search hits and blocked atomic indexing of valid repeated declarations.
## How to avoid
Check `RowsAffected` before obtaining the rowid or writing dependent records; propagate driver errors.
Assert exact search identities and `ValidateIntegrity` for interleaved and cross-batch duplicates.
## Tags
#lesson #change-review-storage-security-r02-r09-r17 #anti-pattern
