# Route Oracles Need Metadata and Spans

**Date**: 2026-07-21
**Change**: rails-route-quality-gate
**Category**: pattern

## What happened

An exact route-name oracle alone could pass while a route node carried incorrect
method, path, framework, or source-line metadata.

## How to avoid

Assert route properties and single-line source spans alongside the normalized route
set whenever the fixture has stable source locations.

## Tags

#lesson #change-rails-route-quality-gate #pattern #rails #testing
