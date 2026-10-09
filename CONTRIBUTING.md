# Contributing to codegraph

Thanks for helping. codegraph is a small, opinionated Go project — a token-efficient
code knowledge graph for AI agents. This guide is short on purpose.

## Prerequisites

- **Go 1.26+**.
- **A C compiler** (gcc/clang) — the build links tree-sitter via cgo, so
  `CGO_ENABLED=1` is required.
- **Node.js** — at *index time* for TypeScript/JS repos (scip-typescript runs via
  `npx`), and for the opt-in real-resolver integration tests below. Not needed to
  build or run the default Go test suite.

### Supported platforms

Linux and macOS (amd64/arm64) are supported and released. Windows builds compile
but the **runtime is not supported**: `internal/securefile` fails closed with
`ErrUnsupported` for the private cache/snapshot/manifest operations, so indexing,
stats, and MCP cannot work there. No Windows release asset is published while
that holds (see R01 in `docs/CODE_REVIEW_2026-10-07.md`).

Current owner usage is personal and macOS-only. Follow
[`docs/PERSONAL_MACOS_SCOPE.md`](docs/PERSONAL_MACOS_SCOPE.md) for prioritization:
retain the Linux CI/gates, but do not add Windows work or other-OS benchmark matrices
without an actual usage need. Existing release availability is not a roadmap mandate.

```bash
go build -o codegraph ./cmd/codegraph    # or: make build
make test                                 # or: go test ./...

# Opt-in TS binding oracles: real SCIP, expected CALLS, incremental == rebuild.
# CI pins Node 26.0.0 and SCIP TypeScript 0.4.0 in a separate job.
go test -race -tags integration ./internal/index \
  -run '^TestTSInvalidation_RealResolver' -count=1 -timeout 10m
```

## The loop (small, reviewable increments)

1. **One change at a time.** Keep PRs focused and small enough to review in minutes.
2. **Test-first where it earns its keep** — the store, query, discover, and the call
   resolvers all have testable contracts. A behavior change should come with a test
   that describes it.
3. **Green gate before every commit:** `go build ./...`, `go vet ./...`,
   `go test ./...`, and `gofmt`. CI runs the same on every PR.
4. **Conventional Commits** — `type(scope): description` in English
   (`feat(query): ...`, `fix(gocalls): ...`, `docs: ...`).

## Design principles (don't violate without a reason)

- **Honest precision.** An edge whose endpoints aren't both real nodes is *dropped*,
  never guessed. A missing edge beats a wrong one — an agent that trusts the graph
  must never be sent the wrong way.
- **Delegate to the type checker.** Call resolution is delegated to scip-typescript
  (TS/JS) and go/packages (Go), not re-implemented. New language support should follow
  the same bet.
- **Storage is trivial on purpose.** The value is in the edges and the compact-ref
  protocol, not the database.
- **Token-efficient by construction.** Queries return compact refs, never source;
  code comes only via `snippet`.

See `docs/ARCHITECTURE.md` for the design and `docs/ROADMAP.md` for what's planned.

## Pull requests

`main` is protected: it does not accept direct pushes (except by admins). Open a PR
from a branch; CI must pass before merge.

- Branch, commit, push, open a PR against `main`.
- Make sure CI is green (build, vet, test, gofmt).
- Describe what changed and why; link an issue if there is one.

## Commit authorship

New commits in this fork use the owner identity `Igor Maneschy <igor@maneschy.com>`.
Verify the effective Git identity before committing; do not assume the local
configuration is correct. Preserve historical commit authorship and the original
project's credits and license notices.
**Do not add `Co-Authored-By` trailers** (including AI/assistant trailers) — a
`commit-msg` hook strips them as a safeguard.

## Reporting bugs / ideas

Open an issue with: what you ran, the repo/stack, what you expected, and what you got.
For indexing issues, `codegraph stats <repo>` and the `dropped` count from
`codegraph index <repo>` are useful to include.
