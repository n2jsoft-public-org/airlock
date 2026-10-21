package cachemcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeForward maps announced node addresses to local ones, counting the tunnels opened and stopped.
type fakeForward struct {
	locals  map[string]string
	fail    error
	opened  int
	stopped int
	mu      sync.Mutex
}

func (f *fakeForward) forward(_ context.Context, addr string) (local string, stop func(), err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", nil, f.fail
	}
	f.opened++
	return f.locals[addr], func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
	}, nil
}

// cleanup runs closeFn once the test ends, failing the test on error.
func cleanup(t *testing.T, closeFn func() error) {
	t.Helper()
	t.Cleanup(func() {
		if err := closeFn(); err != nil {
			t.Error(err)
		}
	})
}

// nodeClient reaches addr through the dialer of c, like a node client of the cluster client.
func nodeClient(t *testing.T, c *clients, addr string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: addr, Dialer: c.dialNode, Protocol: 2, DisableIdentity: true})
	cleanup(t, client.Close)
	return client
}

func TestDialNodeRemapsAnnouncedAddressesThroughOneTunnelEach(t *testing.T) {
	seedNode, other := startRedis(t), startRedis(t)
	seed(t, other.Set("k", "on the other node"))
	f := &fakeForward{locals: map[string]string{"10.0.1.5:6379": other.Addr()}}
	c := newClients(ConnConfig{LocalPort: portOf(t, seedNode), Forward: f.forward})
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := nodeClient(t, c, "10.0.1.5:6379").Get(ctx, "k").Result(); err != nil || got != "on the other node" {
				t.Errorf("GET through the tunnel = %q, %v", got, err)
			}
		}()
	}
	wg.Wait()
	// The seed is the session's tunnel: no tunnel of its own.
	if err := nodeClient(t, c, c.seed()).Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if f.opened != 1 {
		t.Fatalf("tunnels opened = %d, want 1 for the node, none for the seed", f.opened)
	}
	if err := c.shutdown(); err != nil {
		t.Fatal(err)
	}
	if f.stopped != 1 {
		t.Fatalf("tunnels stopped = %d, want 1", f.stopped)
	}
	if _, err := c.tunnels.local(ctx, "10.0.1.5:6379"); !errors.Is(err, errTunnelsClosed) {
		t.Fatalf("a tunnel after shutdown: %v", err)
	}
}

func TestDialNodeRetriesAFailedTunnel(t *testing.T) {
	other := startRedis(t)
	f := &fakeForward{locals: map[string]string{"10.0.1.5:6379": other.Addr()}, fail: errors.New("ssm refused")}
	c := newClients(ConnConfig{LocalPort: 1, Forward: f.forward})
	cleanup(t, c.shutdown)
	ctx := context.Background()
	if _, err := c.dialNode(ctx, "tcp", "10.0.1.5:6379"); err == nil || !strings.Contains(err.Error(), "ssm refused") {
		t.Fatalf("dial = %v, want the forward error", err)
	}
	f.fail = nil
	conn, err := c.dialNode(ctx, "tcp", "10.0.1.5:6379")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDialNodeChecksTheNodeHostNameOrTheEndpoint(t *testing.T) {
	named := startTLSRedis(t, "node-0001.cache.example")
	f := &fakeForward{locals: map[string]string{"node-0001.cache.example:6379": named.Addr(), "10.0.1.5:6379": named.Addr()}}
	ctx := context.Background()

	// A node announced by name is checked against its name, whatever the endpoint.
	c := newClients(ConnConfig{LocalPort: 1, TLS: true, ServerName: "clustercfg.cache.example", Forward: f.forward})
	cleanup(t, c.shutdown)
	if err := nodeClient(t, c, "node-0001.cache.example:6379").Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	// A node announced by IP is checked against the endpoint.
	if err := nodeClient(t, c, "10.0.1.5:6379").Ping(ctx).Err(); err == nil {
		t.Fatal("the certificate does not name the endpoint: the dial must fail")
	}
	c2 := newClients(ConnConfig{LocalPort: 1, TLS: true, ServerName: "node-0001.cache.example", Forward: f.forward})
	cleanup(t, c2.shutdown)
	if err := nodeClient(t, c2, "10.0.1.5:6379").Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestScanVisitsEveryNodeWithACombinedCursor(t *testing.T) {
	first, second := startRedis(t), startRedis(t)
	want := map[string]bool{}
	for _, m := range []*miniredis.Miniredis{first, second} {
		for _, key := range []string{"a", "b", "c"} {
			key = m.Addr() + "/" + key
			seed(t, m.Set(key, "v"))
			want[key] = true
		}
	}
	second.HSet(second.Addr()+"/h", "f", "v")
	var nodes []*redis.Client
	for _, m := range []*miniredis.Miniredis{first, second} {
		client := redis.NewClient(&redis.Options{Addr: m.Addr(), Protocol: 2, DisableIdentity: true})
		cleanup(t, client.Close)
		nodes = append(nodes, client)
	}

	c, ctx := &cache{}, context.Background()
	got := map[string]string{}
	var cursors []string
	cursor := ""
	for range 20 {
		out, err := c.scan(ctx, nodes, true, &scanInput{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range out.Keys {
			got[key.Key] = key.Type
		}
		cursor = out.Cursor
		cursors = append(cursors, cursor)
		if cursor == "0" {
			break
		}
	}
	if cursor != "0" || len(got) != len(want)+1 || got[second.Addr()+"/h"] != "hash" {
		t.Fatalf("keys = %v, cursors %v; want every key of both nodes", got, cursors)
	}
	if !strings.HasPrefix(cursors[0], "0:") && !strings.HasPrefix(cursors[0], "1:") {
		t.Fatalf("cursors = %v, want <node>:<cursor>", cursors)
	}
	for _, bad := range []string{"abc", "1", "x:1", "-1:0", "1:x"} {
		if _, err := c.scan(ctx, nodes, true, &scanInput{Cursor: bad}); err == nil {
			t.Errorf("cursor %q must be refused", bad)
		}
	}
	// A cursor past the last node (the cluster lost a shard) ends the scan.
	if out, err := c.scan(ctx, nodes, true, &scanInput{Cursor: "5:0"}); err != nil || out.Cursor != "0" || len(out.Keys) != 0 {
		t.Fatalf("past the last node = %+v, %v", out, err)
	}
}

func TestStandaloneCacheIsNotTakenForACluster(t *testing.T) {
	c, _ := testCache(t)
	cluster, err := c.clients.clusterClient(context.Background())
	if err != nil || cluster != nil {
		t.Fatalf("clusterClient = %v, %v; want standalone", cluster, err)
	}
	if _, err := c.client(context.Background(), 3); err != nil {
		t.Fatalf("a standalone cache has several databases: %v", err)
	}
}
