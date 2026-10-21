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
	testCurrent = "v1.3.2"
	testLatest  = "v1.4.0"
)

// testUpdateServer serves the release testLatest built for this platform, after v1.3.5 and testCurrent.
func testUpdateServer(t *testing.T) *selfupdatetest.Server {
	t.Helper()
	release := selfupdatetest.NewRelease(t, testLatest, runtime.GOOS, runtime.GOARCH, []byte("new airlock"))
	release.Body = "## [1.4.0](https://github.com/acme/airlock/compare/v1.3.5...v1.4.0) (2026-10-21)\n\n### Features\n\n" +
		"* **doctor:** stream the checks live ([#91](https://github.com/acme/airlock/issues/91)) ([fa16896](https://github.com/acme/airlock/commit/fa16896))\n"
	srv := selfupdatetest.NewServer(t, &release)
	srv.History = []selfupdatetest.Release{
		{Tag: "v1.3.5", Body: "### Bug Fixes\n\n* keep the tunnel open ([#80](https://github.com/acme/airlock/issues/80))\n"},
		{Tag: "v1.3.4", Body: "## [1.3.4](https://github.com/acme/airlock/compare/v1.3.3...v1.3.4) (2026-10-01)\n"},
		{Tag: testCurrent, Body: "### Features\n\n* already installed\n"},
	}
	return srv
}

// testChanges is what updating testCurrent to testLatest brings, as airlock update prints it.
const testChanges = "\nWhat's new\n\nv1.4.0\n  Features\n    • doctor: stream the checks live (#91)\n\nv1.3.5\n  Bug Fixes\n    • keep the tunnel open (#80)\n\n"

// installOutput is what airlock update prints when it installs testLatest over current at target.
func installOutput(current, target, changes string) string {
	return "✓ latest release: " + testLatest + " (this build is " + current + ")\n" +
		"✓ Installed airlock " + testLatest + " in " + target + "\n" + changes +
		"✓ Updated airlock " + current + " → " + testLatest + "\n"
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
	data, err := os.ReadFile(filepath.Clean(target))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpdateInstallsANewerRelease(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	out, err := runUpdateCmd(t, env)
	if want := installOutput(testCurrent, target, testChanges); err != nil || out != want {
		t.Fatalf("out = %q, %v, want %q", out, err, want)
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

func TestUpdateReactsToTheInstalledRelease(t *testing.T) {
	t.Setenv(noTrackingEnv, "")
	srv := testUpdateServer(t)
	env, _ := testUpdateEnv(t, srv, testCurrent)
	if _, err := runUpdateCmd(t, env); err != nil {
		t.Fatal(err)
	}
	if got := srv.Reactions(); len(got) != 1 || got[0] != "42:"+selfupdate.InstallReaction {
		t.Fatalf("reactions = %v, want one %s on the installed release", got, selfupdate.InstallReaction)
	}
}

func TestUpdateWithoutTrackingDoesNotReact(t *testing.T) {
	t.Setenv(noTrackingEnv, "1")
	srv := testUpdateServer(t)
	env, _ := testUpdateEnv(t, srv, testCurrent)
	if _, err := runUpdateCmd(t, env); err != nil {
		t.Fatal(err)
	}
	if got := srv.Reactions(); len(got) != 0 {
		t.Fatalf("reactions = %v with %s set", got, noTrackingEnv)
	}
}

func TestUpdateSucceedsWhenTheReactionIsRefused(t *testing.T) {
	t.Setenv(noTrackingEnv, "")
	srv := testUpdateServer(t)
	srv.Release.RefuseReactions = true
	env, target := testUpdateEnv(t, srv, testCurrent)
	out, err := runUpdateCmd(t, env)
	if want := installOutput(testCurrent, target, testChanges); err != nil || out != want {
		t.Fatalf("out = %q, %v, want %q", out, err, want)
	}
	if got := readTarget(t, target); got != "new airlock" {
		t.Fatalf("binary = %q", got)
	}
}

func TestUpdateCheckDoesNotReact(t *testing.T) {
	t.Setenv(noTrackingEnv, "")
	srv := testUpdateServer(t)
	env, _ := testUpdateEnv(t, srv, testCurrent)
	if _, err := runUpdateCmd(t, env, "--check"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Reactions(); len(got) != 0 {
		t.Fatalf("reactions = %v after --check", got)
	}
}

func TestUpdateCheckOnlyReports(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	out, err := runUpdateCmd(t, env, "--check")
	want := "✓ airlock " + testLatest + " is available (this build is " + testCurrent + "): run airlock update\n" + testChanges
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
		want    string
	}{
		{"up to date", testLatest, "✓ airlock " + testLatest + " is up to date\n"},
		{"newer", "v1.4.1-0.20261009120000-0aa11bbccdde",
			"✓ this build (v1.4.1-0.20261009120000-0aa11bbccdde) is newer than the latest release (" + testLatest + "): nothing to do (--force installs " + testLatest + ")\n"},
		{"dev", "dev",
			"✓ this airlock was built without a version (go run): install it with make install (--force installs " + testLatest + ")\n"},
	}
	for _, tt := range tests {
		env, target := testUpdateEnv(t, testUpdateServer(t), tt.current)
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
	env, target := testUpdateEnv(t, testUpdateServer(t), "v1.4.1-0.20261009120000-0aa11bbccdde")
	out, err := runUpdateCmd(t, env, "--force")
	if want := installOutput("v1.4.1-0.20261009120000-0aa11bbccdde", target, ""); err != nil || out != want {
		t.Fatalf("out = %q, %v, want %q", out, err, want)
	}
	if got := readTarget(t, target); got != "new airlock" {
		t.Fatalf("binary = %q", got)
	}
}

func TestUpdateWithoutToken(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	env.token = func(context.Context) (string, error) { return "", selfupdate.ErrNoToken }
	if _, err := runUpdateCmd(t, env); !errors.Is(err, selfupdate.ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
}

func TestUpdateAsksToRestartARunningDaemon(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	env.daemonRunning = func(context.Context) bool { return true }
	out, err := runUpdateCmd(t, env)
	if err != nil || !strings.HasSuffix(out, "restart the daemon to use the new version: airlock daemon restart\n") {
		t.Fatalf("out = %q, err = %v; want the restart notice", out, err)
	}

	env, _ = testUpdateEnv(t, testUpdateServer(t), testCurrent)
	env.daemonRunning = func(context.Context) bool { return false }
	if out, err := runUpdateCmd(t, env); err != nil || strings.Contains(out, "daemon") {
		t.Fatalf("out = %q, err = %v; want no notice without daemon", out, err)
	}
}

func TestUpdateIsRegistered(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"update"})
	if err != nil || cmd.Name() != "update" {
		t.Fatalf("Find(update) = %v, %v", cmd, err)
	}
}
