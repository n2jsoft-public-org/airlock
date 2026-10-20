package kubemcp

import (
	"io"
	"net/http"
	"sync"
	"testing"
)

const (
	discoveryCore   = `{"versions":["v1"]}`
	discoveryCoreV1 = `{"groupVersion":"v1","resources":[` +
		`{"name":"pods","singularName":"pod","namespaced":true,"kind":"Pod","verbs":["get","list","watch"],"shortNames":["po"]},` +
		`{"name":"pods/log","singularName":"","namespaced":true,"kind":"Pod","verbs":["get"]},` +
		`{"name":"namespaces","singularName":"namespace","namespaced":false,"kind":"Namespace","verbs":["get","list"],"shortNames":["ns"]},` +
		`{"name":"nodes","singularName":"node","namespaced":false,"kind":"Node","verbs":["get","list"],"shortNames":["no"]},` +
		`{"name":"secrets","singularName":"secret","namespaced":true,"kind":"Secret","verbs":["get","list"]},` +
		`{"name":"services","singularName":"service","namespaced":true,"kind":"Service","verbs":["get","list"],"shortNames":["svc"]},` +
		`{"name":"events","singularName":"event","namespaced":true,"kind":"Event","verbs":["get","list"],"shortNames":["ev"]},` +
		`{"name":"bindings","singularName":"binding","namespaced":true,"kind":"Binding","verbs":["create"]}]}`
	discoveryGroups = `{"groups":[` +
		`{"name":"apps","preferredVersion":{"groupVersion":"apps/v1","version":"v1"}},` +
		`{"name":"events.k8s.io","preferredVersion":{"groupVersion":"events.k8s.io/v1","version":"v1"}},` +
		`{"name":"metrics.k8s.io","preferredVersion":{"groupVersion":"metrics.k8s.io/v1beta1","version":"v1beta1"}},` +
		`{"name":"a.example.com","preferredVersion":{"groupVersion":"a.example.com/v1","version":"v1"}},` +
		`{"name":"b.example.com","preferredVersion":{"groupVersion":"b.example.com/v1","version":"v1"}}]}`
	discoveryApps = `{"groupVersion":"apps/v1","resources":[` +
		`{"name":"deployments","singularName":"deployment","namespaced":true,"kind":"Deployment","verbs":["get","list"],"shortNames":["deploy"]},` +
		`{"name":"daemonsets","singularName":"daemonset","namespaced":true,"kind":"DaemonSet","verbs":["get","list"],"shortNames":["ds"]}]}`
	discoveryEvents  = `{"groupVersion":"events.k8s.io/v1","resources":[{"name":"events","singularName":"event","namespaced":true,"kind":"Event","verbs":["get","list"]}]}`
	discoveryWidgetA = `{"groupVersion":"a.example.com/v1","resources":[{"name":"widgets","singularName":"widget","namespaced":true,"kind":"Widget","verbs":["get","list"]}]}`
	discoveryWidgetB = `{"groupVersion":"b.example.com/v1","resources":[{"name":"widgets","singularName":"widget","namespaced":true,"kind":"Widget","verbs":["get","list"]}]}`
)

// fakeAPI is a Kubernetes API answering fixed JSON per path (query ignored but the continue token,
// see routeContinue); unknown paths are 404.
type fakeAPI struct {
	routes   map[string]string
	failures map[string]int
	seen     []string
	mu       sync.Mutex
}

// newFakeAPI answers the discovery of a cluster with core, apps, events, two widget CRD groups and
// an unavailable metrics API.
func newFakeAPI() *fakeAPI {
	f := &fakeAPI{routes: map[string]string{}, failures: map[string]int{}}
	f.route("/api", discoveryCore)
	f.route("/api/v1", discoveryCoreV1)
	f.route("/apis", discoveryGroups)
	f.route("/apis/apps/v1", discoveryApps)
	f.route("/apis/events.k8s.io/v1", discoveryEvents)
	f.route("/apis/a.example.com/v1", discoveryWidgetA)
	f.route("/apis/b.example.com/v1", discoveryWidgetB)
	f.fail("/apis/metrics.k8s.io/v1beta1", http.StatusServiceUnavailable)
	return f
}

func (f *fakeAPI) route(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path] = body
}

// routeContinue answers body to the requests of path carrying the continue token, the next page of
// a list.
func (f *fakeAPI) routeContinue(path, token, body string) {
	f.route(path+"?continue="+token, body)
}

func (f *fakeAPI) fail(path string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[path] = status
}

// requests lists the request URIs served so far.
func (f *fakeAPI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.seen...)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.RequestURI())
	status, failed := f.failures[r.URL.Path]
	key := r.URL.Path
	if token := r.URL.Query().Get("continue"); token != "" {
		key += "?continue=" + token
	}
	body, found := f.routes[key]
	f.mu.Unlock()
	switch {
	case failed:
		w.WriteHeader(status)
		reply(w, `{"kind":"Status","message":"unavailable"}`)
	case found:
		reply(w, body)
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(w, `{"kind":"Status","message":"the server could not find the requested resource"}`)
	}
}

// reply writes body; a client that went away is not the fake's concern.
func reply(w http.ResponseWriter, body string) {
	if _, err := io.WriteString(w, body); err != nil {
		return
	}
}

func (f *fakeAPI) client(t *testing.T) *apiClient {
	t.Helper()
	return testClient(t, f, staticTokens())
}
