#!/usr/bin/env ruby

require_relative "../config/environment"

fixture_dir = File.expand_path("..", __dir__)
oracle_path = File.join(fixture_dir, "routes.oracle")
routes_path = File.join(fixture_dir, "config", "routes.rb")
expected = File.readlines(oracle_path, chomp: true).reject(&:empty?).sort
actual = Rails.application.routes.routes.filter_map do |route|
  next unless route.source_location&.start_with?(routes_path)

  verb = route.verb
  next if verb.nil? || verb.empty?

  path = route.path.spec.to_s.delete_suffix("(.:format)")
  "#{verb} #{path}"
end.sort

if actual == expected
  puts "route oracle matches Rails"
  exit 0
end

warn "missing from Rails: #{(expected - actual).join(", ")}" unless (expected - actual).empty?
warn "missing from oracle: #{(actual - expected).join(", ")}" unless (actual - expected).empty?
exit 1
