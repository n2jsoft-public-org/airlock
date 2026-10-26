package cachemcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

const (
	defaultScanLimit = 100
	maxScanLimit     = 1000
	defaultMaxItems  = 100
	maxItemsLimit    = 1000
	// maxStringBytes caps the string values returned.
	maxStringBytes = 64 << 10
	// maxResultBytes caps the JSON of a get_key value or of a command reply: items stop being
	// added once it is reached.
	maxResultBytes = 256 << 10
	// scanRounds bounds the SCAN iterations of one call on sparse matches.
	scanRounds = 50
	// maxDB is the highest database index the tools accept, which bounds the clients kept per db.
	maxDB = 1023
	// infoConcurrency is how many cluster primaries info reads at once.
	infoConcurrency = 8
)

// defaultInfoSections are read when the assistant names none.
var defaultInfoSections = []string{"server", "memory", "keyspace", "stats", "replication"}

type infoInput struct {
	Section string `json:"section,omitempty" jsonschema:"comma-separated INFO sections (default server,memory,keyspace,stats,replication)"`
	DB      int    `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type infoOutput struct {
	// Sections are named "<primary address> <section>" in cluster mode, one set per primary.
	Sections map[string]map[string]string `json:"sections"`
	// Errors holds the sections the cache refused, with its answer.
	Errors map[string]string `json:"errors,omitempty"`
	// Keys is the number of keys of the database (DBSIZE), of every primary in cluster mode.
	Keys int64 `json:"keys"`
}

type scanInput struct {
	Pattern string `json:"pattern,omitempty" jsonschema:"glob pattern of the keys (default *)"`
	Type    string `json:"type,omitempty" jsonschema:"only keys of this type: string, hash, list, set, zset or stream"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"cursor returned by a previous call, to continue the scan"`
	Limit   int    `json:"limit,omitempty" jsonschema:"about how many keys to return (default 100, max 1000)"`
	DB      int    `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type keyInfo struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	// TTL is in seconds; -1 without expiry, -2 when the key vanished meanwhile.
	TTL int64 `json:"ttl_seconds"`
}

type scanOutput struct {
	// Cursor continues the scan; "0" when every key was visited.
	Cursor string    `json:"cursor"`
	Keys   []keyInfo `json:"keys"`
}

type getKeyInput struct {
	Key      string `json:"key" jsonschema:"the key to read"`
	MaxItems int    `json:"max_items,omitempty" jsonschema:"maximum number of items of a hash, list, set, sorted set or stream (default 100, max 1000)"`
	DB       int    `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type keyValue struct {
	// Value holds strings as jsonString returns them, field names as jsonKey does.
	Value any    `json:"value"`
	Key   string `json:"key"`
	Type  string `json:"type"`
	TTL   int64  `json:"ttl_seconds"`
	// Length is the number of items, or of bytes for a string.
	Length      int64 `json:"length"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	Truncated   bool  `json:"truncated"`
}

type commandInput struct {
	Args []string `json:"args" jsonschema:"the command and its arguments, e.g. [\"HGET\", \"user:1\", \"name\"]"`
	DB   int      `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type commandOutput struct {
	Reply any `json:"reply"`
	// Note tells, in cluster mode, which primaries answered a command without key.
	Note string `json:"note,omitempty"`
	// Truncated reports that a string or an array of the reply was cut.
	Truncated bool `json:"truncated"`
}

type scoredMember struct {
	Member any     `json:"member"`
	Score  float64 `json:"score"`
}

type streamEntry struct {
	Fields map[string]any `json:"fields"`
	ID     string         `json:"id"`
}

// cache runs the tools against the databases of one cache.
type cache struct {
	clients *clients
	// rejected is called when the cache refuses the credentials; nil when nothing is to be done.
	rejected func()
}

// client is the client of database db, refused out of 0..maxDB before any client is created: in
// cluster mode, the cluster client routing each key to its shard.
func (c *cache) client(ctx context.Context, db int) (redis.UniversalClient, error) {
	if db < 0 || db > maxDB {
		return nil, fmt.Errorf("db must be between 0 and %d", maxDB)
	}
	client, err := c.clients.open(ctx, db)
	if err != nil {
		return nil, c.fail(err)
	}
	return client, nil
}

// nodes are the primaries holding the keys of database db: the cache itself, or every primary of
// a cluster, sorted by address. cluster reports cluster mode.
func (c *cache) nodes(ctx context.Context, db int) (nodes []*redis.Client, cluster bool, err error) {
	client, err := c.client(ctx, db)
	if err != nil {
		return nil, false, err
	}
	clusterClient, cluster := client.(*redis.ClusterClient)
	if !cluster {
		return []*redis.Client{c.clients.get(db)}, false, nil
	}
	if nodes, err = primaries(ctx, clusterClient); err != nil {
		return nil, true, c.fail(err)
	}
	return nodes, true, nil
}

// fail maps err and reports refused credentials.
func (c *cache) fail(err error) error {
	err = mapError(err)
	if errors.Is(err, ErrSignInRejected) && c.rejected != nil {
		c.rejected()
	}
	return err
}

func (c *cache) Info(ctx context.Context, in *infoInput) (infoOutput, error) {
	nodes, cluster, err := c.nodes(ctx, in.DB)
	if err != nil {
		return infoOutput{}, err
	}
	sections := defaultInfoSections
	if requested := strings.TrimSpace(in.Section); requested != "" {
		sections = nil
		for _, section := range strings.Split(requested, ",") {
			if section = strings.ToLower(strings.TrimSpace(section)); section != "" {
				sections = append(sections, section)
			}
		}
	}
	return c.info(ctx, nodes, cluster, sections)
}

// info reads sections and the number of keys of nodes, infoConcurrency nodes at once: one after
// the other, the primaries of a cluster of a dozen shards would exceed callTimeout.
func (c *cache) info(ctx context.Context, nodes []*redis.Client, cluster bool, sections []string) (infoOutput, error) {
	results := make([]infoOutput, len(nodes))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(infoConcurrency)
	for idx, node := range nodes {
		prefix := ""
		if cluster {
			prefix = node.Options().Addr + " "
		}
		group.Go(func() (err error) {
			results[idx], err = nodeInfo(groupCtx, node, prefix, sections)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return infoOutput{}, c.fail(err)
	}
	out := infoOutput{Sections: map[string]map[string]string{}}
	for _, result := range results {
		maps.Copy(out.Sections, result.Sections)
		if len(result.Errors) > 0 && out.Errors == nil {
			out.Errors = map[string]string{}
		}
		maps.Copy(out.Errors, result.Errors)
		out.Keys += result.Keys
	}
	return out, nil
}

// nodeInfo reads sections and DBSIZE of node, naming the sections prefix and their name. A
// section the node refuses goes to Errors; refused credentials and a failed DBSIZE come back.
func nodeInfo(ctx context.Context, node *redis.Client, prefix string, sections []string) (infoOutput, error) {
	out := infoOutput{Sections: map[string]map[string]string{}}
	// One section per INFO: Redis 6 accepts a single section argument.
	for _, section := range sections {
		text, err := node.Info(ctx, section).Result()
		if err != nil {
			if errors.Is(mapError(err), ErrSignInRejected) {
				return infoOutput{}, err
			}
			if out.Errors == nil {
				out.Errors = map[string]string{}
			}
			out.Errors[prefix+section] = err.Error()
			continue
		}
		for name, values := range parseInfo(text) {
			out.Sections[prefix+name] = values
		}
	}
	keys, err := node.DBSize(ctx).Result()
	if err != nil {
		return infoOutput{}, err
	}
	out.Keys = keys
	return out, nil
}

// parseInfo reads the "# Section" headers and "key:value" lines of an INFO answer.
func parseInfo(text string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			current = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			sections[current] = map[string]string{}
		case current != "":
			if key, value, found := strings.Cut(line, ":"); found {
				sections[current][key] = value
			}
		}
	}
	return sections
}

func (c *cache) Scan(ctx context.Context, in *scanInput) (scanOutput, error) {
	nodes, cluster, err := c.nodes(ctx, in.DB)
	if err != nil {
		return scanOutput{}, err
	}
	return c.scan(ctx, nodes, cluster, in)
}

// scan runs SCAN on nodes one after the other. In cluster mode the cursor is "<address>@<cursor>",
// naming the primary being scanned: a cursor whose primary is gone (failover, resharding) fails
// rather than skip or repeat a shard.
func (c *cache) scan(ctx context.Context, nodes []*redis.Client, cluster bool, in *scanInput) (scanOutput, error) {
	limit := int64(mcpserve.Limit(in.Limit, defaultScanLimit, maxScanLimit))
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		pattern = "*"
	}
	addr, cursor, err := parseCursor(in.Cursor, cluster)
	if err != nil {
		return scanOutput{}, err
	}
	node := 0
	if addr != "" {
		node = slices.IndexFunc(nodes, func(n *redis.Client) bool { return n.Options().Addr == addr })
		if node < 0 {
			return scanOutput{}, fmt.Errorf("the cluster changed during the scan (%s is no longer a primary, after a failover or a resharding): start the scan again without a cursor", addr)
		}
	}

	keysOf := make([][]string, len(nodes))
	found := int64(0)
	for round := 0; round < scanRounds && found < limit && node < len(nodes); round++ {
		var batch []string
		if in.Type != "" {
			batch, cursor, err = nodes[node].ScanType(ctx, cursor, pattern, limit, in.Type).Result()
		} else {
			batch, cursor, err = nodes[node].Scan(ctx, cursor, pattern, limit).Result()
		}
		if err != nil {
			return scanOutput{}, c.fail(err)
		}
		keysOf[node] = append(keysOf[node], batch...)
		found += int64(len(batch))
		if cursor == 0 {
			node++
		}
	}

	out := scanOutput{Cursor: "0", Keys: make([]keyInfo, 0, found)}
	switch {
	case node >= len(nodes):
	case cluster:
		out.Cursor = nodes[node].Options().Addr + "@" + strconv.FormatUint(cursor, 10)
	default:
		out.Cursor = strconv.FormatUint(cursor, 10)
	}
	for n, keys := range keysOf {
		if len(keys) == 0 {
			continue
		}
		// The node that returned the keys holds them.
		pipe := nodes[n].Pipeline()
		types := make([]*redis.StatusCmd, len(keys))
		ttls := make([]*redis.DurationCmd, len(keys))
		for idx, key := range keys {
			types[idx] = pipe.Type(ctx, key)
			ttls[idx] = pipe.TTL(ctx, key)
		}
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return scanOutput{}, c.fail(err)
		}
		for idx, key := range keys {
			out.Keys = append(out.Keys, keyInfo{Key: key, Type: types[idx].Val(), TTL: ttlSeconds(ttls[idx].Val())})
		}
	}
	return out, nil
}

// parseCursor reads a scan_keys cursor: "<address>@<cursor>" in cluster mode, a SCAN cursor
// otherwise; empty and "0" start the scan (addr empty).
func parseCursor(text string, cluster bool) (addr string, cursor uint64, err error) {
	invalid := fmt.Errorf("invalid cursor %q: pass the cursor a previous scan_keys returned", text)
	if text == "" || text == "0" {
		return "", 0, nil
	}
	if cluster {
		at := strings.LastIndex(text, "@")
		if at <= 0 {
			return "", 0, invalid
		}
		addr, text = text[:at], text[at+1:]
	}
	if cursor, err = strconv.ParseUint(text, 10, 64); err != nil {
		return "", 0, invalid
	}
	return addr, cursor, nil
}

// ttlSeconds converts a go-redis TTL: -1 (no expiry) and -2 (no key) come back as is.
func ttlSeconds(ttl time.Duration) int64 {
	if ttl < 0 {
		return int64(ttl)
	}
	return int64(ttl / time.Second)
}

func (c *cache) Get(ctx context.Context, in *getKeyInput) (keyValue, error) {
	if in.Key == "" {
		return keyValue{}, errors.New("key is required")
	}
	client, err := c.client(ctx, in.DB)
	if err != nil {
		return keyValue{}, err
	}
	key := in.Key
	limit := mcpserve.Limit(in.MaxItems, defaultMaxItems, maxItemsLimit)

	kind, err := client.Type(ctx, key).Result()
	if err != nil {
		return keyValue{}, c.fail(err)
	}
	if kind == "none" {
		return keyValue{}, fmt.Errorf("key %q does not exist", key)
	}
	out := keyValue{Key: key, Type: kind}
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		return keyValue{}, c.fail(err)
	}
	out.TTL = ttlSeconds(ttl)
	// MEMORY USAGE is best effort: a cache may refuse it.
	if usage, usageErr := client.MemoryUsage(ctx, key).Result(); usageErr == nil {
		out.MemoryBytes = usage
	}

	switch kind {
	case "string":
		err = c.readString(ctx, client, &out)
	case "hash":
		err = c.readHash(ctx, client, &out, limit)
	case "list":
		err = c.readList(ctx, client, &out, limit)
	case "set":
		err = c.readSet(ctx, client, &out, limit)
	case "zset":
		err = c.readSortedSet(ctx, client, &out, limit)
	case "stream":
		err = c.readStream(ctx, client, &out, limit)
	}
	if err != nil {
		return keyValue{}, c.fail(err)
	}
	return out, nil
}

func (c *cache) readString(ctx context.Context, client redis.Cmdable, out *keyValue) error {
	length, err := client.StrLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	value, err := client.GetRange(ctx, out.Key, 0, maxStringBytes-1).Result()
	if err != nil {
		return err
	}
	if length > maxStringBytes {
		value = trimCutRune(value)
	}
	out.Value, out.Length, out.Truncated = jsonString(value), length, length > maxStringBytes
	return nil
}

func (c *cache) readHash(ctx context.Context, client redis.Cmdable, out *keyValue, limit int) error {
	length, err := client.HLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	fields := map[string]any{}
	budget := newReplyBudget()
	var cursor uint64
	cut, full := false, false
	for !full {
		pairs, next, err := client.HScan(ctx, out.Key, cursor, "*", int64(limit)).Result()
		if err != nil {
			return err
		}
		for idx := 0; idx+1 < len(pairs) && len(fields) < limit; idx += 2 {
			text, valueCut := cutString(pairs[idx+1])
			field, value := jsonKey(pairs[idx]), jsonString(text)
			if !budget.fits(field, value) {
				full = true
				break
			}
			fields[field], cut = value, cut || valueCut
		}
		if next == 0 || len(fields) >= limit {
			break
		}
		cursor = next
	}
	out.Value, out.Length, out.Truncated = fields, length, cut || full || length > int64(len(fields))
	return nil
}

func (c *cache) readList(ctx context.Context, client redis.Cmdable, out *keyValue, limit int) error {
	length, err := client.LLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	items, err := client.LRange(ctx, out.Key, 0, int64(limit-1)).Result()
	if err != nil {
		return err
	}
	kept, cut := fitStrings(items, newReplyBudget())
	out.Value, out.Length, out.Truncated = kept, length, cut || length > int64(len(kept))
	return nil
}

func (c *cache) readSet(ctx context.Context, client redis.Cmdable, out *keyValue, limit int) error {
	length, err := client.SCard(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	members := make([]any, 0)
	budget := newReplyBudget()
	var cursor uint64
	cut, full := false, false
	for !full {
		batch, next, err := client.SScan(ctx, out.Key, cursor, "*", int64(limit)).Result()
		if err != nil {
			return err
		}
		for _, member := range batch {
			if len(members) >= limit {
				break
			}
			text, valueCut := cutString(member)
			value := jsonString(text)
			if !budget.fits(value) {
				full = true
				break
			}
			members, cut = append(members, value), cut || valueCut
		}
		if next == 0 || len(members) >= limit {
			break
		}
		cursor = next
	}
	out.Value, out.Length, out.Truncated = members, length, cut || full || length > int64(len(members))
	return nil
}

func (c *cache) readSortedSet(ctx context.Context, client redis.Cmdable, out *keyValue, limit int) error {
	length, err := client.ZCard(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	scored, err := client.ZRangeWithScores(ctx, out.Key, 0, int64(limit-1)).Result()
	if err != nil {
		return err
	}
	members := make([]scoredMember, 0, len(scored))
	budget := newReplyBudget()
	cut := false
	for _, z := range scored {
		member, memberCut := cutString(fmt.Sprint(z.Member))
		entry := scoredMember{Member: jsonString(member), Score: z.Score}
		if !budget.fits(entry) {
			cut = true
			break
		}
		members, cut = append(members, entry), cut || memberCut
	}
	out.Value, out.Length, out.Truncated = members, length, cut || length > int64(len(members))
	return nil
}

func (c *cache) readStream(ctx context.Context, client redis.Cmdable, out *keyValue, limit int) error {
	length, err := client.XLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	messages, err := client.XRangeN(ctx, out.Key, "-", "+", int64(limit)).Result()
	if err != nil {
		return err
	}
	entries := make([]streamEntry, 0, len(messages))
	budget := newReplyBudget()
	cut := false
	for _, message := range messages {
		entry := streamEntry{ID: message.ID, Fields: make(map[string]any, len(message.Values))}
		for field, value := range message.Values {
			if text, ok := value.(string); ok {
				var fieldCut bool
				text, fieldCut = cutString(text)
				value, cut = jsonString(text), cut || fieldCut
			}
			entry.Fields[jsonKey(field)] = value
		}
		if !budget.fits(entry) {
			cut = true
			break
		}
		entries = append(entries, entry)
	}
	out.Value, out.Length, out.Truncated = entries, length, cut || length > int64(len(entries))
	return nil
}

// Command runs one command: a read command of the allowlist, or with write any command but the
// denylist. It returns the reply and the number of items it holds.
func (c *cache) Command(ctx context.Context, in *commandInput, write bool) (commandOutput, int64, error) {
	check := CheckRead
	if write {
		check = CheckWrite
	}
	if err := check(in.Args); err != nil {
		return commandOutput{}, 0, err
	}
	client, err := c.client(ctx, in.DB)
	if err != nil {
		return commandOutput{}, 0, err
	}
	if cluster, ok := client.(*redis.ClusterClient); ok && keyless(in.Args) {
		nodes, nodesErr := primaries(ctx, cluster)
		if nodesErr != nil {
			return commandOutput{}, 0, c.fail(nodesErr)
		}
		return c.keylessCommand(ctx, nodes, in.Args)
	}
	reply, err := client.Do(ctx, commandArgs(in.Args)...).Result()
	return c.commandReply(reply, err)
}

// keylessCommands name no key: a cluster client would send them to any node. MEMORY does but for
// MEMORY USAGE.
// ponytail: a fixed list, the keyless read commands and the common admin ones; a keyless command
// missing here goes to a node go-redis picks, without a note.
var keylessCommands = setOf("DBSIZE", "PING", "TIME", "ECHO", "SLOWLOG", "CLIENT", "LATENCY", "INFO",
	"RANDOMKEY", "LASTSAVE", "ROLE", "COMMAND", "LOLWUT")

// keyless reports whether the command of args names no key.
func keyless(args []string) bool {
	name, sub, err := commandName(args)
	return err == nil && (keylessCommands[name] || (name == "MEMORY" && sub != "USAGE"))
}

// keylessCommand runs a command without key on the primaries of a cluster, sorted by address:
// DBSIZE on every one, adding up their answers, any other on the first one only. Its note says so.
func (c *cache) keylessCommand(ctx context.Context, nodes []*redis.Client, args []string) (commandOutput, int64, error) {
	if len(nodes) == 0 {
		return commandOutput{}, 0, errors.New("the cluster announces no primary")
	}
	if name, _, _ := commandName(args); name == "DBSIZE" { //nolint:errcheck // keyless checked args.
		var total int64
		for _, node := range nodes {
			keys, err := node.DBSize(ctx).Result()
			if err != nil {
				return commandOutput{}, 0, c.fail(err)
			}
			total += keys
		}
		note := fmt.Sprintf("cluster mode: the sum of DBSIZE over the %d primaries", len(nodes))
		return commandOutput{Reply: total, Note: note}, 1, nil
	}
	out, count, err := c.commandReply(nodes[0].Do(ctx, commandArgs(args)...).Result())
	if err == nil {
		out.Note = fmt.Sprintf("cluster mode: only the primary %s answered (1 of %d): a command without key runs on one node, "+
			"so its answer covers that node only", nodes[0].Options().Addr, len(nodes))
	}
	return out, count, err
}

// commandArgs is args as go-redis takes them.
func commandArgs(args []string) []any {
	out := make([]any, len(args))
	for idx, arg := range args {
		out[idx] = arg
	}
	return out
}

// commandReply is the capped reply of a command and the number of items it holds.
func (c *cache) commandReply(reply any, err error) (commandOutput, int64, error) {
	if errors.Is(err, redis.Nil) {
		return commandOutput{Reply: nil}, 0, nil
	}
	if err != nil {
		return commandOutput{}, 0, c.fail(err)
	}
	value, truncated := capReply(jsonReply(reply))
	count := int64(1)
	if items, ok := value.([]any); ok {
		count = int64(len(items))
	}
	return commandOutput{Reply: value, Truncated: truncated}, count, nil
}

// jsonReply converts a RESP reply to JSON values: maps get string keys (see jsonKey); capReply
// encodes the strings.
func jsonReply(reply any) any {
	switch v := reply.(type) {
	case []any:
		out := make([]any, len(v))
		for idx, item := range v {
			out[idx] = jsonReply(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[jsonKey(fmt.Sprint(key))] = jsonReply(item)
		}
		return out
	case error:
		return v.Error()
	default:
		return v
	}
}
