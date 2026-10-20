package kubemcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoveryListsReadableTypesAndSkipsUnavailableGroups(t *testing.T) {
	api := newFakeAPI()
	d := &discovery{client: api.client(t)}

	types, err := d.all(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rt := range types {
		names = append(names, rt.Group+"/"+rt.Name)
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"/pods", "/secrets", "apps/deployments", "events.k8s.io/events", "a.example.com/widgets"} {
		if !strings.Contains(got, want) {
			t.Fatalf("types %q lack %s", got, want)
		}
	}
	for _, unwanted := range []string{"pods/log", "bindings", "metrics"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("types %q must not contain %s", got, unwanted)
		}
	}

	before := len(api.requests())
	if _, err := d.all(context.Background()); err != nil || len(api.requests()) != before {
		t.Fatalf("discovery must be cached: %d requests after %d", len(api.requests()), before)
	}
}

// cancelOn cancels a context when the path is requested, then lets the request proceed.
type cancelOn struct {
	next   http.Handler
	cancel context.CancelFunc
	path   string
}

func (c cancelOn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == c.path {
		c.cancel()
	}
	c.next.ServeHTTP(w, r)
}

func TestDiscoveryCancelledMidwayNeitherTruncatesNorPoisonsTheCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &discovery{client: testClient(t, cancelOn{newFakeAPI(), cancel, "/apis/apps/v1"}, staticTokens())}

	if types, err := d.all(ctx); err == nil {
		t.Fatalf("cancelled discovery must fail, got %d types", len(types))
	}
	types, err := d.all(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rt := range types {
		found = found || (rt.Group == "apps" && rt.Name == "deployments")
	}
	if !found {
		t.Fatalf("a later call must discover the full list, got %v", types)
	}
}

func TestDiscoveryResolve(t *testing.T) {
	d := &discovery{client: newFakeAPI().client(t)}
	cases := []struct{ name, group, wantGroup, wantName string }{
		{"pods", "", "", "pods"},
		{"po", "", "", "pods"},
		{"Pod", "", "", "pods"},
		{"pod", "", "", "pods"},
		{"deploy", "", "apps", "deployments"},
		{"Deployment", "", "apps", "deployments"},
		{"events", "", "", "events"},
		{"events", "events.k8s.io", "events.k8s.io", "events"},
		{"widgets", "b.example.com", "b.example.com", "widgets"},
		{"svc", "core", "", "services"},
	}
	for _, c := range cases {
		got, err := d.resolve(context.Background(), c.name, c.group)
		if err != nil {
			t.Errorf("resolve(%q, %q): %v", c.name, c.group, err)
			continue
		}
		if got.Group != c.wantGroup || got.Name != c.wantName {
			t.Errorf("resolve(%q, %q) = %s/%s; want %s/%s", c.name, c.group, got.Group, got.Name, c.wantGroup, c.wantName)
		}
	}
}

func TestDiscoveryResolveErrors(t *testing.T) {
	d := &discovery{client: newFakeAPI().client(t)}
	ctx := context.Background()
	if _, err := d.resolve(ctx, "widgets", ""); err == nil || !strings.Contains(err.Error(), "a.example.com, b.example.com") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := d.resolve(ctx, "gizmos", ""); err == nil || !strings.Contains(err.Error(), "list_resource_types") {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := d.resolve(ctx, " ", ""); err == nil {
		t.Fatal("empty name must fail")
	}
}

func TestResourceTypePath(t *testing.T) {
	pods := resourceType{Name: "pods", Version: "v1", Namespaced: true}
	deployments := resourceType{Name: "deployments", Group: "apps", Version: "v1", Namespaced: true}
	nodes := resourceType{Name: "nodes", Version: "v1"}
	cases := map[string]string{
		pods.path("", ""):                    "/api/v1/pods",
		pods.path("web", ""):                 "/api/v1/namespaces/web/pods",
		pods.path("web", "api-0"):            "/api/v1/namespaces/web/pods/api-0",
		deployments.path("web", "api"):       "/apis/apps/v1/namespaces/web/deployments/api",
		nodes.path("ignored", "ip-10-0-1-2"): "/api/v1/nodes/ip-10-0-1-2",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
	}
}
