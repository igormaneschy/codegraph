# Scoped routes need controller context

**Date**: 2026-07-22
**Change**: ruby-route-literal-forms
**Category**: anti-pattern

## What happened

A URL prefix alone does not identify the controller namespace. Emitting a handler
edge inside `namespace`, `scope module:`, or `scope controller:` could link a route
to the wrong controller.

## How to avoid

Keep the Route node for static paths but drop HANDLES until the complete controller
context is modeled, including legacy `:module =>` syntax.

## Tags

#lesson #change-ruby-route-literal-forms #anti-pattern #rails #handlers #precision
