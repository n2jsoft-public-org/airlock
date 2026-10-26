package selfupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoToken is returned when no GitHub token is found. airlock then reads releases anonymously,
// which a private repository refuses with this error.
var ErrNoToken = errors.New("airlock update needs GitHub access: run gh auth login or set GH_TOKEN")

// commandOutput runs a command and returns its standard output.
type commandOutput func(ctx context.Context, name string, args ...string) ([]byte, error)

// Token returns the GitHub token to read airlock's releases with, when there is one: GH_TOKEN, then
// GITHUB_TOKEN, then the token of the GitHub CLI.
func Token(ctx context.Context) (string, error) {
	return token(ctx, os.Getenv, runCommand)
}

func token(ctx context.Context, getenv func(string) string, run commandOutput) (string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value, nil
		}
	}
	out, err := run(ctx, "gh", "auth", "token", "--hostname", "github.com")
	if err != nil {
		return "", ErrNoToken
	}
	if value := strings.TrimSpace(string(out)); value != "" {
		return value, nil
	}
	return "", ErrNoToken
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- name and args are constants of this package.
}
