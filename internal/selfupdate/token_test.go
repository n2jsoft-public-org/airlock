package selfupdate

import (
	"context"
	"errors"
	"testing"
)

func fakeEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func fakeGh(out string, err error, called *bool) commandOutput {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*called = true
		if name != "gh" || len(args) != 4 || args[0] != "auth" || args[1] != "token" || args[2] != "--hostname" || args[3] != "github.com" {
			return nil, errors.New("unexpected command")
		}
		return []byte(out), err
	}
}

func TestTokenPrefersGHToken(t *testing.T) {
	called := false
	got, err := token(context.Background(), fakeEnv(map[string]string{"GH_TOKEN": "gh-env", "GITHUB_TOKEN": "github-env"}), fakeGh("from-gh\n", nil, &called))
	if err != nil || got != "gh-env" || called {
		t.Fatalf("token = %q, %v (gh called: %v), want GH_TOKEN without calling gh", got, err, called)
	}
}

func TestTokenFallsBackToGitHubToken(t *testing.T) {
	called := false
	got, err := token(context.Background(), fakeEnv(map[string]string{"GITHUB_TOKEN": " github-env "}), fakeGh("from-gh\n", nil, &called))
	if err != nil || got != "github-env" || called {
		t.Fatalf("token = %q, %v (gh called: %v), want GITHUB_TOKEN", got, err, called)
	}
}

func TestTokenFallsBackToGh(t *testing.T) {
	got, err := token(context.Background(), fakeEnv(nil), fakeGh("from-gh\n", nil, new(false)))
	if err != nil || got != "from-gh" {
		t.Fatalf("token = %q, %v, want the gh token", got, err)
	}
}

func TestTokenWithoutAnySource(t *testing.T) {
	called := false
	for _, gh := range []commandOutput{fakeGh("", errors.New("exec: \"gh\": executable file not found"), &called), fakeGh("  \n", nil, &called)} {
		if _, err := token(context.Background(), fakeEnv(nil), gh); !errors.Is(err, ErrNoToken) {
			t.Fatalf("token error = %v, want ErrNoToken", err)
		}
	}
	if ErrNoToken.Error() != "airlock update needs GitHub access: run gh auth login or set GH_TOKEN" {
		t.Fatalf("ErrNoToken = %q", ErrNoToken)
	}
}
