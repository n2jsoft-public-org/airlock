package kubemcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func encode(t *testing.T, obj any) string {
	t.Helper()
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(buf.String())
}

const secretJSON = `{"kind":"Secret","apiVersion":"v1","type":"Opaque",` +
	`"metadata":{"name":"db","namespace":"web","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"data\":{\"password\":\"czNjcmV0\"}}","team":"core"}},` +
	`"data":{"password":"czNjcmV0","user":"YXBw"},"stringData":{"token":"plain"}}`

func TestRedactSecret(t *testing.T) {
	obj := decode(t, secretJSON)
	redactSecrets(obj)
	out := encode(t, obj)

	for _, leaked := range []string{"czNjcmV0", "YXBw", "plain"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("secret value %q leaked: %s", leaked, out)
		}
	}
	for _, kept := range []string{`"password":"<redacted>"`, `"user":"<redacted>"`, `"token":"<redacted>"`, `"type":"Opaque"`, `"team":"core"`,
		`"kubectl.kubernetes.io/last-applied-configuration":"<redacted>"`} {
		if !strings.Contains(out, kept) {
			t.Fatalf("redacted secret lacks %s: %s", kept, out)
		}
	}
}

func TestRedactSecretListItemsWithoutKind(t *testing.T) {
	obj := decode(t, `{"kind":"SecretList","items":[{"metadata":{"name":"a"},"data":{"k":"djE="}},{"metadata":{"name":"b"},"data":{"k":"djI="}}]}`)
	redactSecrets(obj)
	if out := encode(t, obj); strings.Contains(out, "djE=") || strings.Contains(out, "djI=") || strings.Count(out, "<redacted>") != 2 {
		t.Fatalf("list not redacted: %s", out)
	}
}

func TestRedactSecretsInMixedList(t *testing.T) {
	obj := decode(t, `{"kind":"List","items":[{"kind":"Secret","data":{"k":"djE="}},{"kind":"ConfigMap","data":{"k":"visible"}}]}`)
	redactSecrets(obj)
	out := encode(t, obj)
	if strings.Contains(out, "djE=") || !strings.Contains(out, "visible") {
		t.Fatalf("mixed list = %s", out)
	}
}

func TestRedactLeavesConfigMaps(t *testing.T) {
	obj := decode(t, `{"kind":"ConfigMap","data":{"LOG_LEVEL":"debug"}}`)
	redactSecrets(obj)
	if out := encode(t, obj); !strings.Contains(out, "debug") {
		t.Fatalf("config map changed: %s", out)
	}
}
