# A transport must not swallow its own failures
**Date**: 2026-10-08 · **Change**: review-query-r10-r11-r13-r16-r18-r19 · **Category**: pattern
## What happened
The JSON-RPC loop logged a malformed line and continued with no reply, ignored
`Encode` errors, replied to notifications, accepted empty required fields as
meaningful queries, and returned a fixed protocol version without negotiation — a
client could hang waiting for an answer that was silently dropped.
## How to avoid
Answer a malformed line with -32700 (null id) and keep the framed stream; treat a
request without `id` as a notification and stay silent; validate required fields as
-32602 before running anything; return the first write error from the loop; and
negotiate the protocol version from a supported set, defaulting when unknown.
## Tags
#lesson #change-review-query-r10-r11-r13-r16-r18-r19 #pattern
