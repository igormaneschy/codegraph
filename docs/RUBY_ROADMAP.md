# Ruby and Rails Roadmap

## Purpose

Finish useful Ruby and Ruby on Rails support without weakening codegraph's
precision-first contract: a missing relationship is better than a guessed one.

This document is the active product roadmap. The broader [`ROADMAP.md`](ROADMAP.md)
records completed platform milestones and deferred work outside this focus.

## Current Baseline: R1 Structural Indexing

Status: complete locally, pending the Ruby/Rails feature commit.

- Discovery recognizes `.rb`, `.rake`, `.ru`, and `.rbi` files as Ruby.
- The official tree-sitter Ruby grammar extracts explicit `Module`, `Class`,
  `Constant`, instance-method, singleton-method, and `class << self` declarations.
- `config/routes.rb` emits `Route` nodes for receiverless literal `get`, `post`,
  `put`, `patch`, `delete`, `head`, and `options` DSL calls inside
  `Rails.application.routes.draw`.
- A route path must be a non-empty, non-interpolated string. Receiver-based calls such
  as `client.get "/health"` do not become Rails routes.
- Ruby has its own incremental scope and does not trigger Go or TypeScript call
  resolvers.
- Ruby files do not enter the TypeScript/JavaScript imports pass.

R1 deliberately excludes `CALLS`, inferred Rails declarations, resources,
namespaces, dynamic routes, runtime introspection, and metaprogramming.

## Quality Contract

Ruby must use the same pipeline contracts already proven for Go and
TypeScript/JavaScript:

- Parse and persist definitions before resolving relationships.
- Emit an edge only when both qualified-name endpoints are existing repository
  nodes; external, generated, unresolved, and ambiguous endpoints are dropped.
- Run a semantic resolver in a bounded batch per incremental scope. A resolver error
  must leave structural indexing usable and emit no partial Ruby `CALLS` edges.
- Attribute a resolved reference to its lexical containing function or method, using
  the stored function spans rather than an anonymous closure or raw source position.
- Attach resolver provenance and confidence to every semantic edge, so a future
  higher-confidence resolver can supersede it.
- Establish quality from an independent oracle, never from codegraph's own graph.

## Upstream Inputs and Local Decisions

The upstream project is a useful source of ideas, not a feature checklist.
Its current README advertises tree-sitter parsing for Ruby and rates Ruby as
"Good" structural support, but its Hybrid LSP language list does not include Ruby.
That means it cannot supply a type-aware Ruby `CALLS` resolver for this project to
adopt. Its textual fallback is incompatible with codegraph's no-guesses contract.

Useful upstream ideas to evaluate later:

- Route nodes as first-class graph entities.
- A quality corpus with explicit expected answers.
- Compressed, shareable graph artifacts after the language support is proven.

Not current Ruby/Rails goals:

- 158-language coverage, embeddings, full Cypher, graph UI, cross-repository links,
  infrastructure indexing, or a background file watcher.
- A hand-written Ruby type resolver modeled after the upstream Hybrid LSP.
- Runtime boot, Rails console access, database access, or application introspection.

Sources: [upstream README](https://github.com/DeusData/codebase-memory-mcp/blob/main/README.md),
[local upstream assessment](UPSTREAM.md), and [local architecture](ARCHITECTURE.md).

## R2 Rails Route Coverage

Goal: extend only deterministic route syntax and preserve an auditable source span
for every emitted route.

- Completed: route extraction is restricted to the `Rails.application.routes.draw` DSL
  context. Receiverless verbs elsewhere in `config/routes.rb` are not routes.
- Completed: literal `namespace`, `scope "prefix"`, and `scope path: "prefix"`
  compose URL paths when nesting and the optional `path:` override are static. Dynamic
  namespace or scope prefixes skip their subtrees.
- Add `resources` and `resource` expansion only after a table-driven implementation
  covers `only`, `except`, `path`, `path_names`, and shallow nesting semantics.
- Keep `draw`, dynamic `send`, variables, interpolation, constraints backed by code,
  engine mounts, and application-defined route macros out of scope.
- Add fixture tests for every accepted form and a negative test for every rejected
  dynamic form.

Exit gate: compare extracted literal routes against `rails routes` output captured
from versioned fixture applications. The graph must contain no route absent from the
fixture oracle for the supported subset. Capturing that oracle is test-only; indexing
must not boot a Rails application.

## R3 Static Ruby Relationships

Goal: add graph relationships whose endpoints are explicit source declarations.

- Resolve literal `require_relative` paths into `IMPORTS` edges when the target file
  exists within the indexed repository. Implement this as a Ruby-only imports pass;
  do not broaden the TypeScript/JavaScript imports pass.
- Evaluate literal `require` only for repository-local load paths that can be proven
  from project configuration; package and ambiguous load-path imports stay dropped.
- Link an explicit Rails route target such as `to: "users#show"` to an existing
  controller method only when the controller class and method are uniquely known.
- Define a dedicated edge type and query behavior before emitting route-to-handler
  relationships; they are not `CALLS`.

Exit gate: all positive and negative relationship fixtures pass, and missing or
ambiguous endpoints emit no edge.

## R4 Ruby Semantic Resolver Spike

Goal: decide whether an optional external resolver can produce enough evidence for
Ruby `CALLS` edges.

- Evaluate Rubydex and scip-ruby on the same pinned Ruby and Rails fixture corpus.
- Measure resolver startup cost, reproducibility, Ruby/Rails version support, and
  intra-repository callers/callees precision and recall.
- Require a batch invocation per `ruby` incremental scope. Build semantic edges only
  after definitions are stored, map the resolver's locations to the containing graph
  function or method, and retain only stable codegraph qualified-name mappings.
- Keep the resolver optional and fail-soft. A missing executable, invalid output, or
  unsupported project must leave structural indexing usable; it must not retain a
  partial set of Ruby `CALLS` edges.
- Preserve resolver provenance and confidence in the edge properties, matching the
  existing Go and TypeScript/JavaScript contract.

Exit gate: publish a `docs/QUALITY.md` result with an agreed corpus and thresholds.
The resolver must improve answer quality without introducing false `CALLS` edges.
Otherwise Ruby remains structural-only.

## R5 Ruby Calls and Release Gate

Goal: ship Ruby `CALLS` only if R4 proves it safe.

- Integrate the selected resolver behind the Ruby incremental scope.
- Drop unresolved, external, generated, ambiguous, and metaprogrammed call targets.
- Preserve existing Go and TypeScript/JavaScript behavior and resolver isolation.
- Dogfood on at least one non-trivial Rails application and one plain Ruby project.
- Document supported Ruby, Rails, and resolver versions plus known limitations.

Release gate:

- `go test ./... -count=1`, `go vet ./...`, `go build ./cmd/codegraph`, and
  `git diff --check` pass.
- Ruby fixture tests cover all supported constructs and their negative boundaries.
- Definitions reach 100% file-and-line accuracy on the fixture corpus. Ruby semantic
  edges have zero false positives in the curated oracle corpus; unresolved targets are
  accepted misses, never guessed edges.
- The quality harness publishes separate Ruby callers, callees, definitions, and open
  comprehension scores. Its mean must reach the current TS/JS baseline of 89% before
  Ruby `CALLS` is released; known gaps are documented in `docs/QUALITY.md`.
- `CHANGELOG.md`, `ARCHITECTURE.md`, and `ROADMAP.md` reflect the delivered scope.

## Decision Rules

- Do not add a relationship because it is conventional in Rails; emit it only from
  explicit syntax or a resolver with verifiable evidence.
- Prefer a narrowly supported, well-tested form over broad heuristic parsing.
- New Ruby work must remain isolated from Go and TypeScript/JavaScript resolver scopes.
- A feature that needs a Rails process, database, or runtime application state is out
  of scope unless the product direction explicitly changes.
