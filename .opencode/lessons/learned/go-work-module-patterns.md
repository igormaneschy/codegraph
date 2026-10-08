# Load a go.work workspace module-by-module
**Date**: 2026-10-08 · **Change**: review-operational-r01-r07-r08-r14-r15 · **Category**: pattern
## What happened
`packages.Load(dir, "./...")` at a `go.work` root fails when the workspace has no
root module ("directory prefix . does not contain modules listed in go.work") and
silently omits the other modules when a root module exists. Go CALLS were degraded
or missing for every workspace repo.
## How to avoid
Parse `go.work` and pass one `./<use-dir>/...` pattern per `use` entry in a single
`packages.Load` call, so all modules share one type environment. Skip entries that
escape the repository snapshot. A repo with nested modules and no root
`go.mod`/`go.work` has no workspace to enumerate — document that contract instead
of silently claiming an empty-but-healthy graph.
## Tags
#lesson #change-review-operational-r01-r07-r08-r14-r15 #pattern
