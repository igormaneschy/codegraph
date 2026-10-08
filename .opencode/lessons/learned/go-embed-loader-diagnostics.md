# Healthy syntax loading does not validate embed assets
**Date**: 2026-10-08 · **Change**: review-embed-r05 · **Category**: tool
## What happened
LoadAllSyntax returned healthy CALLS even with a missing embed asset. Positive
CALLS assertions alone did not prove transport completeness; the negative test
exposed the driver's omitted EmbedFiles query.
## How to avoid
Request NeedEmbedFiles and assert explicit missing-asset failure alongside healthy
CALLS, snapshot selection, and equality to the original Go driver asset list.
## Tags
#lesson #change-review-embed-r05 #tool
