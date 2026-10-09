# Probe Go controls before costly environment observation
**Date**: 2026-10-09 · **Change**: go-input-admission · **Category**: pattern

## What happened
Full `go env -json` can initialize an external GOCACHEPROG while computing costly
compiler settings. Failure then discards the effective persisted command, hiding
its coverage reason behind a generic unavailable environment.

## How to avoid
First query only GOFLAGS/GOCACHEPROG with the captured launcher/environment;
admit controls and retain them on full-observation failure. Never log raw stderr
or commands. This extra subprocess is a safety cost, not a speed optimization.

## Tags
#lesson #change-go-input-admission #pattern
