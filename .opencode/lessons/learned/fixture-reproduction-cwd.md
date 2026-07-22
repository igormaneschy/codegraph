# Fixture commands need their working directory

**Date**: 2026-07-21
**Change**: ruby-semantic-resolver-spike
**Category**: process

## What happened

The first documented reproduction commands used relative fixture paths but omitted
the fixture working directory, causing them to target the repository instead.

## How to avoid

Wrap fixture commands in an explicit subshell that changes into the fixture directory
and keep generated outputs outside the repository fixture.

## Tags

#lesson #change-ruby-semantic-resolver-spike #process #testing #reproducibility
