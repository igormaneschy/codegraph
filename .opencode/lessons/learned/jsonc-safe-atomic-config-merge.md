# Merge user config as JSONC, replace it atomically
**Date**: 2026-10-08 · **Change**: review-operational-r01-r07-r08-r14-r15 · **Category**: pattern
## What happened
The installer preferred `opencode.jsonc` but parsed it with plain `json.Unmarshal`,
so a commented config was rejected; a `null` document left a nil map and panicked;
and any read error (not just ENOENT) was swallowed and the file overwritten.
## How to avoid
Strip comments and trailing commas with a string-aware pass before decoding, and
reject any top-level value that is not an object. Treat only `os.IsNotExist` as
"fresh config". Replace the file through a same-directory temp file plus rename so
an interrupted write cannot truncate it. Keep a string-aware scanner — comment
markers inside string values must survive.
## Tags
#lesson #change-review-operational-r01-r07-r08-r14-r15 #pattern
