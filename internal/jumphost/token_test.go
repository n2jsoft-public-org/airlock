package jumphost

import "testing"

func TestTokenCommandSignsTheIAMUserIn(t *testing.T) {
	cfg := awsConfig{Profile: "dev", Region: "eu-west-3", Args: []string{"--profile", "dev", "--region", "eu-west-3"}}
	target := &ideTarget{RDSHost: "archive.abc.eu-west-3.rds.amazonaws.com", RDSPort: 5432, DBUser: "app_ro", IAMAuth: true}

	want := "aws rds generate-db-auth-token --hostname archive.abc.eu-west-3.rds.amazonaws.com --port 5432 --username app_ro --profile dev --region eu-west-3"
	if got := tokenCommand(target, cfg); got != want {
		t.Fatalf("tokenCommand = %q, want %q", got, want)
	}

	target.IAMAuth = false
	if got := tokenCommand(target, cfg); got != "" {
		t.Fatalf("tokenCommand = %q, want none without AWS IAM sign-in", got)
	}
}
