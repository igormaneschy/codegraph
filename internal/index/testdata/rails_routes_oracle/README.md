# Rails route oracle fixture

This fixture pins Rails 8.0.2 and exercises only the literal route forms supported
by codegraph. It is a minimal bootable Rails application: from this directory, run
`bundle install` followed by `bundle exec ruby script/verify_oracle.rb`. The verifier
boots Rails, normalizes application routes to `VERB PATH`, and compares them with
`routes.oracle`. Controller names, route helpers, and internal routes are excluded
because codegraph models only HTTP method and path.

The unreachable branch contains interpolation, dynamic namespace/scope prefixes, and
dynamic resource options. They must not appear in the oracle or in extracted route
nodes; it exists to preserve the dynamic rejection boundary without altering Rails'
captured route output. `ruby_test.go` verifies codegraph's extraction boundary; the
Ruby verifier only establishes that `routes.oracle` matches the Rails fixture.

Expanded `resources` and `resource` routes all use the declaration line as their
source span; individual REST actions do not have separate source locations.
