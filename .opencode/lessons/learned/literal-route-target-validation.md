# Literal route targets still need validation

**Date**: 2026-07-22
**Change**: ruby-route-literal-forms
**Category**: pattern

## What happened

Accepting any non-empty string in `root` could emit `GET /` for a malformed Rails
target that the supported literal route contract does not recognize.

## How to avoid

Validate `controller#action` structure before emitting root routes and pin malformed
forms with a negative extractor test.

## Tags

#lesson #change-ruby-route-literal-forms #pattern #rails #routes #testing
