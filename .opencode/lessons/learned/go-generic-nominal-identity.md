# Match the nominal declaration, not the instantiation
**Date**: 2026-10-08 · **Change**: review-operational-r01-r07-r08-r14-r15 · **Category**: pattern
## What happened
A generic receiver written `Box[T]` produced the node QN `Box[T].Get`, while SSA
names the receiver `Box`; instantiations (`Box[int].Get`) are `Synthetic` and were
rejected outright. Both halves dropped every CALLS edge touching a generic method.
## How to avoid
Give the extractor and the resolver the same nominal identity: unwrap a generic
receiver to its base type, and map a synthetic instantiation/wrapper to `Origin()`
before rejecting it. Check the synthetic case *before* an early `Pkg == nil` guard —
instances have no `Pkg` and would otherwise be discarded. A semantic change here
needs a resolver version bump.
## Tags
#lesson #change-review-operational-r01-r07-r08-r14-r15 #pattern
