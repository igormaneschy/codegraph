# Rails route oracle fixture

This fixture pins Rails 8.0.2 and exercises only the literal route forms supported
by codegraph. It is a minimal bootable Rails application: from this directory, run
`bundle install` followed by `bundle exec bin/rails routes --expanded`. Normalize the
application routes to `VERB PATH`, then compare the result to `routes.oracle` before
updating that file. Controller names, route helpers, and internal routes are excluded
because codegraph models only HTTP method and path.

The unreachable branch contains interpolation, dynamic namespace/scope prefixes, and
dynamic resource options. They must not appear in the oracle or in extracted route
nodes; it exists to preserve the dynamic rejection boundary without altering Rails'
captured route output.
