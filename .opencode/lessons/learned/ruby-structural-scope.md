# Ruby Structural Scope

**Date**: 2026-07-21
**Change**: ruby-m1-structural
**Category**: pattern

## What happened

Ruby and Rails expose many dynamic declarations, while explicit classes, modules,
methods, constants, and literal routes are safe to index from syntax alone.

## How to avoid

Keep structural extraction separate from semantic resolvers and do not emit inferred
CALLS or Rails-generated declarations without an evidence-producing resolver.

## Tags

#lesson #change-ruby-m1-structural #pattern #ruby #rails
