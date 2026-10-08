# Never let "" mean both "absent" and "unknown"
**Date**: 2026-10-08 · **Change**: review-operational-r01-r07-r08-r14-r15 · **Category**: pattern
## What happened
`@Controller(BASE)` + `@Get(PATH)` both decoded to `""`, the same as `@Controller()`
+ `@Get()`, so the NestJS extractor invented the literal route `GET /` for a path
that is not statically known.
## How to avoid
Model the value as tri-state (absent / literal / unknown). Only absent and literal
may produce output; unknown omits the node rather than guessing a default. Keep the
empty *literal* distinguishable from absent (`@Get('')` is a valid root path). Add
a regression that asserts the exact produced set, so a re-introduced default is
visible.
## Tags
#lesson #change-review-operational-r01-r07-r08-r14-r15 #pattern
