package orgconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheIgnoredForOtherSource(t *testing.T) {
	c := Cache{Source: "https://a.example/x.json", Body: []byte("{}")}
	if _, ok := c.For("https://b.example/x.json"); ok {
		t.Error("cache applied to another source")
	}
	if _, ok := (Cache{Source: "s"}).For("s"); ok {
		t.Error("empty cache applied")
	}
	if _, ok := c.For(c.Source); !ok {
		t.Error("cache not applied to its own source")
	}
}

func TestCacheStale(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := Cache{FetchedAt: at}
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{
		{at.Add(59 * time.Minute), false},
		{at.Add(60 * time.Minute), true},
		{at.Add(-time.Minute), true},
	} {
		if got := c.Stale(tc.now); got != tc.want {
			t.Errorf("Stale(%v) = %v, want %v", tc.now.Sub(at), got, tc.want)
		}
	}
	if !(Cache{}).Stale(at) {
		t.Error("zero cache must be stale")
	}
}

func TestSaveLoadCacheRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "organization-cache.json")
	want := Cache{Source: "s", ETag: `"e"`, Body: []byte("{}"), Previous: []byte("[]")}
	if err := SaveCache(path, want); err != nil {
		t.Fatal(err)
	}
	got := LoadCache(path)
	if got.Source != "s" || got.ETag != `"e"` || string(got.Body) != "{}" || string(got.Previous) != "[]" {
		t.Errorf("round trip = %+v", got)
	}
	if info, err := os.Stat(path); err != nil {
		t.Error(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if c := LoadCache(filepath.Join(t.TempDir(), "missing")); c.Source != "" || c.Body != nil {
		t.Errorf("missing file = %+v", c)
	}
}
