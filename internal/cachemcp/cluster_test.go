package cachemcp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeForward maps announced node addresses to local ones, counting the tunnels opened and stopped
// and the failed attempts; end ends the last tunnel opened.
type fakeForward struct {
	locals  map[string]string
	fail    error
	end     chan struct{}
	opened  int
	stopped int
	failed  int
	mu      sync.Mutex
}

func (f *fakeForward) forward(_ context.Context, addr string) (local string, stop func(), done <-chan struct{}, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		f.failed++
		return "", nil, nil, f.fail
	}
	f.opened++
	f.end = make(chan struct{})
	return f.locals[addr], func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
	}, f.end, nil
}

// counts returns opened, stopped and failed.
func (f *fakeForward) counts() (opened, stopped, failed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened, f.stopped, f.failed
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

func TestDialNodeRetriesAFailedTunnelOnlyAfterADelay(t *testing.T) {
	other := startRedis(t)
	f := &fakeForward{locals: map[string]string{"10.0.1.5:6379": other.Addr()}, fail: errors.New("ssm refused")}
	c := newClients(ConnConfig{LocalPort: 1, Forward: f.forward})
	cleanup(t, c.shutdown)
	ctx := context.Background()
	for range 3 {
		if _, err := c.dialNode(ctx, "tcp", "10.0.1.5:6379"); err == nil || !strings.Contains(err.Error(), "ssm refused") {
			t.Fatalf("dial = %v, want the forward error", err)
		}
	}
	if _, _, failed := f.counts(); failed != 1 {
		t.Fatalf("tunnel attempts = %d, want 1 until the retry delay passed", failed)
	}
	f.mu.Lock()
	f.fail = nil
	f.mu.Unlock()
	c.tunnels.mu.Lock()
	c.tunnels.retryDelay = 0
	c.tunnels.mu.Unlock()
	conn, err := c.dialNode(ctx, "tcp", "10.0.1.5:6379")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDialNodeReopensATunnelThatEnded(t *testing.T) {
	other := startRedis(t)
	f := &fakeForward{locals: map[string]string{"10.0.1.5:6379": other.Addr()}}
	c := newClients(ConnConfig{LocalPort: 1, Forward: f.forward})
	cleanup(t, c.shutdown)
	ctx := context.Background()
	if _, err := c.tunnels.local(ctx, "10.0.1.5:6379"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	close(f.end)
	f.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for _, stopped, _ := f.counts(); stopped != 1; _, stopped, _ = f.counts() {
		if time.Now().After(deadline) {
			t.Fatal("the ended tunnel was not stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c.tunnels.local(ctx, "10.0.1.5:6379"); err != nil {
		t.Fatal(err)
	}
	if opened, _, _ := f.counts(); opened != 2 {
		t.Fatalf("tunnels opened = %d, want a new one after the first ended", opened)
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
	if !strings.HasPrefix(cursors[0], first.Addr()+"@") && !strings.HasPrefix(cursors[0], second.Addr()+"@") {
		t.Fatalf("cursors = %v, want <address>@<cursor>", cursors)
	}
	for _, bad := range []string{"abc", "1", "@1", first.Addr() + "@x", first.Addr() + "@-1"} {
		if _, err := c.scan(ctx, nodes, true, &scanInput{Cursor: bad}); err == nil {
			t.Errorf("cursor %q must be refused", bad)
		}
	}
	// The cursor names its node, wherever it is sorted now.
	out, err := c.scan(ctx, nodes[1:], true, &scanInput{Cursor: nodes[1].Options().Addr + "@0", Limit: 100})
	if err != nil || out.Cursor != "0" || len(out.Keys) != 4 {
		t.Fatalf("resuming on the second node alone = %+v, %v; want its 4 keys", out, err)
	}
	// A node that is no longer a primary (failover, resharding) fails rather than skip a shard.
	if _, err := c.scan(ctx, nodes[1:], true, &scanInput{Cursor: nodes[0].Options().Addr + "@3"}); err == nil || !strings.Contains(err.Error(), "no longer a primary") {
		t.Fatalf("cursor of a gone node: %v", err)
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

func TestClusterDetectionOnlyTakesAMissingCommandForStandalone(t *testing.T) {
	ctx := context.Background()
	for _, reply := range []string{"LOADING Redis is loading the dataset in memory", "ERR max number of clients reached"} {
		c, m := testCache(t)
		m.SetError(reply)
		if _, err := c.clients.clusterClient(ctx); err == nil {
			t.Fatalf("%s: detection succeeded, want the error", reply)
		}
		// Not remembered: the next call detects again.
		m.SetError("")
		if cluster, err := c.clients.clusterClient(ctx); err != nil || cluster != nil {
			t.Fatalf("%s, then a reply: clusterClient = %v, %v; want standalone", reply, cluster, err)
		}
	}
	c, m := testCache(t)
	m.SetError("ERR unknown command 'info', with args beginning with: 'cluster'")
	if cluster, err := c.clients.clusterClient(ctx); err != nil || cluster != nil {
		t.Fatalf("without INFO: clusterClient = %v, %v; want standalone", cluster, err)
	}
}

// replyError is an error reply of the cache.
type replyError string

func (e replyError) Error() string { return string(e) }
func (replyError) RedisError()     {}

// replies makes a client answer the error reply mapped to a command name instead of sending it.
type replies map[string]string

func (r replies) DialHook(next redis.DialHook) redis.DialHook { return next }
func (r replies) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if reply, ok := r[cmd.Name()]; ok {
			cmd.SetErr(replyError(reply))
			return cmd.Err()
		}
		return next(ctx, cmd)
	}
}
func (r replies) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestClusterDetectionProbesWhenTheACLRefusesInfo(t *testing.T) {
	ctx := context.Background()
	noperm := "NOPERM User reader has no permissions to run the 'info' command"
	cases := []struct {
		replies   replies
		name      string
		cluster   bool
		warning   bool
		transient bool
	}{
		{name: "standalone counts the keys", replies: replies{"info": noperm}},
		{name: "a cluster refuses keys of two slots", replies: replies{"info": noperm, "exists": "CROSSSLOT Keys in request don't hash to the same slot"}, cluster: true},
		{name: "a cluster redirects", replies: replies{"info": noperm, "exists": "MOVED 3300 10.0.1.5:6379"}, cluster: true},
		{name: "refused too: standalone with a warning", replies: replies{"info": noperm, "exists": "NOPERM User reader has no permissions to access one of the keys"}, warning: true},
		{name: "a transient error comes back", replies: replies{"info": noperm, "exists": "LOADING Redis is loading the dataset in memory"}, transient: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCache(t)
			c.clients.get(0).AddHook(tc.replies)
			cluster, err := c.clients.clusterClient(ctx)
			if tc.transient {
				if err == nil || c.clients.detected {
					t.Fatalf("clusterClient = %v, detected %v; want the error, not remembered", err, c.clients.detected)
				}
				return
			}
			if err != nil || (cluster != nil) != tc.cluster || (c.clients.modeWarning() != "") != tc.warning {
				t.Fatalf("clusterClient = %v, %v, warning %q; want cluster %v, warning %v", cluster, err, c.clients.modeWarning(), tc.cluster, tc.warning)
			}
		})
	}
}

func TestToolResultsWarnWhenTheClusterModeIsUnknown(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("k", "v"))
	session, _ := connect(t, c, false)
	if text, isError := callTool(t, session, "get_key", map[string]any{"key": "k"}); isError || strings.Contains(text, "Warning") {
		t.Fatalf("get_key = %s, %v; want no warning", text, isError)
	}
	if err := c.clients.close(); err != nil {
		t.Fatal(err)
	}
	c.clients.get(0).AddHook(replies{"info": "NOPERM no info", "exists": "NOPERM no keys"})
	text, isError := callTool(t, session, "get_key", map[string]any{"key": "k"})
	if isError || !strings.Contains(text, `"value":"v"`) || !strings.Contains(text, "Warning: "+unknownModeWarning) {
		t.Fatalf("get_key = %s, %v; want the value and the warning", text, isError)
	}
}

// twoNodes are two fake primaries of a cluster, sorted by address, with keys a, b, c on the first
// and d on the second.
func twoNodes(t *testing.T) []*redis.Client {
	t.Helper()
	var nodes []*redis.Client
	for _, keys := range [][]string{{"a", "b", "c"}, {"d"}} {
		m := startRedis(t)
		for _, key := range keys {
			seed(t, m.Set(key, "v"))
		}
		client := redis.NewClient(&redis.Options{Addr: m.Addr(), Protocol: 2, DisableIdentity: true})
		cleanup(t, client.Close)
		nodes = append(nodes, client)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Options().Addr < nodes[j].Options().Addr })
	return nodes
}

func TestInfoReadsEveryPrimary(t *testing.T) {
	nodes := twoNodes(t)
	out, err := (&cache{}).info(t.Context(), nodes, true, []string{"clients", "server"})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		addr := node.Options().Addr
		if out.Sections[addr+" clients"]["connected_clients"] == "" || out.Errors[addr+" server"] == "" {
			t.Errorf("%s missing from %+v", addr, out)
		}
	}
	if out.Keys != 4 {
		t.Fatalf("keys = %d, want the 4 keys of both nodes", out.Keys)
	}
}

func TestKeylessCommandsSayWhichPrimariesAnswered(t *testing.T) {
	nodes := twoNodes(t)
	c := &cache{}
	out, _, err := c.keylessCommand(t.Context(), nodes, []string{"dbsize"})
	if err != nil || out.Reply != int64(4) || !strings.Contains(out.Note, "2 primaries") {
		t.Fatalf("DBSIZE = %+v, %v; want the sum over both primaries", out, err)
	}
	out, _, err = c.keylessCommand(t.Context(), nodes, []string{"PING"})
	if err != nil || out.Reply != "PONG" || !strings.Contains(out.Note, nodes[0].Options().Addr+" answered (1 of 2)") {
		t.Fatalf("PING = %+v, %v; want the first primary named", out, err)
	}
	for args, want := range map[string]bool{"DBSIZE": true, "slowlog get": true, "MEMORY STATS": true, "MEMORY USAGE k": false, "GET k": false} {
		if got := keyless(strings.Fields(args)); got != want {
			t.Errorf("keyless(%s) = %v, want %v", args, got, want)
		}
	}
}
