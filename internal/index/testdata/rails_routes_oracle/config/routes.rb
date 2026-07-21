Rails.application.routes.draw do
  get "health", to: "health#show"

  namespace :admin do
    get "dashboard", to: "dashboard#index"
  end

  scope path: "api" do
    post "sessions", to: "sessions#create"
  end

  resources :photos, only: [:index, :show, :new], path: "images", path_names: { new: "browse" }
  # `except: :destroy` deliberately omits DELETE /profile from the oracle.
  resource :profile, except: :destroy

  scope "public" do
    get "status", to: "health#show"
  end

  namespace :admin, path: "control" do
    get "reports", to: "reports#index"
  end

  delete "sessions/:id", to: "sessions#destroy"
  options "health", to: "health#show"
  # Keep these separate: `match ... via:` is deliberately outside extractor scope.
  put "settings", to: "settings#update"
  patch "settings", to: "settings#update"

  # These syntactically valid dynamic forms must remain excluded by codegraph.
  # Keeping them unreachable lets `rails routes` produce the same supported oracle.
  if false
    get "/dynamic/#{version}", to: "health#show"
    namespace dynamic_namespace do
      get "dashboard", to: "dashboard#index"
    end
    scope path: dynamic_prefix do
      get "status", to: "health#show"
    end
    resources dynamic_resources
    resources :records, path: dynamic_path
    resources :messages, path_names: { new: dynamic_new_name }
  end
end
