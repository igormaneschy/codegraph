# Measure production decisions, not desired scenario labels

- **Type:** pattern
- **Change:** reproducible production benchmark matrix
- **Observed:** fixed Cobra and Zustand checkouts rebuilt even without edits;
  `go-dependency-inputs-unobserved`, `go-cgo-external-inputs-unobserved` and
  `ts-runtime-inputs-unobserved` prevent trusted reuse.
- **Rule:** unchanged is an input scenario, not an instruction to force a no-op.
  Measure `RunAtomicContext`, preserve the real reason codes, and compare the
  resulting graph against a fresh production rebuild. Resolver shortcuts would
  benchmark different safety semantics.
- **Limit:** small warm-cache pilots are not universal performance or recall
  evidence. Keep native query minima so a misspelled target cannot become a
  misleadingly cheap empty walk.
