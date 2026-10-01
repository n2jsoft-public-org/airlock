package kubemcp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var summaryNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestSummarizePod(t *testing.T) {
	pod := decode(t, `{"metadata":{"name":"api-0","namespace":"web","creationTimestamp":"2026-10-01T10:00:00Z"},`+
		`"spec":{"nodeName":"ip-10-0-1-2"},"status":{"phase":"Running","containerStatuses":[`+
		`{"ready":true,"restartCount":1},{"ready":false,"restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`)
	got := summarize("Pod", pod, summaryNow)
	want := itemSummary{Name: "api-0", Namespace: "web", Age: "2h", Status: map[string]any{
		"phase": "Running", "ready": "1/2", "restarts": int64(3), "node": "ip-10-0-1-2", "reason": "CrashLoopBackOff",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %#v\nwant %#v", got, want)
	}
}

func TestSummarizeOtherKinds(t *testing.T) {
	cases := []struct {
		want      map[string]any
		kind, raw string
	}{
		{kind: "Deployment", raw: `{"spec":{"replicas":3},"status":{"readyReplicas":2}}`, want: map[string]any{"ready": "2/3"}},
		{kind: "StatefulSet", raw: `{"spec":{"replicas":1},"status":{}}`, want: map[string]any{"ready": "0/1"}},
		{kind: "DaemonSet", raw: `{"status":{"numberReady":4,"desiredNumberScheduled":5}}`, want: map[string]any{"ready": "4/5"}},
		{kind: "Node", raw: `{"metadata":{"labels":{"node-role.kubernetes.io/worker":"","kubernetes.io/os":"linux"}},"status":{"conditions":[{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":"True"}]}}`, want: map[string]any{"ready": true, "roles": []string{"worker"}}},
		{kind: "Service", raw: `{"spec":{"type":"ClusterIP","clusterIP":"10.100.0.10","ports":[{"port":80,"protocol":"TCP"},{"port":53,"protocol":"UDP"}]}}`, want: map[string]any{"type": "ClusterIP", "cluster_ip": "10.100.0.10", "ports": []string{"80/TCP", "53/UDP"}}},
		{kind: "Job", raw: `{"status":{"succeeded":1,"failed":2}}`, want: map[string]any{"succeeded": int64(1), "failed": int64(2), "active": int64(0)}},
		{kind: "Secret", raw: `{"type":"Opaque","data":{"user":"<redacted>","password":"<redacted>"}}`, want: map[string]any{"type": "Opaque", "keys": []string{"password", "user"}}},
		{kind: "Namespace", raw: `{"status":{"phase":"Active"}}`, want: map[string]any{"phase": "Active"}},
		{kind: "ConfigMap", raw: `{"data":{"a":"b"}}`, want: nil},
	}
	for _, c := range cases {
		if got := summarize(c.kind, decode(t, c.raw), summaryNow).Status; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s status = %#v, want %#v", c.kind, got, c.want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		42 * time.Second: "42s", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 47 * time.Hour: "47h", 72 * time.Hour: "3d",
	}
	for d, want := range cases {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFitResultCutsAtItemBoundary(t *testing.T) {
	big := strings.Repeat("x", 100<<10)
	items := []itemSummary{{Name: big}, {Name: big}, {Name: big}}
	kept, truncated := fitResult(items)
	if len(kept) != 2 || !truncated {
		t.Fatalf("kept %d items, truncated %v; want 2, true", len(kept), truncated)
	}
	small, truncated := fitResult([]itemSummary{{Name: "a"}})
	if len(small) != 1 || truncated {
		t.Fatalf("small result changed: %d, %v", len(small), truncated)
	}
}
