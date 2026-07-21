package index

import (
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

func TestDefs_Ruby_ClassModuleMethodsAndConstants(t *testing.T) {
	src := `module Billing
  DEFAULT_CURRENCY = "EUR"

  class Invoice
    def total
      0
    end

    def self.find_open
      []
    end
  end
end
`
	nodes, _ := extractDefsFromSource("p", "app/models/billing/invoice.rb", LangRuby, []byte(src))

	want := map[string]struct {
		label graph.NodeLabel
		qn    string
	}{
		"Billing":          {graph.LabelModule, "p:app/models/billing/invoice.rb.Billing"},
		"DEFAULT_CURRENCY": {graph.LabelConstant, "p:app/models/billing/invoice.rb.Billing::DEFAULT_CURRENCY"},
		"Invoice":          {graph.LabelClass, "p:app/models/billing/invoice.rb.Billing::Invoice"},
		"total":            {graph.LabelMethod, "p:app/models/billing/invoice.rb.Billing::Invoice#total"},
		"find_open":        {graph.LabelMethod, "p:app/models/billing/invoice.rb.Billing::Invoice.find_open"},
	}
	for name, expected := range want {
		n, ok := findDef(nodes, name)
		if !ok {
			t.Errorf("missing Ruby definition %q", name)
			continue
		}
		if n.Label != expected.label || n.QualifiedName != expected.qn {
			t.Errorf("%s = (%s, %s), want (%s, %s)", name, n.Label, n.QualifiedName, expected.label, expected.qn)
		}
	}
}

func TestDefs_RubySingletonClassMethods(t *testing.T) {
	src := `class Invoice
  class << self
    def overdue
      []
    end
  end
end
`
	nodes, _ := extractDefsFromSource("p", "invoice.rb", LangRuby, []byte(src))
	overdue, ok := findDef(nodes, "overdue")
	if !ok {
		t.Fatal("overdue not found")
	}
	if overdue.QualifiedName != "p:invoice.rb.Invoice.overdue" {
		t.Errorf("overdue QN = %q", overdue.QualifiedName)
	}
}

func TestRoutes_RailsLiteralVerbs(t *testing.T) {
	src := `Rails.application.routes.draw do
  get "dashboard", to: "dashboard#index"
  post "/sessions", to: "sessions#create"
  get "/users/#{id}", to: "users#show"
  client.get "/health"
  get ""
end

def helper
  get "/not-a-route"
end
`
	nodes, _ := extractDefsFromSource("p", "config/routes.rb", LangRuby, []byte(src))
	routes := map[string]graph.Node{}
	for _, node := range nodes {
		if node.Label == graph.LabelRoute {
			routes[node.Name] = node
		}
	}
	want := map[string]struct{}{
		"GET /dashboard": {},
		"POST /sessions": {},
	}
	if len(routes) != len(want) {
		t.Errorf("routes = %#v, want exactly %#v", routes, want)
	}
	for name := range want {
		if _, ok := routes[name]; !ok {
			t.Errorf("missing route %q: %#v", name, routes)
		}
	}
}

func TestRoutes_RailsLiteralScopes(t *testing.T) {
	src := `Rails.application.routes.draw do
  namespace :admin do
    get "dashboard", to: "dashboard#index"

    scope path: "api" do
      post "/sessions", to: "sessions#create"
    end
  end

  scope "public" do
    get "status", to: "status#show"
  end

  namespace :admin, path: "control" do
    get "reports", to: "reports#index"
  end

  namespace :admin, "path" => "backoffice" do
    get "audits", to: "audits#index"
  end

  scope module: "api" do
    get "unprefixed", to: "status#show"
  end

  scope do
    get "also_unprefixed", to: "status#show"
  end

  namespace do
    get "invalid_namespace", to: "skipped#show"
  end

  namespace dynamic_namespace do
    get "skipped", to: "skipped#show"
  end

  namespace :dynamic_path, path: dynamic_prefix do
    get "also_skipped", to: "skipped#show"
  end

  scope path: dynamic_prefix do
    get "still_skipped", to: "skipped#show"
  end
end
`
	nodes, _ := extractDefsFromSource("p", "config/routes.rb", LangRuby, []byte(src))
	routes := map[string]struct{}{}
	for _, node := range nodes {
		if node.Label == graph.LabelRoute {
			routes[node.Name] = struct{}{}
		}
	}
	want := map[string]struct{}{
		"GET /admin/dashboard":     {},
		"POST /admin/api/sessions": {},
		"GET /public/status":       {},
		"GET /control/reports":     {},
		"GET /backoffice/audits":   {},
		"GET /unprefixed":          {},
		"GET /also_unprefixed":     {},
	}
	if len(routes) != len(want) {
		t.Errorf("routes = %#v, want exactly %#v", routes, want)
	}
	for name := range want {
		if _, ok := routes[name]; !ok {
			t.Errorf("missing route %q: %#v", name, routes)
		}
	}
}

func TestRoutes_RailsResources(t *testing.T) {
	src := `Rails.application.routes.draw do
  resources :photos, only: [:index, :show, :new], path: "images", path_names: { new: "make" }
  resource :profile, except: :destroy
  resource :settings, only: :edit, path_names: { edit: "change" }
  resources :posts, only: :edit, path_names: { edit: "change" }
  resources :articles, only: :index do
    resources :comments
  end
  resources dynamic_resources
  resources :invalid_except, except: [:destroy, :unknown]
  resources :dynamic_path_names, path_names: { new: dynamic_name }
  resources :empty_path, path: ""
end
`
	nodes, _ := extractDefsFromSource("p", "config/routes.rb", LangRuby, []byte(src))
	routes := map[string]struct{}{}
	for _, node := range nodes {
		if node.Label == graph.LabelRoute {
			routes[node.Name] = struct{}{}
		}
	}
	want := map[string]struct{}{
		"GET /images": {}, "GET /images/:id": {}, "GET /images/make": {},
		"POST /profile": {}, "GET /profile": {}, "GET /profile/new": {}, "GET /profile/edit": {}, "PATCH /profile": {}, "PUT /profile": {},
		"GET /settings/change":  {},
		"GET /posts/:id/change": {}, "GET /articles": {},
	}
	if len(routes) != len(want) {
		t.Errorf("routes = %#v, want %#v", routes, want)
	}
	for name := range want {
		if _, ok := routes[name]; !ok {
			t.Errorf("missing route %q", name)
		}
	}
	if _, ok := routes["GET /comments"]; ok {
		t.Error("nested resources must be skipped until parent parameters are modeled")
	}
}
