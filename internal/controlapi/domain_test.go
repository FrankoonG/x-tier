package controlapi

import (
	"net/http"
	"testing"
)

func TestDomainRouteSetIsClosedAndVersioned(t *testing.T) {
	routes := DomainRoutes()
	if len(routes) != 24 {
		t.Fatalf("domain route count=%d, want 24", len(routes))
	}
	seen := make(map[string]bool)
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Fatalf("duplicate domain route %s", key)
		}
		seen[key] = true
		if route.Path[:3] != "/v1" {
			t.Fatalf("unversioned domain route %s", route.Path)
		}
		if got, ok := LookupDomainRoute(route.Path, route.Method); !ok || got != route {
			t.Fatalf("route lookup failed for %s", key)
		}
	}
	if _, ok := LookupDomainRoute(CommandPath, http.MethodPost); ok {
		t.Fatal("CLI command endpoint was included in the browser domain route set")
	}
	if IsDomainPath(CommandPath) {
		t.Fatal("CLI command endpoint was classified as a domain path")
	}
	for method, mutating := range map[string]bool{
		http.MethodGet:    false,
		http.MethodPut:    true,
		http.MethodDelete: true,
	} {
		route, ok := LookupDomainRoute(DomainNodeEgressGrantsPath, method)
		if !ok || route.Mutating != mutating {
			t.Fatalf("node egress grant route %s = %+v found=%t", method, route, ok)
		}
	}
}
