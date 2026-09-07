# Benchmark — codegraph

Reproduces the **measurable** part of the upstream headline (token + tool-call
efficiency) and adds our own indexing-speed number. Run it yourself:

```bash
codegraph bench <repo>      # re-indexes, then benchmarks the top call hubs
```

## What this measures — and what it deliberately does not

| Metric | Measured here? | Why |
|---|---|---|
| **Tokens** to answer a structural question | ✅ deterministic | the project's whole bet |
| **Tool calls** to answer it | ✅ deterministic | fewer round-trips = cheaper agent |
| **Indexing speed** | ✅ deterministic | our strongest win vs upstream |
| **Answer quality** (upstream: 83% vs 92%) | ❌ not measured | needs an LLM-as-judge over many repos; reproducing it badly is a *romantic* number, not an engineered one. Left for a separate harness. |

We only report numbers a machine can reproduce exactly. The token figure is a
**ratio** (baseline ÷ graph), so the rough `bytes/4` token estimate cancels out —
the same estimator meters both sides.

## Method

For each question "**who calls `X`?**" we compare three strategies:

| Strategy | Tokens it spends | Tool calls |
|---|---|---|
| **graph** | `callers(X)` → all pages of compact refs (one TSV line each, no source, plus a `#` trailer per page) | 1 per page |
| **baseline-window** (efficient agent) | `grep X` output + a **±10-line window** around every match | 1 grep + 1 read/file |
| **baseline-file** (typical agent) | `grep X` output + every **whole matched file** | 1 grep + 1 read/file |

- The graph side meters the **exact paged wire format the tools return** — TSV
  refs plus the `# has_more/cursor/generation` trailer, **every real page**
  walked (one metered round-trip per page). No measurement trick: the product
  emits what the benchmark counts. (Since P2 the old fixed 200-ref cap is gone;
  see `docs/EFFICIENCY_RESULTS.md`.)
- **Questions** = the top 15 *call hubs* (symbols with the most inbound `CALLS`
  edges). Deterministic, and the hardest case for grep: a real caller set it has
  to reconstruct by hand.
- **±10 lines is generous to the baseline** — more context than strictly needed —
  so the graph's win is a floor, not an inflated ceiling.
- **Why the graph wins, honestly:** it has already resolved the enclosing caller
  of every call site and dropped definitions/imports/comments/homonyms. That is
  exactly the work `grep` leaves for the agent to redo by opening files.
- **Tool-call premise:** the baseline opens one file per file-with-a-match to give
  a *complete, precise* answer (a popular symbol matches in dozens of files). A
  lazy agent that guesses from grep alone would call less — and answer worse.

## Results

### ajuda-aqui — 857 files, real NestJS + React/Next monorepo (TS/JS)

```
indexing: 857 files → 4605 nodes, 8093 edges (6 dropped) in 30.0s  (~29 files/s)

tokens (median per query):  15.3×  vs grep+window   ·  84.8× vs grep+file
tokens (total across set):  16.0×  vs grep+window   ·  74.4× vs grep+file   ← headline
tool calls (total):         graph 15  vs  baseline 511   →  34× fewer
raw tokens:                 graph 17,474 · grep+window 279,920 · grep+file 1,299,580
best case (Button):         48× window · 206× file
```

### codegraph self — 26 files (Go)

```
indexing: 26 files → 196 nodes, 302 edges in ~3.6s
tokens (median): 15.0× vs grep+window · 82.7× vs grep+file
tokens (total):  18.7× vs grep+window · 86.4× vs grep+file
tool calls:      graph 15 vs baseline 64 → 4.3× fewer
```

## The compact wire format (what got us from 8.8× to 16×)

The first cut returned a JSON array of objects. Two costs dominated, both pure
overhead:

1. **Repeated keys** — `name`/`qualified_name`/`label`/`file`/`start_line`/`end_line`
   serialized on *every* row.
2. **Repeated project prefix** — a ~40-char `D--…ajuda-aqui-2-0:` glued to every
   qualified name.

Switching the tools to a **TSV wire format** (keys once as columns, prefix stripped
and re-added on input) roughly halved the graph-side tokens (ajuda-aqui: 31,884 →
17,474) with no loss of information — the qualified name still round-trips straight
back into `callers`/`callees`. The baseline cost is fixed, so halving the graph
side doubled the ratio: **8.8× → 16.0×** (window, total). This is the project's
thesis in miniature: the win is in the *representation*, not the database.

## vs upstream (codebase-memory-mcp)

| | upstream (31-repo paper) | codegraph (ajuda-aqui) |
|---|---|---|
| Tokens | **10×** fewer | **16.0×** (window) / **74.4×** (file) |
| Tool calls | **2.1×** fewer | **34×** fewer¹ |
| Answer quality | 83% vs 92% | *not measured* |
| Index time | ~20 min / 969 files (Windows) | **30s / 857 files** (~40× faster)² |

¹ Our tool-call baseline counts one read per matched file (an upper bound for a
*complete* answer); the upstream's baseline agent likely read more selectively.
The honest, conservative token number is the **window** ratio (16.0×).

² The big gap is because we skip the upstream's on-device embedding pass
(`nomic-embed-code`, 768-dim) — the likely bottleneck. We trade `SEMANTICALLY_RELATED`
edges (a later milestone, M4) for a ~40× faster index. Our `CALLS` accuracy comes
from delegating to type checkers (scip-typescript / go callgraph), not embeddings.

## Honest reading

- The **window ratio (~15–16×)** is the number to quote — it assumes a competent
  agent that reads only the context it needs. We still beat grep there because the
  graph pre-resolves callers and filters noise.
- The **file ratio (~74×)** and **best-case (206×)** describe the common reality of
  agents that slurp whole files; useful, but the optimistic end.
- **Where grep still wins:** finding an exact string/literal, or anything in code
  the type checker can't resolve (dynamic dispatch, DI string tokens). The graph is
  a complement for *map / who-calls / understand*, not a replacement for search.

## Known biases (read before quoting)

- **Hub selection.** Questions are the top-15 inbound-`CALLS` hubs: popular
  symbols where pre-resolved callers pay off most. Low-caller questions (1–3
  callers) are under-represented, and there the per-question overhead (trailer,
  round-trip) weighs more — the ratio on those is smaller. The median ratio is
  the robust headline for this reason.
- **Same question, different semantics.** The grep side matches the hub *name*
  as a whole word — including comments, string literals, other homonyms, and
  the definition itself — then opens every matched file to disambiguate by
  hand. The graph side answers one resolved qualified name and drops all of
  that by design. The comparison is fair as *cost to reach a complete, precise
  answer*, not as *cost to emit the same bytes*: the two sides deliberately do
  different amounts of disambiguation work.
- **bytes/4.** Absolute token counts are rough (real tokenizers vary by model
  and language); only the ratio is reported, metered identically on both
  sides. Multi-page graph answers cost one round-trip per page — charged, not
  hidden.
- **Completeness is oracle-checked in tests, not in `bench` output.**
  `codegraph bench` on a real repo has no independent oracle, so its table
  reports cost, not recall. Recall is pinned separately:
  `TestGraphCompletenessAgainstIndependentOracle` indexes a 600-caller Go
  fixture and requires the exact set a go/ast oracle finds (a different
  frontend from the graph's own pipeline), and
  `TestPageSizeNeverManufacturesACheaperAnswer` requires the identical set at
  page sizes 500/50/7. On a degraded index `bench` prints the status and the
  resolver failure instead of a clean number.
- **Prepare vs query.** `bench` prints index wall time separately from query
  cost; they must not be folded into one ratio. The edit-refresh loop
  (query → edit → stale → reindex → re-query, plus amortized wall time per
  query for sessions of 1/10/100 queries) is measured by
  `bench.MeasureTrajectory` on temp fixtures — e.g. a 600-caller Go hub:
  index 1.32 s, reindex 1.57 s, query 0.027 s over 2 pages, amortized 2.91 s
  (k=1) → 0.32 s (k=10) → 0.06 s (k=100). Prepare dominates short sessions;
  per-query cost dominates long ones.

## Method history (do not compare across rows)

| Method | Wire format | Graph calls |
|---|---|---|
| `tsv-uncapped-v0` (pre-P2) | single-shot TSV, silent 500-cut, fixed 200-cap in bench | 1 |
| `paged-wire-v1` (P2+) | every real page + `#` trailer, 1 call/page | pages walked |

The 16.0×/74.4× ajuda-aqui numbers above are `tsv-uncapped-v0`. They stay
valid as historical results of that method — but a `paged-wire-v1` run on the
same repo is a different measurement (trailers charged, all pages walked) and
the two must not be quoted as a trend. New headline numbers require a fresh
run, stamped with its method.
