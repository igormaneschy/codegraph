# Scoped Rails roots need distinct helper names

**Date**: 2026-07-22
**Change**: ruby-route-literal-forms
**Category**: pattern

## What happened

Adding a second `root` route to the Rails fixture failed boot because both routes
claimed the default `root` helper name.

## How to avoid

Give scoped fixture roots an explicit `as:` prefix so the Rails oracle remains a
valid application while testing URL-prefix composition.

## Tags

#lesson #change-ruby-route-literal-forms #pattern #rails #fixture #oracle
