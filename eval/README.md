# eval — multi-repo quality evaluation (for the paper)

The historical `name-v1` single-repo run (ajuda-aqui, `docs/QUALITY.md`) showed
graph ≈ baseline quality at ~8× lower cost. Those permissive-name figures are not
strict-QN results and must not be compared directly to new `qualified-name-v1` runs. One private repo is not a paper. This folder is the
harness for scaling that to **N public repos** so the number is reproducible.

## How many repos

- **Pilot: 5** — validate the pipeline on public repos and *measure variance* before
  committing budget. Don't pick the final N by guess; pick it by the pilot's variance.
- **Paper: 12–15** public repos, ~12–14 questions each (~150–180 paired questions).
  The **cost** effect (≈8×, low variance) is significant by ~8 repos; the **quality**
  equivalence (small gap) needs the larger N. More than ~20 is diminishing returns for
  our focused TS/JS + Go scope (upstream's 31 covered ~15 languages — we don't).

`repos.json` is the candidate list (mixed stacks + sizes). `status` stays `candidate`
until a repo's build is verified locally.

## What makes the number credible (matters more than N)

1. **Public, well-known repos.** A reviewer must be able to reproduce it; ajuda-aqui
   (private) is for development only, never the paper.
2. **Stratified questions — done.** `quality gen` now samples symbols across the
   call-degree distribution (hub → typical → leaf), not just the top hubs, so the set
   isn't cherry-picked to where the graph wins. See `internal/quality/questions.go`.
3. **Stochasticity.** LLM answers vary; plan k=3 runs per question (or report variance).
   Not yet automated — tracked below.

## Per-repo pipeline

```bash
# 1. clone + build so the type checkers can resolve (REQUIRED)
git clone <url> eval/checkouts/<id>
cd eval/checkouts/<id> && <build>     # e.g. pnpm install  /  go mod download

# 2. index + generate the stratified question set
codegraph index eval/checkouts/<id>
codegraph quality gen eval/checkouts/<id> eval/runs/<id> <lang>

# 3. fill truth.json + answers.json via the ultracode workflow
#    (oracle + graph-only/grep-only responders + judge; see docs/QUALITY.md)

# 4. grade
codegraph quality score eval/runs/<id>     # -> eval/runs/<id>/report.md
```

`eval/checkouts/` and `eval/runs/` are gitignored (clones + per-repo artifacts).
`repos.json` and this protocol are versioned.

## Strict, complete evaluation (R13)

`quality score` defaults to **`qualified-name-v1`**, exact case-sensitive QNs for
call sets and full relative path:exact declaration line for definitions. It
requires one truth per question and one answer per question×declared mode;
default modes are graph+baseline. Empty/null/duplicate/partial runs are errors,
not scores. Verified empty call `items: []` is valid, missing/null items are not.
Open questions require independent rubric notes, answer text and finite judge
scores in `[0,1]`. See `docs/QUALITY.md` and `docs/VALIDATION_R13.md`.

The workflow oracle/baseline derive QNs from source. Graph responders take TSV
**column 4** (not the bare-name column 2), preserve file/owner homonyms, and keep
the same query/page limit through every continuation. Failed agents/judges or
incomplete pipeline results fail rather than fabricate []/zero scores. The CLI
checks the actual written artifacts; neither schemas nor write acknowledgements
certify oracle accuracy, source freshness or self-reported cost.

`graph-answers.js` is a deterministic **explicit call-only, graph-only** producer:
prepare a nonempty callers/callees-only `questions.json` and matching independent
`truth.json` in a separate run directory. It rejects non-call questions rather
than silently filtering the requested experiment. It walks to a valid terminal
trailer, retaining exact identities and counting every invocation and UTF-8 byte;
malformed/missing trailers, cursor cycles, generation changes or exhausted page
caps are errors and leave any prior answers untouched. Publication is a private
atomic replacement, including a preexisting destination symlink.

```bash
CODEGRAPH_EXE=/path/to/codegraph node eval/graph-answers.js <repo> <call-run-dir>
# Optional positional executable takes precedence over CODEGRAPH_EXE.
codegraph quality score <call-run-dir> --modes graph
node --test eval/*.test.js
```

`CODEGRAPH_EVAL_PAGE_LIMIT` defaults to 500 (valid 1..2000),
`CODEGRAPH_EVAL_MAX_PAGES` to 1000 (positive safe integer). A cap is a failure
bound, not permission to score a truncated answer. Legacy production requires
explicit `CODEGRAPH_EVAL_SCORER=name-v1` (or workflow `args.scorer`), and legacy
scoring requires `--scorer name-v1`. Never infer the method or available modes
from incomplete contents. Reports always identify the method and mode matrix.
Node producer tests run in the pinned-Node CI job; default Go tests need no Node.

## TODO before the paper run

- [ ] Verify each candidate's build in `repos.json` (flip `status` to `verified`).
- [ ] Aggregator: combine per-repo `report.md`/scores into one table
      (mean quality graph vs baseline, token/call ratios, by-type, **by-language**)
      with variance / CIs. (`codegraph quality aggregate eval/runs/` — not built yet.)
- [ ] Automate k=3 runs per question and report variance.
- [ ] Run the pilot 5, do a real power analysis, then fix the final N.
