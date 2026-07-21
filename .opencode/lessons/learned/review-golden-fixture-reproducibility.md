# Golden Fixtures Must Be Reproducible

**Date**: 2026-07-21
**Change**: rails-route-quality-gate
**Category**: anti-pattern

## What happened

A golden file alone can silently drift from framework behavior when its capture
environment and dependency graph are not checked in.

## How to avoid

Document the capture command and commit a lockfile plus minimal boot configuration
before treating a framework-generated golden file as a quality gate.

## Tags

#lesson #change-rails-route-quality-gate #anti-pattern #fixtures #reproducibility
