# Ruby singleton receiver syntax is lexical

**Date**: 2026-07-21
**Change**: ruby-verified-static-calls
**Category**: anti-pattern

## What happened

An unqualified receiver in `def Gateway.authorize` or `class << Gateway` can resolve
inside the surrounding lexical namespace, so it must not be matched to
`::Gateway.authorize`.

## How to avoid

Admit static singleton targets only from root-qualified receivers or scopes whose
fully qualified owner has already been proven; test both singleton definition forms.

## Tags

#lesson #change-ruby-verified-static-calls #anti-pattern #ruby #precision
