require "rails"
require "action_controller/railtie"

module RailsRoutesOracle
  class Application < Rails::Application
    config.load_defaults 8.0
    config.eager_load = false
    config.secret_key_base = "rails-routes-oracle-test-key"
  end
end
