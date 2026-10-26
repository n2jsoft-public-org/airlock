package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/selfupdate/selfupdatetest"
)

func testRelease(t *testing.T) *selfupdatetest.Release {
	t.Helper()
	return new(selfupdatetest.NewRelease(t, "v1.4.0", "linux", "amd64", []byte("new airlock")))
}

func TestLatestRelease(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	release, err := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token).LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.ID != 42 || release.Tag != "v1.4.0" || len(release.Assets) != 2 {
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

func TestReact(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	client := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token)
	if err := client.React(context.Background(), Release{ID: 42}, InstallReaction); err != nil {
		t.Fatal(err)
	}
	if got := srv.Reactions(); len(got) != 1 || got[0] != "42:rocket" {
		t.Fatalf("reactions = %v", got)
	}
}

func TestReactWithARefusingToken(t *testing.T) {
	release := testRelease(t)
	release.RefuseReactions = true
	srv := selfupdatetest.NewServer(t, release)
	err := NewClient(srv.URL, selfupdatetest.Repository, selfupdatetest.Token).React(context.Background(), Release{ID: 42}, InstallReaction)
	if err == nil || err.Error() != "GitHub answered 403 Forbidden for /repos/acme/airlock/releases/42/reactions" {
		t.Fatalf("error = %v", err)
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

func TestDownloadFromAnotherHostSendsNoToken(t *testing.T) {
	var foreignAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")
		if _, err := w.Write([]byte("archive")); err != nil {
			t.Error(err)
		}
	}))
	defer foreign.Close()
	api := httptest.NewServer(http.NotFoundHandler())
	defer api.Close()

	got, err := NewClient(api.URL, "acme/airlock", "secret").Download(context.Background(), Asset{Name: "a.tar.gz", URL: foreign.URL + "/assets/1"})
	if err != nil || string(got) != "archive" {
		t.Fatalf("Download = %q, %v", got, err)
	}
	if foreignAuth != "" {
		t.Fatalf("a foreign host received an Authorization header: %q", foreignAuth)
	}
}

func TestLatestReleaseAnonymouslyFromAPublicRepository(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	srv.Public = true
	release, err := NewClient(srv.URL, selfupdatetest.Repository, "").LatestRelease(context.Background())
	if err != nil || release.Tag != "v1.4.0" {
		t.Fatalf("LatestRelease = %+v, %v", release, err)
	}
}

func TestLatestReleaseAnonymouslyFromAPrivateRepository(t *testing.T) {
	srv := selfupdatetest.NewServer(t, testRelease(t))
	_, err := NewClient(srv.URL, selfupdatetest.Repository, "").LatestRelease(context.Background())
	if !errors.Is(err, ErrNoToken) || err.Error() != "cannot read releases of acme/airlock: "+ErrNoToken.Error() {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
}

func TestAnonymousClientSendsNoAuthorization(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if _, err := w.Write([]byte(`{"tag_name":"v1.4.0"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "acme/airlock", "").LatestRelease(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 || auth[0] != "" {
		t.Fatalf("Authorization headers = %q, want none", auth)
	}
}
