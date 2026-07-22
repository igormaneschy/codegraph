# Route roots inherit static scope prefixes

**Date**: 2026-07-22
**Change**: ruby-route-literal-forms
**Category**: pattern

## What happened

The initial `root` extractor always emitted `/`, even when the root declaration was
inside a literal `scope` or `namespace`, creating a false route.

## How to avoid

Run every route form through the same static prefix composition path and include a
scoped positive case in the Rails fixture oracle.

## Tags

#lesson #change-ruby-route-literal-forms #pattern #rails #routes #precision
