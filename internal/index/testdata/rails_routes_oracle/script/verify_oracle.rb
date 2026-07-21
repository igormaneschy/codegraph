#!/usr/bin/env ruby

require_relative "../config/environment"

fixture_dir = File.expand_path("..", __dir__)
oracle_path = File.join(fixture_dir, "routes.oracle")
routes_path = File.join(fixture_dir, "config", "routes.rb")
expected = File.readlines(oracle_path, chomp: true).reject(&:empty?).sort
routes = Rails.application.routes.routes
abort "route source locations are unavailable" unless routes.all? { |route| route.respond_to?(:source_location) }

actual = routes.filter_map do |route|
  next unless route.source_location&.start_with?(routes_path)

  verb = route.verb
  next if verb.nil? || verb.empty?

  # Rails 8 renders optional formats as `(.:format)` in the route path spec.
  path = route.path.spec.to_s.delete_suffix("(.:format)")
  "#{verb} #{path}"
end.sort
abort "no fixture routes matched source locations" if actual.empty?

if actual == expected
  puts "route oracle matches Rails"
  exit 0
end

warn "missing from Rails: #{(expected - actual).join(", ")}" unless (expected - actual).empty?
warn "missing from oracle: #{(actual - expected).join(", ")}" unless (actual - expected).empty?
exit 1
