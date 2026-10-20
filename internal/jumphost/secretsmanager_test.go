package jumphost

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

// fakeSecretsManager serves GetSecretValue (values, by secret ID) and ListSecrets (the listed
// ARNs, tagged as the read-only secrets of secretsInstanceARN) to the SDK.
type fakeSecretsManager struct {
	t      testing.TB
	values map[string]string
	// cfg is the SDK configuration of profile dev, pointed at the fake.
	cfg    *aws.Config
	listed []string
	ops    []string
	mu     sync.Mutex
}

func installSecretsManager(t *testing.T, values map[string]string, listed ...string) *fakeSecretsManager {
	t.Helper()
	f := &fakeSecretsManager{t: t, values: values, listed: listed}
	awsapitest.Env(t, f.serve, "dev")
	cfg, err := awsapi.NewConfigs().Get(context.Background(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = &cfg
	return f
}

func (f *fakeSecretsManager) serve(w http.ResponseWriter, r *http.Request) {
	op := awsapitest.Operation(r)
	f.mu.Lock()
	f.ops = append(f.ops, op)
	f.mu.Unlock()
	switch op {
	case "GetSecretValue":
		var body struct {
			SecretID string `json:"SecretId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Error(err)
		}
		if value, ok := f.values[body.SecretID]; ok {
			respond(f.t, w, http.StatusOK, map[string]string{"SecretString": value})
			return
		}
		respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": "ResourceNotFoundException", "message": "no such secret"})
	case "ListSecrets":
		list := make([]map[string]any, 0, len(f.listed))
		for _, arn := range f.listed {
			list = append(list, map[string]any{"ARN": arn, "Tags": []map[string]string{
				{"Key": "Role", "Value": "read-only"}, {"Key": "DBInstanceArn", "Value": secretsInstanceARN},
			}})
		}
		respond(f.t, w, http.StatusOK, map[string]any{"SecretList": list})
	default:
		respond(f.t, w, http.StatusBadRequest, map[string]string{"__type": "UnknownOperationException", "message": op})
	}
}

// respond writes the JSON reply of a fake AWS API.
func respond(t testing.TB, w http.ResponseWriter, status int, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Error(err)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if _, err := w.Write(raw); err != nil {
		t.Error(err)
	}
}

func (f *fakeSecretsManager) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(slices.DeleteFunc(slices.Clone(f.ops), func(o string) bool { return o != op }))
}
