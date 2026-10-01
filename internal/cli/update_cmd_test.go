package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
)

const (
	testCurrent = "20261001-b50fc20"
	testLatest  = "20261005-3f9a1c2"
)

// testUpdateServer serves the release testLatest built for this platform; compare is the status
// GitHub answers when comparing commits.
func testUpdateServer(t *testing.T, compare string) *selfupdatetest.Server {
	t.Helper()
	release := selfupdatetest.NewRelease(t, testLatest, runtime.GOOS, runtime.GOARCH, []byte("new airlock"))
	release.Compare = compare
	return selfupdatetest.NewServer(t, &release)
}

// testUpdateEnv points airlock update at srv and at a fake installed binary, which it returns.
func testUpdateEnv(t *testing.T, srv *selfupdatetest.Server, current string) (env updateEnv, target string) {
	t.Helper()
	dir := t.TempDir()
	target = filepath.Join(dir, selfupdate.BinaryName(runtime.GOOS))
	writeTestFile(t, target, "old airlock")
	return updateEnv{
		token:      func(context.Context) (string, error) { return selfupdatetest.Token, nil },
		repository: func() (string, error) { return selfupdatetest.Repository, nil },
		executable: func() (string, error) { return target, nil },
		cacheFile:  func() (string, error) { return filepath.Join(dir, "update-check.json"), nil },
		current:    current,
		baseURL:    srv.URL,
	}, target
}

func runUpdateCmd(t *testing.T, env updateEnv, args ...string) (string, error) {
	t.Helper()
	cmd := newUpdateCmdWith(env)
	var out, stderr strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func readTarget(t *testing.T, target string) string {
	t.Helper()
	// nolint:gosec // target is a file path created by testUpdateEnv in t.TempDir(), not user-controlled
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpdateInstallsANewerRelease(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t, selfupdate.CompareAhead), testCurrent)
	out, err := runUpdateCmd(t, env)
	if err != nil || out != "Updated airlock "+testCurrent+" → "+testLatest+"\n" {
		t.Fatalf("out = %q, %v", out, err)
	}
	if got := readTarget(t, target); got != "new airlock" {
		t.Fatalf("binary = %q", got)
	}
	cachePath, err := env.cacheFile()
	if err != nil {
		t.Fatal(err)
	}
	if cache := selfupdate.LoadCache(cachePath); cache.Current != testLatest || cache.Status != selfupdate.StatusUpToDate {
		t.Fatalf("cache after the update = %+v, want the new build up to date", cache)
	}
}

func TestUpdateCheckOnlyReports(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t, selfupdate.CompareAhead), testCurrent)
	out, err := runUpdateCmd(t, env, "--check")
	want := "airlock " + testLatest + " is available (this build is " + testCurrent + "): run airlock update\n"
	if err != nil || out != want {
		t.Fatalf("out = %q, %v, want %q", out, err, want)
	}
	if got := readTarget(t, target); got != "old airlock" {
		t.Fatalf("--check changed the binary: %q", got)
	}
}

func TestUpdateStatusMessages(t *testing.T) {
	tests := []struct {
		name    string
		current string
		compare string
		want    string
	}{
		{"up to date", testLatest, "", "airlock " + testLatest + " is up to date\n"},
		{"newer", "20261009-0aa11bb", selfupdate.CompareBehind,
			"this build (20261009-0aa11bb) is newer than the latest release (" + testLatest + "): nothing to do (--force installs " + testLatest + ")\n"},
		{"diverged", "20261005-0aa11bb", selfupdate.CompareDiverged,
			"this build (20261005-0aa11bb) comes from a commit not on main: nothing to do (--force installs " + testLatest + ")\n"},
		{"dev", "dev", "",
			"this airlock was built without a version (go run): install it with make install (--force installs " + testLatest + ")\n"},
	}
	for _, tt := range tests {
		env, target := testUpdateEnv(t, testUpdateServer(t, tt.compare), tt.current)
		out, err := runUpdateCmd(t, env)
		if err != nil || out != tt.want {
			t.Errorf("%s: out = %q, %v, want %q", tt.name, out, err, tt.want)
		}
		if got := readTarget(t, target); got != "old airlock" {
			t.Errorf("%s: the binary changed: %q", tt.name, got)
		}
	}
}

func TestUpdateForceInstallsOverANewerBuild(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t, selfupdate.CompareBehind), "20261009-0aa11bb")
	out, err := runUpdateCmd(t, env, "--force")
	if err != nil || out != "Updated airlock 20261009-0aa11bb → "+testLatest+"\n" {
		t.Fatalf("out = %q, %v", out, err)
	}
	if got := readTarget(t, target); got != "new airlock" {
		t.Fatalf("binary = %q", got)
	}
}

func TestUpdateWithoutToken(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t, selfupdate.CompareAhead), testCurrent)
	env.token = func(context.Context) (string, error) { return "", selfupdate.ErrNoToken }
	if _, err := runUpdateCmd(t, env); !errors.Is(err, selfupdate.ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
}

func TestUpdateIsRegistered(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"update"})
	if err != nil || cmd.Name() != "update" {
		t.Fatalf("Find(update) = %v, %v", cmd, err)
	}
}
