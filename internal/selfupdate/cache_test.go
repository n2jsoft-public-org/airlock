package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var checkTime = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "update-check.json")
	r := Result{Current: "20261001-b50fc20", Latest: testLatest, Status: StatusAvailable}
	if err := SaveCache(path, r.CacheAt(checkTime)); err != nil {
		t.Fatal(err)
	}
	got := LoadCache(path)
	if !got.CheckedAt.Equal(checkTime) || got.Current != r.Current || got.Latest != testLatest || got.Status != StatusAvailable {
		t.Fatalf("LoadCache = %+v", got)
	}
}

func TestCacheAvailable(t *testing.T) {
	c := Cache{CheckedAt: checkTime, Current: "20261001-b50fc20", Latest: testLatest, Status: StatusAvailable}
	if got := c.Available("20261001-b50fc20"); got != testLatest {
		t.Fatalf("Available = %q, want %q", got, testLatest)
	}
	if got := c.Available(testLatest); got != "" {
		t.Fatalf("a cache of another build must not show a notice, got %q", got)
	}
	c.Status = StatusNewer
	if got := c.Available("20261001-b50fc20"); got != "" {
		t.Fatalf("only an available release shows a notice, got %q", got)
	}
}

func TestCacheStale(t *testing.T) {
	c := Cache{CheckedAt: checkTime, Current: "20261001-b50fc20"}
	for label, tt := range map[string]struct {
		now     time.Time
		current string
		want    bool
	}{
		"fresh":           {checkTime.Add(23 * time.Hour), "20261001-b50fc20", false},
		"a day old":       {checkTime.Add(24 * time.Hour), "20261001-b50fc20", true},
		"other build":     {checkTime.Add(time.Hour), testLatest, true},
		"clock went back": {checkTime.Add(-time.Hour), "20261001-b50fc20", true},
	} {
		if got := c.Stale(tt.current, tt.now); got != tt.want {
			t.Errorf("%s: Stale = %v, want %v", label, got, tt.want)
		}
	}
	if !(Cache{}).Stale("20261001-b50fc20", checkTime) {
		t.Error("an empty cache is stale")
	}
}

func TestLoadCacheIgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	if got := LoadCache(filepath.Join(dir, "missing.json")); got != (Cache{}) {
		t.Fatalf("missing file = %+v", got)
	}
	garbage := filepath.Join(dir, "update-check.json")
	if err := os.WriteFile(garbage, []byte(`{"checkedAt": "yest`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadCache(garbage)
	if got != (Cache{}) || !got.Stale("20261001-b50fc20", checkTime) {
		t.Fatalf("garbage file = %+v, want an empty, stale cache", got)
	}
}
