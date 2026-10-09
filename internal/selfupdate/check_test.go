package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
	"github.com/n2jsoft/airlock/internal/version"
)

const testLatest = "v1.4.0"

func testUpdater(t *testing.T, current string) (Updater, *selfupdatetest.Server) {
	t.Helper()
	srv := selfupdatetest.NewServer(t, new(selfupdatetest.NewRelease(t, testLatest, "linux", "amd64", []byte("new airlock"))))
	return Updater{
		Client:  NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token),
		Current: current,
		GOOS:    "linux",
		GOARCH:  "amd64",
	}, srv
}

func TestCheckFindsANewerRelease(t *testing.T) {
	u, _ := testUpdater(t, "v1.3.2")
	result, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAvailable || result.Latest != testLatest || result.Current != "v1.3.2" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCheckOfTheLatestReleaseAndOfADevBuild(t *testing.T) {
	for current, want := range map[string]Status{testLatest: StatusUpToDate, version.Unknown: StatusDev} {
		u, _ := testUpdater(t, current)
		result, err := u.Check(context.Background())
		if err != nil || result.Status != want {
			t.Fatalf("%s: Check = %+v, %v, want %s", current, result, err, want)
		}
	}
}

func TestInstallReplacesTheTarget(t *testing.T) {
	u, _ := testUpdater(t, "v1.3.2")
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
	u, _ := testUpdater(t, "v1.3.2")
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
	u, srv := testUpdater(t, "v1.3.2")
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
	u, srv := testUpdater(t, "v1.3.2")
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
