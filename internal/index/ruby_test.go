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
