# Validation contract — Go external-input admission (R04/R05)

Written before implementation. This bounded milestone closes unsafe Go input
admission/reuse cases; it does not certify module caches, toolchain/stdlib,
external compilers/drivers/native inputs, workspaces, or npm/SCIP closure.

## Behavior assertions

- [x] GOFLAGS classification follows the Go command's field grammar: ASCII
  space/tab/newline/carriage return, single/double quotes around a whole field,
  no shell expansion/unescaping, quotes inside unquoted fields literal, and
  one/two leading dashes. Malformed quoting/nonflag fields fail with a redacted,
  actionable error rather than being treated as absent flags.
- [x] Overlay, alternate modfile, and directory-changing flags are unadmitted
  in every supported spelling, including quoted fields and `--`. Admission fails
  before resolver execution/no-op/publication and preserves prior graph/manifest.
  The implementation does not open any file named by these flags. Process
  controls are checked before Go observation; a named effective-control probe
  checks persisted settings without costly cache initialization. A failing
  control probe aborts admission with no raw stderr, while a missing Go binary
  retains the existing explicit unavailable-environment fallback.
- [x] Only a finite, value-validated set of identity-only build flags can
  participate in the existing reuse certificate. External profiles, package
  directories, tool wrappers, nested compiler/linker/assembler arguments and
  unknown/future flags record `go-build-flags-inputs-unobserved`. Gccgo keeps its
  explicit external-compiler reason. No raw flag values/commands reach admission diagnostics
  or manifests. This is coverage classification, not general cmd/go validation.
- [x] Nonempty GOCACHEPROG records `go-external-cache-inputs-unobserved`, including
  the effective persisted Go setting. It never certifies no-op/CALLS reuse.
  Actual subprocess execution remains the operator's existing Go configuration;
  this milestone does not sandbox or certify an external cache/driver.
- [x] A real pure-Go fixture with a delegating -toolexec wrapper, unchanged flag
  text and changed wrapper bytes resolves again, publishes expected CALLS and
  matches a fresh production rebuild. Same flags without wrapper edits also
  remain uncertified; an unrelated-language edit cannot reuse old Go CALLS.
- [x] Existing empty/tag-only supported Go configurations retain their behavior:
  tag changes select expected bindings and match a rebuild; unchanged admitted
  inputs can reuse after exact validation. Other existing unobserved reasons
  and TS conservative behavior remain intact.
- [x] Admission policy is versioned in the resolver-input plan so previously
  recorded certificates rebuild once. No graph schema or CALLS algorithm change.
  Raw effective environment remains memory-only; reports persist opaque digests
  and bounded reason codes, never auth/flag/cache-command contents.

## Quality gates / exit

- [x] Red regressions recorded before production changes; grammar/boundary,
  secret-redaction, quoted/double-dash rejection, effective settings, old-policy,
  no-op/CALLS-reuse and atomic preservation tests.
- [x] Local gofmt/modules verify/tidy-diff/build/vet/default/race/coverage/lint,
  plus real SCIP integration and Node harness suite; default Go suite remains
  Node-independent. Isolated Go end-to-end evidence, never index served checkout.
- [x] Code/test self-review, architecture/roadmap/review records and lessons.
- [ ] Green remote PR head; merge only after explicit authorization.

## References / scope boundary

Official `go help environment` / `go help build`; Go command
`cmd/go/internal/base/goflags.go` and `cmd/internal/quoted/quoted.go` (Go 1.27
installed, compatibility checked against project Go 1.26 in CI). No internal Go
package dependency or shell tokenizer is introduced. Unsupported flags remain
usable for an actual resolver attempt unless explicitly unadmitted above, but
cannot certify reuse. Complete external-input observation and verified transport
remain follow-up stages; no new performance or full-certification claim.

## Recorded evidence

- `/tmp/cg-go-admission/red{,-wrapper}.log`: original red regressions; quoted
  overlay reached resolver; same-path wrapper mutation/unchanged refresh could
  return healthy no-op. After review, `gates-final.log` ends ALL_GATES_PASSED:
  modules, build/vet, full default/full race/coverage, pinned real SCIP integration,
  lint and 26 Node tests. New classifier/control-probe functions: 100% statement
  coverage (not evidence of complete runtime/input coverage).
- `/tmp/cg-go-admission/e2e-{first,changed,unchanged,reference}.json`: isolated
  CLI production samples with expected `source.go.Probe → source.go.Target`,
  every round rebuilding because the wrapper is uncertified. All graph digests
  equal `885f4ebeeac42987525e3caa6d0df22bca766e1e65bd5627f229f858a0b25cb2`.
  Quoted redirection refuses publication without logging its value; committed
  sidecar hash remains `0416475e1e8fa20fff24daaafc45fdd731c2ef6d17aa3d3513f00474f87e1d51`.
- Self-review code/tests PASS after retaining persisted cache settings on costly
  observation failure, redacting control-probe failures, rejecting JSON null,
  and lint corrections for owned executable fixtures (0700). No skips or new
  Node dependency in default Go tests. The named probe adds a Go subprocess per
  observation; this is a disclosed safety cost, not a performance claim.
