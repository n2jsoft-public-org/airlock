package selfupdate

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
)

func testRelease(t *testing.T) *selfupdatetest.Release {
	t.Helper()
	release := selfupdatetest.NewRelease(t, "20261005-3f9a1c2", "linux", "amd64", []byte("new airlock"))
	return &release
}

func TestLatestRelease(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	release, err := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token).LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.Tag != "20261005-3f9a1c2" || len(release.Assets) != 2 {
		t.Fatalf("release = %+v", release)
	}
}

func TestLatestReleaseWithABadToken(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	_, err := NewClient(srv.URL, selfupdatetest.Repository, "wrong").LatestRelease(context.Background())
	if err == nil || err.Error() != "cannot read releases of acme/airlock: check the token can access it" {
		t.Fatalf("error = %v", err)
	}
}

func TestLatestReleaseOfAnUnknownRepository(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	_, err := NewClient(srv.URL, "acme/other", selfupdatetest.Token).LatestRelease(context.Background())
	if err == nil || err.Error() != "cannot read releases of acme/other: check the token can access it" {
		t.Fatalf("error = %v", err)
	}
}

func TestLatestReleaseWithoutAnyRelease(t *testing.T) {
	srv := selfupdatetest.NewServer(t, nil)
	_, err := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token).LatestRelease(context.Background())
	if err == nil || err.Error() != "acme/airlock has no release yet" {
		t.Fatalf("error = %v", err)
	}
}

func TestLatestReleaseOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "acme/airlock", "t").LatestRelease(context.Background())
	if err == nil || err.Error() != "GitHub answered 500 Internal Server Error for /repos/acme/airlock/releases/latest" {
		t.Fatalf("error = %v", err)
	}
}

func TestCompare(t *testing.T) {
	release := testRelease(t)
	release.Compare = CompareAhead
	srv := selfupdatetest.NewServer(t, release)
	client := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token)

	status, err := client.Compare(context.Background(), "b50fc20", "20261005-3f9a1c2")
	if err != nil || status != CompareAhead {
		t.Fatalf("Compare = %q, %v", status, err)
	}
	if paths := srv.Paths(); paths[len(paths)-1] != "/repos/acme/airlock/compare/b50fc20...20261005-3f9a1c2" {
		t.Fatalf("requested %v", paths)
	}

	release.Compare = "" // GitHub answers 404 for a commit it does not know
	status, err = client.Compare(context.Background(), "0aa11bb", "20261005-3f9a1c2")
	if err != nil || status != CompareUnknown {
		t.Fatalf("Compare of an unknown commit = %q, %v", status, err)
	}
}

func TestDownload(t *testing.T) {
	release := testRelease(t)
	srv := selfupdatetest.NewServer(t, release)
	client := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token)
	got, err := client.Download(context.Background(), Asset{Name: ChecksumsName, URL: srv.URL + "/assets/" + ChecksumsName})
	if err != nil || !bytes.Equal(got, release.Assets[ChecksumsName]) {
		t.Fatalf("Download = %q, %v", got, err)
	}
	_, err = client.Download(context.Background(), Asset{Name: "missing.zip", URL: srv.URL + "/assets/missing.zip"})
	if err == nil || err.Error() != "cannot download missing.zip: not found" {
		t.Fatalf("Download of a missing asset error = %v", err)
	}
}

func TestDownloadDoesNotSendTheTokenToTheRedirectHost(t *testing.T) {
	var storageAuth string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageAuth = r.Header.Get("Authorization")
		if _, err := w.Write([]byte("archive")); err != nil {
			t.Error(err)
		}
	}))
	defer storage.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, storage.URL+"/signed", http.StatusFound)
	}))
	defer api.Close()

	got, err := NewClient(api.URL, "acme/airlock", "secret").Download(context.Background(), Asset{Name: "a.tar.gz", URL: api.URL + "/assets/1"})
	if err != nil || string(got) != "archive" {
		t.Fatalf("Download = %q, %v", got, err)
	}
	if strings.Contains(storageAuth, "secret") {
		t.Fatalf("the storage host received the token: %q", storageAuth)
	}
}
