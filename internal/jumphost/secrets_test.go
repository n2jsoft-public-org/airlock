package jumphost

import (
	"encoding/json"
	"testing"
)

const secretsInstanceARN = "arn:aws:rds:eu-west-3:111111111111:db:archive"

func TestRankDBSecretsPutsLinkedSecretsFirst(t *testing.T) {
	var entries []secretListEntry
	raw := `[
		{"ARN":"arn:z","Name":"zeta/other","Description":""},
		{"ARN":"arn:tag","Name":"team/app-ro","Tags":[{"Key":"DBInstanceArn","Value":"` + secretsInstanceARN + `"}]},
		{"ARN":"arn:master","Name":"rds!db-1234","Description":"master"},
		{"ARN":"arn:name","Name":"prod/Archive/app-user"},
		{"ARN":"arn:a","Name":"alpha/other"}
	]`
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatal(err)
	}

	got := rankDBSecrets(entries, "archive", secretsInstanceARN, "arn:master")
	want := []struct {
		name   string
		linked bool
	}{
		{"prod/Archive/app-user", true}, {"rds!db-1234", true}, {"team/app-ro", true},
		{"alpha/other", false}, {"zeta/other", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for idx, w := range want {
		if got[idx].Name != w.name || got[idx].Linked != w.linked {
			t.Fatalf("got[%d] = %+v, want %s linked=%v", idx, got[idx], w.name, w.linked)
		}
	}
	if got[1].Description != "master" || got[1].ARN != "arn:master" {
		t.Fatalf("summary lost its fields: %+v", got[1])
	}
}

func TestSecretDisplayName(t *testing.T) {
	cases := map[string]string{
		"arn:aws:secretsmanager:eu-west-3:111111111111:secret:prod/app-user-AbC123":    "prod/app-user",
		"arn:aws:secretsmanager:eu-west-3:111111111111:secret:rds!db-1234-5678-xYz9Qw": "rds!db-1234-5678",
		"prod/app-user": "prod/app-user",
		"":              "",
	}
	for id, want := range cases {
		if got := SecretDisplayName(id); got != want {
			t.Errorf("SecretDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}
