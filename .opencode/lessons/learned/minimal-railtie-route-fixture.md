# Minimal Railtie Route Fixture

**Date**: 2026-07-21
**Change**: rails-route-quality-gate
**Category**: pattern

## What happened

Requiring `rails/all` added Action Mailbox and Active Storage routes to the fixture,
which polluted an oracle intended to represent only application routes.

## How to avoid

For a route-only fixture, require `rails` and `action_controller/railtie` rather than
all Rails engines, then verify the real `bin/rails routes --expanded` output.

## Tags

#lesson #change-rails-route-quality-gate #pattern #rails #fixtures
