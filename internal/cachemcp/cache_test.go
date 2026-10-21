package cachemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// fmtValue prints a tool value the way the cases below spell it.
func fmtValue(v any) string { return fmt.Sprint(v) }

// seed fails the test when preparing the fake cache failed.
func seed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// errOf drops the value of a call, keeping its error for seed.
func errOf[T any](_ T, err error) error { return err }

func testCache(t *testing.T) (*cache, *miniredis.Miniredis) {
	t.Helper()
	m := startRedis(t)
	c := &cache{clients: newClients(ConnConfig{LocalPort: portOf(t, m)})}
	t.Cleanup(func() {
		if err := c.clients.close(); err != nil {
			t.Error(err)
		}
	})
	return c, m
}

func TestParseInfo(t *testing.T) {
	got := parseInfo("# Server\r\nredis_version:7.1.0\r\nuptime_in_days:3\r\n\r\n# Keyspace\r\ndb0:keys=12,expires=1\r\n")
	if got["server"]["redis_version"] != "7.1.0" || got["keyspace"]["db0"] != "keys=12,expires=1" {
		t.Fatalf("parseInfo = %#v", got)
	}
}

func TestInfoReportsSectionsErrorsAndKeys(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("a", "1"))
	// miniredis serves INFO clients but not INFO server, like a cache refusing a section.
	out, err := c.Info(context.Background(), &infoInput{Section: "clients, server"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Sections["clients"]["connected_clients"] == "" || out.Errors["server"] == "" || out.Keys != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestScanListsKeysWithTypeAndTTL(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("user:1", "a"))
	m.SetTTL("user:1", time.Minute)
	m.HSet("user:2", "name", "b")
	seed(t, m.Set("order:1", "c"))

	out, err := c.Scan(context.Background(), &scanInput{Pattern: "user:*"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Cursor != "0" || len(out.Keys) != 2 {
		t.Fatalf("out = %+v", out)
	}
	byKey := map[string]keyInfo{}
	for _, k := range out.Keys {
		byKey[k.Key] = k
	}
	if byKey["user:1"].Type != "string" || byKey["user:1"].TTL != 60 || byKey["user:2"].Type != "hash" || byKey["user:2"].TTL != -1 {
		t.Fatalf("keys = %+v", byKey)
	}

	hashes, err := c.Scan(context.Background(), &scanInput{Type: "hash"})
	if err != nil || len(hashes.Keys) != 1 || hashes.Keys[0].Key != "user:2" {
		t.Fatalf("hashes = %+v, %v", hashes, err)
	}
	if _, err := c.Scan(context.Background(), &scanInput{Cursor: "abc"}); err == nil {
		t.Fatal("an invalid cursor must fail")
	}
}

func TestGetReadsEveryType(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("s", "hello"))
	m.HSet("h", "a", "1", "b", "2")
	seed(t, errOf(m.Push("l", "x", "y", "z")))
	seed(t, errOf(m.SAdd("set", "m1")))
	seed(t, errOf(m.ZAdd("z", 1.5, "m")))
	seed(t, errOf(m.XAdd("x", "1-1", []string{"f", "v"})))
	ctx := context.Background()

	cases := map[string]string{"s": `hello`, "h": `map[a:1 b:2]`, "l": `[x y z]`, "set": `[m1]`, "z": `[{m 1.5}]`, "x": `[{map[f:v] 1-1}]`}
	for key, want := range cases {
		out, err := c.Get(ctx, &getKeyInput{Key: key})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got := fmtValue(out.Value); got != want {
			t.Errorf("%s (%s) = %s, want %s", key, out.Type, got, want)
		}
	}
	if _, err := c.Get(ctx, &getKeyInput{Key: "missing"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestGetCutsLongValues(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("big", strings.Repeat("a", maxStringBytes+10)))
	seed(t, errOf(m.Push("l", "1", "2", "3")))
	ctx := context.Background()

	big, err := c.Get(ctx, &getKeyInput{Key: "big"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fmtValue(big.Value)) != maxStringBytes || !big.Truncated || big.Length != maxStringBytes+10 {
		t.Fatalf("big = %d bytes, truncated %v, length %d", len(fmtValue(big.Value)), big.Truncated, big.Length)
	}
	list, err := c.Get(ctx, &getKeyInput{Key: "l", MaxItems: 2})
	if err != nil || fmtValue(list.Value) != "[1 2]" || !list.Truncated || list.Length != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestCommandGuardsReadsAndRunsWrites(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("k", "v"))
	ctx := context.Background()

	out, count, err := c.Command(ctx, &commandInput{Args: []string{"GET", "k"}}, false)
	if err != nil || out.Reply != "v" || count != 1 {
		t.Fatalf("GET = %+v %d %v", out, count, err)
	}
	if _, _, err = c.Command(ctx, &commandInput{Args: []string{"SET", "k", "w"}}, false); !errors.Is(err, ErrReadOnlyGuard) {
		t.Fatalf("SET read-only: %v", err)
	}
	if _, _, err = c.Command(ctx, &commandInput{Args: []string{"SET", "k", "w"}}, true); err != nil {
		t.Fatalf("SET with writes: %v", err)
	}
	if got, getErr := m.Get("k"); getErr != nil || got != "w" {
		t.Fatalf("k = %q", got)
	}
	if _, _, err = c.Command(ctx, &commandInput{Args: []string{"FLUSHALL"}}, true); err == nil {
		t.Fatal("FLUSHALL must stay refused with writes")
	}
	missing, _, err := c.Command(ctx, &commandInput{Args: []string{"GET", "nope"}}, false)
	if err != nil || missing.Reply != nil {
		t.Fatalf("missing key = %+v, %v", missing, err)
	}
	list, count, err := c.Command(ctx, &commandInput{Args: []string{"MGET", "k", "nope"}}, false)
	if err != nil || count != 2 || fmtValue(list.Reply) != "[w <nil>]" {
		t.Fatalf("MGET = %+v %d", list, count)
	}
}

func TestRejectedCredentialsAreReported(t *testing.T) {
	m := startRedis(t)
	m.RequireUserAuth("app", "good")
	rejected := 0
	c := &cache{clients: newClients(ConnConfig{LocalPort: portOf(t, m), Credentials: PasswordCredentials("app", "bad")}), rejected: func() { rejected++ }}
	defer func() {
		if err := c.clients.close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := c.Get(context.Background(), &getKeyInput{Key: "k"}); !errors.Is(err, ErrSignInRejected) || rejected != 1 {
		t.Fatalf("err = %v, rejected = %d", err, rejected)
	}
}

func TestCommandCutsLongReplies(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("big", strings.Repeat("x", maxStringBytes+10)))
	for i := 0; i < maxItemsLimit+5; i++ {
		_, err := m.RPush("items", "v")
		seed(t, err)
	}
	out, _, err := c.Command(t.Context(), &commandInput{Args: []string{"GET", "big"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := out.Reply.(string)
	if !ok || len(s) != maxStringBytes || !out.Truncated {
		t.Fatalf("GET big = %d bytes, truncated=%v", len(s), out.Truncated)
	}
	// The read guard refuses ranges over maxItemsLimit: execute still caps them.
	out, _, err = c.Command(t.Context(), &commandInput{Args: []string{"LRANGE", "items", "0", "-1"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := out.Reply.([]any)
	if !ok || len(items) != maxItemsLimit || !out.Truncated {
		t.Fatalf("LRANGE = %d items, truncated=%v", len(items), out.Truncated)
	}
	small, _, err := c.Command(t.Context(), &commandInput{Args: []string{"LRANGE", "items", "0", "1"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if small.Truncated {
		t.Fatal("a small reply must not be truncated")
	}
}

// jsonSize is the size of v encoded as JSON.
func jsonSize(t *testing.T, v any) int {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestGetKeepsCollectionsUnderTheResultBudget(t *testing.T) {
	c, m := testCache(t)
	value := strings.Repeat("v", 10<<10)
	for i := 0; i < 100; i++ {
		m.HSet("h", "field"+strconv.Itoa(i), value)
		seed(t, errOf(m.Push("l", value)))
		seed(t, errOf(m.SAdd("set", value+strconv.Itoa(i))))
		seed(t, errOf(m.ZAdd("z", float64(i), value+strconv.Itoa(i))))
		seed(t, errOf(m.XAdd("x", strconv.Itoa(i+1)+"-1", []string{"f", value})))
	}
	for _, key := range []string{"h", "l", "set", "z", "x"} {
		out, err := c.Get(context.Background(), &getKeyInput{Key: key})
		if err != nil {
			t.Fatal(err)
		}
		if size := jsonSize(t, out); size > maxResultBytes || size < maxResultBytes/2 || !out.Truncated || out.Length != 100 {
			t.Errorf("%s: %d bytes of JSON, truncated %v, length %d; want under %d, truncated", key, size, out.Truncated, out.Length, maxResultBytes)
		}
	}
}

// onlyItem is the string of a one-item get_key value: the field f of a hash or stream, the item of
// a list or set, the member of a sorted set.
func onlyItem(t *testing.T, value any) string {
	t.Helper()
	switch v := value.(type) {
	case map[string]string:
		return v["f"]
	case []string:
		if len(v) == 1 {
			return v[0]
		}
	case []scoredMember:
		if len(v) == 1 {
			return v[0].Member
		}
	case []streamEntry:
		if len(v) == 1 {
			return fmt.Sprint(v[0].Fields["f"])
		}
	}
	t.Fatalf("value = %T of unexpected shape", value)
	return ""
}

func TestGetCutsLongItemsOfCollections(t *testing.T) {
	c, m := testCache(t)
	long := strings.Repeat("a", 100<<10)
	m.HSet("h", "f", long)
	seed(t, errOf(m.Push("l", long)))
	seed(t, errOf(m.SAdd("set", long)))
	seed(t, errOf(m.ZAdd("z", 1, long)))
	seed(t, errOf(m.XAdd("x", "1-1", []string{"f", long})))
	ctx := context.Background()

	for _, key := range []string{"h", "l", "set", "z", "x"} {
		out, err := c.Get(ctx, &getKeyInput{Key: key})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(onlyItem(t, out.Value)); got != maxStringBytes || !out.Truncated || out.Length != 1 {
			t.Errorf("%s: item of %d bytes, truncated %v, length %d; want %d bytes, truncated", key, got, out.Truncated, out.Length, maxStringBytes)
		}
	}
}

func TestCommandKeepsRepliesUnderTheResultBudget(t *testing.T) {
	c, m := testCache(t)
	value := strings.Repeat("v", 10<<10)
	for i := 0; i < 100; i++ {
		seed(t, errOf(m.Push("l", value)))
		m.HSet("h", "field"+strconv.Itoa(i), value)
	}
	for _, args := range [][]string{{"LRANGE", "l", "0", "99"}, {"HMGET", "h", "field1", "field2"}} {
		out, _, err := c.Command(t.Context(), &commandInput{Args: args}, false)
		if err != nil {
			t.Fatal(err)
		}
		size := jsonSize(t, out)
		if args[0] == "LRANGE" && (size > maxResultBytes || size < maxResultBytes/2 || !out.Truncated) {
			t.Errorf("LRANGE: %d bytes of JSON, truncated %v; want under %d, truncated", size, out.Truncated, maxResultBytes)
		}
		if args[0] == "HMGET" && out.Truncated {
			t.Errorf("HMGET of two fields truncated")
		}
	}
}
