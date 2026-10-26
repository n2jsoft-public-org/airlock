package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/selfupdate"
)

var noticeTime = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func saveTestCache(t *testing.T, env *updateEnv, c selfupdate.Cache) {
	t.Helper()
	path, err := env.cacheFile()
	if err != nil {
		t.Fatal(err)
	}
	if err = selfupdate.SaveCache(path, c); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateNoticeFromAFreshCache(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	saveTestCache(t, env, selfupdate.Cache{CheckedAt: noticeTime, Current: testCurrent, Latest: testLatest, Status: selfupdate.StatusAvailable})

	latest, stale := updateNotice(env, noticeTime.Add(time.Hour))
	if latest != testLatest || stale {
		t.Fatalf("updateNotice = %q, %v, want %q from a fresh cache", latest, stale, testLatest)
	}
	latest, stale = updateNotice(env, noticeTime.Add(25*time.Hour))
	if latest != testLatest || !stale {
		t.Fatalf("updateNotice a day later = %q, %v, want the notice and a refresh", latest, stale)
	}
}

func TestUpdateNoticeWithoutCache(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	if latest, stale := updateNotice(env, noticeTime); latest != "" || !stale {
		t.Fatalf("updateNotice = %q, %v, want no notice and a refresh", latest, stale)
	}
}

func TestUpdateNoticeIsOff(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	saveTestCache(t, env, selfupdate.Cache{CheckedAt: noticeTime, Current: testCurrent, Latest: testLatest, Status: selfupdate.StatusAvailable})

	t.Setenv(noUpdateCheckEnv, "1")
	if latest, stale := updateNotice(env, noticeTime); latest != "" || stale {
		t.Fatalf("with %s: updateNotice = %q, %v", noUpdateCheckEnv, latest, stale)
	}
	t.Setenv(noUpdateCheckEnv, "")
	env.current = "dev"
	if latest, stale := updateNotice(env, noticeTime); latest != "" || stale {
		t.Fatalf("dev build: updateNotice = %q, %v", latest, stale)
	}
}

func TestRefreshUpdateCheckWritesTheCache(t *testing.T) {
	env, _ := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	if err := refreshUpdateCheck(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	latest, stale := updateNotice(env, time.Now())
	if latest != testLatest || stale {
		t.Fatalf("after a refresh: updateNotice = %q, %v", latest, stale)
	}
}

func TestRefreshUpdateCheckOfARepositoryWithoutRelease(t *testing.T) {
	srv := testUpdateServer(t)
	srv.Release = nil
	env, _ := testUpdateEnv(t, srv, testCurrent)
	if err := refreshUpdateCheck(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if latest, stale := updateNotice(env, time.Now()); latest != "" || stale {
		t.Fatalf("without release: updateNotice = %q, %v, want no notice and a fresh check", latest, stale)
	}
}

func TestRefreshUpdateCheckWritesNothingWhenGitHubIsUnreachable(t *testing.T) {
	srv := testUpdateServer(t)
	env, _ := testUpdateEnv(t, srv, testCurrent)
	srv.Close()

	if err := refreshUpdateCheck(context.Background(), env); err == nil {
		t.Fatal("a refresh without GitHub must fail")
	}
	path, err := env.cacheFile()
	if err != nil {
		t.Fatal(err)
	}
	if cache := selfupdate.LoadCache(path); cache != (selfupdate.Cache{}) {
		t.Fatalf("cache written after a failure: %+v", cache)
	}
}

func TestUpdateNoticeTexts(t *testing.T) {
	if got := wizardUpdateNotice(testLatest, "brew upgrade airlock"); got != "update available: "+testLatest+" · run brew upgrade airlock" {
		t.Errorf("wizard notice = %q", got)
	}
	if got := stderrUpdateNotice(testLatest, "airlock update"); got != "A new airlock is available ("+testLatest+"): run airlock update" {
		t.Errorf("stderr notice = %q", got)
	}
}

func TestUpdateCommandFollowsTheInstall(t *testing.T) {
	env, target := testUpdateEnv(t, testUpdateServer(t), testCurrent)
	if got := env.updateCommand(); got != "airlock update" {
		t.Fatalf("script install: updateCommand = %q", got)
	}
	markerPath, err := env.markerFile()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if got := env.updateCommand(); got != "" {
		t.Fatalf("unknown install: updateCommand = %q, want none", got)
	}
	t.Setenv("GOBIN", filepath.Dir(target))
	if got := env.updateCommand(); got != "go install github.com/n2jsoft-public-org/airlock/cmd/airlock@latest" {
		t.Fatalf("go install: updateCommand = %q", got)
	}
}
