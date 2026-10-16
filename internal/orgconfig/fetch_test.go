package orgconfig

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reply(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body)) //nolint:errcheck // a failed write fails the test through the client.
}

func tokenFn() func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return "s3cret-token", nil }
}

func TestFetchHTTPSSendsIfNoneMatchAndHandles304(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		reply(w, "{}")
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client()}
	src := Source{Kind: KindHTTPS, URL: srv.URL}

	got, err := f.Fetch(context.Background(), src, "")
	if err != nil || string(got.Body) != "{}" || got.ETag != `"v1"` {
		t.Fatalf("first fetch = %+v, %v", got, err)
	}
	got, err = f.Fetch(context.Background(), src, got.ETag)
	if err != nil || !got.NotModified || got.ETag != `"v1"` {
		t.Fatalf("second fetch = %+v, %v", got, err)
	}
}

func TestFetchGitHubUsesContentsAPI(t *testing.T) {
	var req *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = r
		reply(w, "{}")
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client(), GitHubAPI: srv.URL, Token: tokenFn()}
	src := Source{Kind: KindGitHub, Owner: "acme", Repo: "infra", Ref: "main", File: "airlock/airlock.json"}

	if _, err := f.Fetch(context.Background(), src, `"e"`); err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/repos/acme/infra/contents/airlock/airlock.json" || req.URL.Query().Get("ref") != "main" {
		t.Errorf("url = %s", req.URL)
	}
	if req.Header.Get("Accept") != "application/vnd.github.raw" ||
		req.Header.Get("Authorization") != "Bearer s3cret-token" ||
		req.Header.Get("If-None-Match") != `"e"` {
		t.Errorf("headers = %v", req.Header)
	}
}

func TestFetchGitHubAccessErrorHidesToken(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		f := &Fetcher{HTTP: srv.Client(), GitHubAPI: srv.URL, Token: tokenFn()}
		_, err := f.Fetch(context.Background(), Source{Kind: KindGitHub, Owner: "a", Repo: "b", Ref: "main", File: "c"}, "")
		srv.Close()
		if !errors.Is(err, ErrAccess) || strings.Contains(err.Error(), "s3cret-token") {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
	// A token that cannot be obtained is an access problem too.
	f := &Fetcher{Token: func(context.Context) (string, error) { return "", errors.New("no gh") }}
	_, err := f.Fetch(context.Background(), Source{Kind: KindGitHub, Owner: "a", Repo: "b", Ref: "main", File: "c"}, "")
	if !errors.Is(err, ErrAccess) {
		t.Errorf("token error = %v", err)
	}
}

func TestFetchRedirectDropsTokenOnOtherHost(t *testing.T) {
	var auth string
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		reply(w, "{}")
	}))
	defer other.Close()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer api.Close()
	f := &Fetcher{HTTP: api.Client(), GitHubAPI: api.URL, Token: tokenFn()}

	if _, err := f.Fetch(context.Background(), Source{Kind: KindGitHub, Owner: "a", Repo: "b", Ref: "main", File: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("token reached the other host: %q", auth)
	}
}

func TestFetchTooLarge(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, strings.Repeat("a", MaxSize+1))
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client()}
	if _, err := f.Fetch(context.Background(), Source{Kind: KindHTTPS, URL: srv.URL}, ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.json")
	f := &Fetcher{}
	_, err := f.Fetch(context.Background(), Source{Kind: KindFile, Path: path}, "")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file err = %v", err)
	}
	if werr := os.WriteFile(path, []byte("{}"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	got, err := f.Fetch(context.Background(), Source{Kind: KindFile, Path: path}, `"ignored"`)
	if err != nil || string(got.Body) != "{}" || got.ETag != "" || got.NotModified {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestFetchHTTPSNeverSendsToken(t *testing.T) {
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		reply(w, "{}")
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client(), Token: tokenFn()}
	if _, err := f.Fetch(context.Background(), Source{Kind: KindHTTPS, URL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestFetchRefusesRedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reply(w, "{}") }))
	defer plain.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client()}
	if _, err := f.Fetch(context.Background(), Source{Kind: KindHTTPS, URL: srv.URL}, ""); !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchServerErrorIsUnreachableWithoutSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	f := &Fetcher{HTTP: srv.Client(), GitHubAPI: srv.URL, Token: tokenFn()}
	_, err := f.Fetch(context.Background(), Source{Kind: KindGitHub, Owner: "a", Repo: "b", Ref: "main", File: "c"}, "")
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "502") ||
		strings.Contains(err.Error(), "s3cret-token") || strings.Contains(err.Error(), "ref=") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchCancelledContextKeepsCause(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &Fetcher{HTTP: srv.Client()}
	_, err := f.Fetch(ctx, Source{Kind: KindHTTPS, URL: srv.URL + "/?secret=1"}, "")
	if !errors.Is(err, ErrUnreachable) || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchFileTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", MaxSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Fetcher{}).Fetch(context.Background(), Source{Kind: KindFile, Path: path}, ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchGitHubWithoutTokenFuncIsAccessError(t *testing.T) {
	_, err := (&Fetcher{}).Fetch(context.Background(), Source{Kind: KindGitHub, Owner: "a", Repo: "b", Ref: "main", File: "c"}, "")
	if !errors.Is(err, ErrAccess) {
		t.Errorf("err = %v", err)
	}
}
