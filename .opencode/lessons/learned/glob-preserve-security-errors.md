# Glob must not erase a filesystem security failure
**Date**: 2026-10-08 · **Change**: review-embed-r05 · **Category**: pattern
## What happened
The standard FS glob algorithm ignores directory-read errors. An unsafe parent
or canceled scan could otherwise become an ordinary empty pattern. Wrapped
missing-path errors also require errors.Is rather than os.IsNotExist.
## How to avoid
Retain filesystem security/cancellation faults outside the generic glob result;
assert unsafe-parent rejection and use descriptor-only directory enumeration.
## Tags
#lesson #change-review-embed-r05 #pattern
