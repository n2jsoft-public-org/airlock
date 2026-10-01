package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
	"github.com/n2jsoft/airlock/internal/version"
)

const testLatest = "20261005-3f9a1c2"

func testUpdater(t *testing.T, current, compare string) (Updater, *selfupdatetest.Server) { //nolint:unparam // compare parameter needed for test flexibility
	t.Helper()
	release := selfupdatetest.NewRelease(t, testLatest, "linux", "amd64", []byte("new airlock"))
	release.Compare = compare
	srv := selfupdatetest.NewServer(t, &release)
	return Updater{
		Client:  NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token),
		Current: current,
		GOOS:    "linux",
		GOARCH:  "amd64",
	}, srv
}

func hasCompare(paths []string) bool {
	return slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "/compare/") })
}

func TestCheckFindsANewerRelease(t *testing.T) {
	u, srv := testUpdater(t, "20261001-b50fc20", CompareAhead)
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAvailable || result.Latest != testLatest || result.Current != "20261001-b50fc20" {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Contains(srv.Paths(), "/repos/acme/airlock/compare/b50fc20..."+testLatest) {
		t.Fatalf("requested %v", srv.Paths())
	}
}

func TestCheckSkipsTheComparisonWhenItIsNotNeeded(t *testing.T) {
	for current, want := range map[string]Status{testLatest: StatusUpToDate, version.Unknown: StatusDev} {
		u, srv := testUpdater(t, current, CompareAhead)
		result, err := u.Check(context.Background())
		if err != nil || result.Status != want {
			t.Fatalf("%s: Check = %+v, %v, want %s", current, result, err, want)
		}
		if hasCompare(srv.Paths()) {
			t.Fatalf("%s: compared commits without need: %v", current, srv.Paths())
		}
	}
}

func TestCheckOfAnUnreadableVersionIsDiverged(t *testing.T) {
	u, srv := testUpdater(t, "custom-build", CompareAhead)
	result, err := u.Check(context.Background())
	if err != nil || result.Status != StatusDiverged {
		t.Fatalf("Check = %+v, %v, want diverged", result, err)
	}
	if hasCompare(srv.Paths()) {
		t.Fatalf("compared a version without a commit: %v", srv.Paths())
	}
}

func TestInstallReplacesTheTarget(t *testing.T) {
	u, _ := testUpdater(t, "20261001-b50fc20", CompareAhead)
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "airlock")
	writeBinary(t, target, "old airlock", 0o755)

	if err = u.Install(context.Background(), &result, target); err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(target) //nolint:gosec // target is from t.TempDir()
	if readErr != nil || string(got) != "new airlock" {
		t.Fatalf("target = %q, %v", got, readErr)
	}
}

func TestInstallWithoutABuildForThePlatform(t *testing.T) {
	u, _ := testUpdater(t, "20261001-b50fc20", CompareAhead)
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u.GOOS, u.GOARCH = "freebsd", "amd64"
	err = u.Install(context.Background(), &result, filepath.Join(t.TempDir(), "airlock"))
	if err == nil || err.Error() != "release "+testLatest+" has no build for freebsd/amd64" {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallRefusesAnArchiveThatDoesNotMatchItsChecksum(t *testing.T) {
	u, srv := testUpdater(t, "20261001-b50fc20", CompareAhead)
	name := AssetName(testLatest, "linux", "amd64")
	srv.Release.Assets[name] = selfupdatetest.Archive(t, "linux", []byte("tampered"))
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "airlock")
	writeBinary(t, target, "old airlock", 0o755)

	err = u.Install(context.Background(), &result, target)
	if err == nil || err.Error() != "the download of "+name+" does not match checksums.txt: nothing was changed" {
		t.Fatalf("error = %v", err)
	}
	got, readErr := os.ReadFile(target) //nolint:gosec // target is from t.TempDir()
	if readErr != nil || string(got) != "old airlock" {
		t.Fatalf("target changed: %q, %v", got, readErr)
	}
}

func TestInstallWithoutChecksums(t *testing.T) {
	u, srv := testUpdater(t, "20261001-b50fc20", CompareAhead)
	delete(srv.Release.Assets, ChecksumsName)
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = u.Install(context.Background(), &result, filepath.Join(t.TempDir(), "airlock"))
	if err == nil || err.Error() != "release "+testLatest+" has no checksums.txt: nothing was changed" {
		t.Fatalf("error = %v", err)
	}
}
