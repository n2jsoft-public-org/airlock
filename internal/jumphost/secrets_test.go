package jumphost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func secretAWS(value string, err error) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		if strings.Join(args[:2], " ") != "secretsmanager get-secret-value" {
			return "", errors.New("unexpected call " + strings.Join(args, " "))
		}
		return value, err
	}
}

func TestResolveSecretSignInPrefersSecretUser(t *testing.T) {
	user, err := resolveSecretSignIn(context.Background(), secretAWS(`{"username":"app_user","password":"s3cret"}`, nil), "arn:secret", "typed")
	if err != nil || user != "app_user" {
		t.Fatalf("user = %q, err = %v", user, err)
	}
}

func TestResolveSecretSignInPlainTextNeedsUser(t *testing.T) {
	if user, err := resolveSecretSignIn(context.Background(), secretAWS("s3cret", nil), "arn:secret", " typed "); err != nil || user != "typed" {
		t.Fatalf("user = %q, err = %v", user, err)
	}
	_, err := resolveSecretSignIn(context.Background(), secretAWS("s3cret", nil), "arn:secret", "")
	if err == nil || !strings.Contains(err.Error(), "has no username: fill the User field") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveSecretSignInExplainsUnreadableSecret(t *testing.T) {
	_, err := resolveSecretSignIn(context.Background(), secretAWS("", errors.New("ResourceNotFoundException")), "arn:secret", "typed")
	for _, want := range []string{"arn:secret", "secretsmanager:GetSecretValue", "kms:Decrypt", "ResourceNotFoundException"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
}
