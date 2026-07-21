# Versioned Route Oracle Fixture

**Date**: 2026-07-21
**Change**: rails-route-quality-gate
**Category**: pattern

## What happened

A bootable Rails fixture with a checked-in route oracle makes route extraction testable
against framework behavior without starting Rails during Go tests.

## How to avoid

Keep the framework version and its lockfile with the fixture, and compare extracted
routes exactly against the normalized captured oracle.

## Tags

#lesson #change-rails-route-quality-gate #pattern #rails #testing
