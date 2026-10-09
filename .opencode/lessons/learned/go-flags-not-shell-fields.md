# GOFLAGS needs the Go grammar, not strings.Fields
**Date**: 2026-10-09 · **Change**: go-input-admission · **Category**: anti-pattern

## What happened
`strings.Fields` missed whole-field quoting and `--` flag names accepted by Go.
Quoted overlay/modfile inputs reached the resolver; a same-path compiler wrapper
could certify no-op without any observation of its changed executable bytes.

## How to avoid
Mirror the documented Go field grammar (not shell escaping), normalize flag
names, and version a finite identity-only allowlist. Unknown flags stay uncertified.

## Tags
#lesson #change-go-input-admission #anti-pattern
