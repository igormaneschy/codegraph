# Resolver admission requires edge evidence

**Date**: 2026-07-21
**Change**: ruby-semantic-resolver-spike
**Category**: process

## What happened

Both Ruby resolver candidates started successfully on the Rails fixture, but neither
provided a verified, stable mapping from a call site to a repository-local callee.
Startup success is not sufficient evidence to enable semantic edges.

## How to avoid

Require an independent callers/callees oracle and a versioned source-location-to-QN
mapping before admitting a resolver to the indexing pipeline.

## Tags

#lesson #change-ruby-semantic-resolver-spike #process #ruby #resolver
