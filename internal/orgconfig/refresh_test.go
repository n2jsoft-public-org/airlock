package orgconfig

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type remote struct {
	body atomic.Value // string
	etag atomic.Value // string
	f    *Fetcher
	url  string
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	r := &remote{}
	r.body.Store(`{"a":1}`)
	r.etag.Store(`"v1"`)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		etag, _ := r.etag.Load().(string) //nolint:errcheck // always a string
		if etag != "" && req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		body, _ := r.body.Load().(string) //nolint:errcheck // always a string
		reply(w, body)
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	r.f = &Fetcher{HTTP: srv.Client()}
	return r
}

func (r *remote) src() Source { return Source{Raw: r.url, Kind: KindHTTPS, URL: r.url} }

func always() func([]byte) error { return func([]byte) error { return nil } }

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestRefreshStoresBodyAndETag(t *testing.T) {
	r := newRemote(t)
	path := filepath.Join(t.TempDir(), "c.json")
	changed, err := Refresh(context.Background(), r.f, r.src(), path, t0, always())
	if err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	c := LoadCache(path)
	if c.Source != r.url || c.ETag != `"v1"` || string(c.Body) != `{"a":1}` || !c.FetchedAt.Equal(t0) || c.Previous != nil {
		t.Errorf("cache = %+v", c)
	}
}

func TestRefreshKeepsCacheOnInvalidBody(t *testing.T) {
	r := newRemote(t)
	path := filepath.Join(t.TempDir(), "c.json")
	if _, err := Refresh(context.Background(), r.f, r.src(), path, t0, always()); err != nil {
		t.Fatal(err)
	}
	r.body.Store("garbage")
	r.etag.Store(`"v2"`)
	bad := errors.New("malformed")
	changed, err := Refresh(context.Background(), r.f, r.src(), path, t0.Add(2*time.Hour), func([]byte) error { return bad })
	if !errors.Is(err, bad) || changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	c := LoadCache(path)
	if string(c.Body) != `{"a":1}` || c.ETag != `"v1"` || !c.FetchedAt.Equal(t0) {
		t.Errorf("cache changed: %+v", c)
	}
	if c.Rejected != "malformed" {
		t.Errorf("Rejected = %q, want the rejection recorded", c.Rejected)
	}
}

func TestRefreshClearsRejectedOnNextSuccess(t *testing.T) {
	for name, fix := range map[string]func(r *remote){
		"not modified": func(r *remote) { r.etag.Store(`"v1"`) }, // the push is reverted
		"valid body":   func(r *remote) { r.body.Store(`{"a":2}`); r.etag.Store(`"v3"`) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRemote(t)
			path := filepath.Join(t.TempDir(), "c.json")
			ctx := context.Background()
			if _, err := Refresh(ctx, r.f, r.src(), path, t0, always()); err != nil {
				t.Fatal(err)
			}
			r.body.Store("garbage")
			r.etag.Store(`"v2"`)
			if _, err := Refresh(ctx, r.f, r.src(), path, t0.Add(time.Hour), func(b []byte) error {
				if string(b) == "garbage" {
					return errors.New("malformed")
				}
				return nil
			}); err == nil {
				t.Fatal("the invalid body must be rejected")
			}
			fix(r)
			if _, err := Refresh(ctx, r.f, r.src(), path, t0.Add(2*time.Hour), always()); err != nil {
				t.Fatal(err)
			}
			if c := LoadCache(path); c.Rejected != "" || !c.FetchedAt.Equal(t0.Add(2*time.Hour)) {
				t.Fatalf("cache = %+v, want Rejected cleared", c)
			}
		})
	}
}

func TestRefreshNotModifiedBumpsFetchedAt(t *testing.T) {
	r := newRemote(t)
	path := filepath.Join(t.TempDir(), "c.json")
	if _, err := Refresh(context.Background(), r.f, r.src(), path, t0, always()); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(time.Hour)
	changed, err := Refresh(context.Background(), r.f, r.src(), path, later, always())
	if err != nil || changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	c := LoadCache(path)
	if !c.FetchedAt.Equal(later) || string(c.Body) != `{"a":1}` {
		t.Errorf("cache = %+v", c)
	}
}

func TestRefreshNotModifiedWithoutCacheFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	src := Source{Raw: srv.URL, Kind: KindHTTPS, URL: srv.URL}
	path := filepath.Join(t.TempDir(), "c.json")
	_, err := Refresh(context.Background(), &Fetcher{HTTP: srv.Client()}, src, path, t0, always())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v", err)
	}
}

func TestRefreshSetsPreviousOnChange(t *testing.T) {
	r := newRemote(t)
	path := filepath.Join(t.TempDir(), "c.json")
	ctx := context.Background()
	if _, err := Refresh(ctx, r.f, r.src(), path, t0, always()); err != nil {
		t.Fatal(err)
	}
	if LoadCache(path).Previous != nil {
		t.Fatal("first fetch must not set Previous")
	}
	r.body.Store(`{"a":2}`)
	r.etag.Store(`"v2"`)
	changed, err := Refresh(ctx, r.f, r.src(), path, t0.Add(time.Hour), always())
	if err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	if c := LoadCache(path); string(c.Previous) != `{"a":1}` || string(c.Body) != `{"a":2}` {
		t.Errorf("cache = %+v", c)
	}
	// A third version while the diff was not shown keeps the oldest unseen body.
	r.body.Store(`{"a":3}`)
	r.etag.Store(`"v3"`)
	if _, err = Refresh(ctx, r.f, r.src(), path, t0.Add(2*time.Hour), always()); err != nil {
		t.Fatal(err)
	}
	if c := LoadCache(path); string(c.Previous) != `{"a":1}` || string(c.Body) != `{"a":3}` {
		t.Errorf("cache = %+v", c)
	}
	// Same body, new ETag: unchanged.
	r.etag.Store(`"v4"`)
	changed, err = Refresh(ctx, r.f, r.src(), path, t0.Add(3*time.Hour), always())
	if err != nil || changed {
		t.Fatalf("same body = %v, %v", changed, err)
	}
	if c := LoadCache(path); c.ETag != `"v4"` || string(c.Previous) != `{"a":1}` {
		t.Errorf("cache = %+v", c)
	}
}

func TestRefreshReplacesCacheOfOtherSource(t *testing.T) {
	r := newRemote(t)
	path := filepath.Join(t.TempDir(), "c.json")
	if err := SaveCache(path, Cache{Source: "https://old.example/x", ETag: `"v1"`, Body: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	// The other source's ETag must not be sent: the server would answer 304.
	changed, err := Refresh(context.Background(), r.f, r.src(), path, t0, always())
	if err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	if c := LoadCache(path); c.Source != r.url || string(c.Body) != `{"a":1}` || c.Previous != nil {
		t.Errorf("cache = %+v", c)
	}
}

func TestRefreshRefusesFileSource(t *testing.T) {
	_, err := Refresh(context.Background(), &Fetcher{}, Source{Raw: "/x", Kind: KindFile, Path: "/x"}, filepath.Join(t.TempDir(), "c.json"), t0, always())
	if err == nil {
		t.Error("file source must not be cached")
	}
}

// TestRefreshKeepsPreviousClearedDuringFetch: the CLI clears Previous once it showed the diff,
// possibly while a background refresh waits for the network.
func TestRefreshKeepsPreviousClearedDuringFetch(t *testing.T) {
	for name, tt := range map[string]struct {
		body         string
		wantPrevious string
		status       int
	}{
		"not modified": {"", "", http.StatusNotModified},
		"unchanged":    {`{"a":2}`, "", http.StatusOK},
		"changed":      {`{"a":3}`, `{"a":2}`, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.json")
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				c := LoadCache(path)
				c.Previous = nil
				if err := SaveCache(path, c); err != nil {
					t.Error(err)
				}
				if tt.status == http.StatusNotModified {
					w.WriteHeader(tt.status)
					return
				}
				reply(w, tt.body)
			}))
			t.Cleanup(srv.Close)
			src := Source{Raw: srv.URL, Kind: KindHTTPS, URL: srv.URL}
			if err := SaveCache(path, Cache{Source: src.Raw, FetchedAt: t0, ETag: `"v2"`, Body: []byte(`{"a":2}`), Previous: []byte(`{"a":1}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := Refresh(context.Background(), &Fetcher{HTTP: srv.Client()}, src, path, t0.Add(2*time.Hour), always()); err != nil {
				t.Fatal(err)
			}
			if c := LoadCache(path); string(c.Previous) != tt.wantPrevious || !c.FetchedAt.Equal(t0.Add(2*time.Hour)) {
				t.Fatalf("cache = %+v, want Previous %q", c, tt.wantPrevious)
			}
		})
	}
}
