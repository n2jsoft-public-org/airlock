package kubemcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func testAPICluster(t *testing.T, api *fakeAPI) *apiCluster {
	t.Helper()
	c := newAPICluster(api.client(t))
	c.now = func() time.Time { return summaryNow }
	return c
}

func lastRequest(api *fakeAPI) string {
	requests := api.requests()
	return requests[len(requests)-1]
}

func TestClusterNamespaces(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces", `{"kind":"NamespaceList","items":[{"metadata":{"name":"web"},"status":{"phase":"Active"}},{"metadata":{"name":"old"},"status":{"phase":"Terminating"}}]}`)
	namespaces, err := testAPICluster(t, api).Namespaces(context.Background())
	if err != nil || len(namespaces) != 2 || namespaces[1] != (namespaceSummary{Name: "old", Phase: "Terminating"}) {
		t.Fatalf("namespaces = %+v, %v", namespaces, err)
	}
}

func TestClusterListSummarizesAndPassesSelectors(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/pods", `{"kind":"PodList","metadata":{"continue":"next-page"},"items":[`+
		`{"metadata":{"name":"api-0","namespace":"web","creationTimestamp":"2026-10-01T11:00:00Z"},"status":{"phase":"Running"}}]}`)
	out, err := testAPICluster(t, api).List(context.Background(), &listResourcesInput{
		Resource: "po", Namespace: "web", LabelSelector: "app=api", FieldSelector: "status.phase=Running", Limit: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != "Pod" || len(out.Items) != 1 || out.Items[0].Name != "api-0" || out.Items[0].Age != "1h" || out.Continue != "next-page" || out.Truncated {
		t.Fatalf("out = %+v", out)
	}
	request := lastRequest(api)
	for _, want := range []string{"/api/v1/namespaces/web/pods?", "limit=500", "labelSelector=app%3Dapi", "fieldSelector=status.phase%3DRunning"} {
		if !strings.Contains(request, want) {
			t.Fatalf("request %s lacks %s", request, want)
		}
	}
}

func TestClusterListRejectsNamespaceOfClusterScopedType(t *testing.T) {
	_, err := testAPICluster(t, newFakeAPI()).List(context.Background(), &listResourcesInput{Resource: "nodes", Namespace: "web"})
	if err == nil || !strings.Contains(err.Error(), "not namespaced") {
		t.Fatalf("err = %v", err)
	}
}

func TestClusterListSecretsShowsKeysNotValues(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/secrets", `{"kind":"SecretList","items":[{"metadata":{"name":"db"},"type":"Opaque","data":{"password":"czNjcmV0"}}]}`)
	out, err := testAPICluster(t, api).List(context.Background(), &listResourcesInput{Resource: "secrets", Namespace: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if encoded := encode(t, out); strings.Contains(encoded, "czNjcmV0") || !strings.Contains(encoded, `"keys":["password"]`) {
		t.Fatalf("out = %s", encoded)
	}
}

func TestClusterGetRedactsSecretsAndDropsManagedFields(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/secrets/db", strings.Replace(secretJSON, `"metadata":{`, `"metadata":{"managedFields":[{"manager":"kubectl"}],`, 1))
	obj, err := testAPICluster(t, api).Get(context.Background(), &getResourceInput{Resource: "secret", Namespace: "web", Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := encode(t, obj)
	if strings.Contains(encoded, "czNjcmV0") || strings.Contains(encoded, "managedFields") || !strings.Contains(encoded, `"password":"<redacted>"`) {
		t.Fatalf("object = %s", encoded)
	}
}

func TestClusterGetRedactsSecretWithoutKind(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/secrets/db", `{"metadata":{"name":"db","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"LEAKED-ANNOTATION"}},"data":{"password":"czNjcmV0"}}`)
	obj, err := testAPICluster(t, api).Get(context.Background(), &getResourceInput{Resource: "secrets", Namespace: "web", Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := encode(t, obj)
	if strings.Contains(encoded, "czNjcmV0") || strings.Contains(encoded, "LEAKED-ANNOTATION") || !strings.Contains(encoded, `"password":"<redacted>"`) {
		t.Fatalf("object = %s", encoded)
	}
}

func TestClusterEventsAreCutAtMaxResultBytes(t *testing.T) {
	api := newFakeAPI()
	message := strings.Repeat("m", 64<<10)
	items := make([]string, 8)
	for i := range items {
		items[i] = `{"type":"Normal","reason":"R","message":"` + message + `","lastTimestamp":"2026-10-01T10:00:00Z","involvedObject":{"kind":"Pod","name":"p"}}`
	}
	api.route("/api/v1/namespaces/web/events", `{"items":[`+strings.Join(items, ",")+`]}`)
	events, _, err := testAPICluster(t, api).Events(context.Background(), &listEventsInput{Namespace: "web"})
	if err != nil || len(events) == 0 || len(events) >= 8 {
		t.Fatalf("got %d events, %v", len(events), err)
	}
}

func TestClusterGetValidatesInput(t *testing.T) {
	c := testAPICluster(t, newFakeAPI())
	if _, err := c.Get(context.Background(), &getResourceInput{Resource: "pods", Name: "api-0"}); err == nil || !strings.Contains(err.Error(), "namespace is required") {
		t.Fatalf("missing namespace: %v", err)
	}
	if _, err := c.Get(context.Background(), &getResourceInput{Resource: "pods", Namespace: "web"}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("missing name: %v", err)
	}
}

func TestClusterGetRefusesHugeObjects(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/nodes/big", `{"kind":"Node","metadata":{"name":"big"},"x":"`+strings.Repeat("a", maxObjectBytes)+`"}`)
	if _, err := testAPICluster(t, api).Get(context.Background(), &getResourceInput{Resource: "nodes", Name: "big"}); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("err = %v", err)
	}
}

func TestClusterLogsPassesOptions(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/pods/api-0/log", "line 1\nline 2\n")
	out, err := testAPICluster(t, api).Logs(context.Background(), &podLogsInput{Namespace: "web", Pod: "api-0", Container: "app", TailLines: 5000, SinceSeconds: 60, Previous: true})
	if err != nil || out.Log != "line 1\nline 2\n" || out.Truncated {
		t.Fatalf("out = %+v, %v", out, err)
	}
	request := lastRequest(api)
	for _, want := range []string{"tailLines=2000", "limitBytes=262144", "container=app", "sinceSeconds=60", "previous=true"} {
		if !strings.Contains(request, want) {
			t.Fatalf("request %s lacks %s", request, want)
		}
	}
}

func TestClusterEventsNewestFirst(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/events", `{"kind":"EventList","items":[`+
		`{"type":"Normal","reason":"Pulled","message":"pulled","count":1,"lastTimestamp":"2026-10-01T10:00:00Z","involvedObject":{"kind":"Pod","name":"api-0"}},`+
		`{"type":"Warning","reason":"BackOff","message":"back-off","count":7,"lastTimestamp":"2026-10-01T11:30:00Z","involvedObject":{"kind":"Pod","name":"api-0"}},`+
		`{"type":"Normal","reason":"Scheduled","message":"assigned","eventTime":"2026-10-01T09:00:00.123456Z","involvedObject":{"kind":"Pod","name":"api-0"}}]}`)
	events, _, err := testAPICluster(t, api).Events(context.Background(), &listEventsInput{Namespace: "web", Kind: "Pod", Name: "api-0", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Reason != "BackOff" || events[0].Count != 7 || events[0].Object != "Pod/api-0" || events[1].Reason != "Pulled" {
		t.Fatalf("events = %+v", events)
	}
	if request := lastRequest(api); !strings.Contains(request, "fieldSelector=involvedObject.kind%3DPod%2CinvolvedObject.name%3Dapi-0") {
		t.Fatalf("request = %s", request)
	}
}

func TestClusterListSecretsRedactsWithoutKind(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/secrets", `{"items":[{"metadata":{"name":"db"},"data":{"password":"czNjcmV0"}}]}`)
	out, err := testAPICluster(t, api).List(context.Background(), &listResourcesInput{Resource: "secrets", Namespace: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if encoded := encode(t, out); strings.Contains(encoded, "czNjcmV0") || !strings.Contains(encoded, `"keys":["password"]`) {
		t.Fatalf("out = %s", encoded)
	}
}

func eventJSON(reason, at string) string {
	return `{"type":"Normal","reason":"` + reason + `","lastTimestamp":"` + at + `","involvedObject":{"kind":"Pod","name":"api-0"}}`
}

func TestClusterEventsFollowContinueTokens(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/events", `{"metadata":{"continue":"page-2"},"items":[`+eventJSON("Old", "2026-10-01T08:00:00Z")+`]}`)
	api.routeContinue("/api/v1/namespaces/web/events", "page-2", `{"items":[`+eventJSON("Newest", "2026-10-01T12:00:00Z")+`]}`)
	events, more, err := testAPICluster(t, api).Events(context.Background(), &listEventsInput{Namespace: "web", Limit: 1})
	if err != nil || more {
		t.Fatalf("err = %v, more = %v", err, more)
	}
	if len(events) != 1 || events[0].Reason != "Newest" {
		t.Fatalf("events = %+v, want the newest event, served on page 2", events)
	}
	if request := lastRequest(api); !strings.Contains(request, "continue=page-2") || !strings.Contains(request, "limit=500") {
		t.Fatalf("request = %s", request)
	}
}

func TestClusterEventsStopAfterTenPages(t *testing.T) {
	api := newFakeAPI()
	page := `{"metadata":{"continue":"again"},"items":[` + eventJSON("Loop", "2026-10-01T08:00:00Z") + `]}`
	api.route("/api/v1/namespaces/web/events", page)
	api.routeContinue("/api/v1/namespaces/web/events", "again", page)
	events, more, err := testAPICluster(t, api).Events(context.Background(), &listEventsInput{Namespace: "web"})
	if err != nil || !more || len(events) != 10 {
		t.Fatalf("got %d events, more = %v, err = %v; want 10 pages and more", len(events), more, err)
	}
	pages := 0
	for _, request := range api.requests() {
		if strings.HasPrefix(request, "/api/v1/namespaces/web/events") {
			pages++
		}
	}
	if pages != 10 {
		t.Fatalf("fetched %d pages, want 10", pages)
	}
}
