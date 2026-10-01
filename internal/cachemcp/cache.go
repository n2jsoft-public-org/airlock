package cachemcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
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
)

// defaultInfoSections are read when the assistant names none.
var defaultInfoSections = []string{"server", "memory", "keyspace", "stats", "replication"}

type infoInput struct {
	Section string `json:"section,omitempty" jsonschema:"comma-separated INFO sections (default server,memory,keyspace,stats,replication)"`
	DB      int    `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type infoOutput struct {
	Sections map[string]map[string]string `json:"sections"`
	// Errors holds the sections the cache refused, with its answer.
	Errors map[string]string `json:"errors,omitempty"`
	// Keys is the number of keys of the database (DBSIZE).
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
	Note   string    `json:"note,omitempty"`
	Keys   []keyInfo `json:"keys"`
}

type getKeyInput struct {
	Key      string `json:"key" jsonschema:"the key to read"`
	MaxItems int    `json:"max_items,omitempty" jsonschema:"maximum number of items of a hash, list, set, sorted set or stream (default 100, max 1000)"`
	DB       int    `json:"db,omitempty" jsonschema:"database index, 0 to 1023 (default 0)"`
}

type keyValue struct {
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
	// Truncated reports that a string or an array of the reply was cut.
	Truncated bool `json:"truncated"`
}

type scoredMember struct {
	Member string  `json:"member"`
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
	rejected    func()
	clusterMode bool
}

// client is the client of database db, refused out of 0..maxDB before any client is created.
func (c *cache) client(db int) (*redis.Client, error) {
	if db < 0 || db > maxDB {
		return nil, fmt.Errorf("db must be between 0 and %d", maxDB)
	}
	return c.clients.get(db), nil
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
	client, err := c.client(in.DB)
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
	out := infoOutput{Sections: map[string]map[string]string{}}
	// One section per INFO: Redis 6 accepts a single section argument.
	for _, section := range sections {
		text, infoErr := client.Info(ctx, section).Result()
		if infoErr != nil {
			if errors.Is(mapError(infoErr), ErrSignInRejected) {
				return infoOutput{}, c.fail(infoErr)
			}
			if out.Errors == nil {
				out.Errors = map[string]string{}
			}
			out.Errors[section] = infoErr.Error()
			continue
		}
		for name, values := range parseInfo(text) {
			out.Sections[name] = values
		}
	}
	keys, err := client.DBSize(ctx).Result()
	if err != nil {
		return infoOutput{}, c.fail(err)
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
	client, err := c.client(in.DB)
	if err != nil {
		return scanOutput{}, err
	}
	limit := int64(clamp(in.Limit, defaultScanLimit, maxScanLimit))
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		pattern = "*"
	}
	var cursor uint64
	if in.Cursor != "" {
		parsed, err := strconv.ParseUint(in.Cursor, 10, 64)
		if err != nil {
			return scanOutput{}, fmt.Errorf("invalid cursor %q: pass the cursor a previous scan_keys returned", in.Cursor)
		}
		cursor = parsed
	}

	var keys []string
	for round := 0; round < scanRounds && int64(len(keys)) < limit; round++ {
		var batch []string
		var err error
		if in.Type != "" {
			batch, cursor, err = client.ScanType(ctx, cursor, pattern, limit, in.Type).Result()
		} else {
			batch, cursor, err = client.Scan(ctx, cursor, pattern, limit).Result()
		}
		if err != nil {
			return scanOutput{}, c.fail(err)
		}
		keys = append(keys, batch...)
		if cursor == 0 {
			break
		}
	}

	out := scanOutput{Cursor: strconv.FormatUint(cursor, 10), Keys: make([]keyInfo, 0, len(keys))}
	if c.clusterMode {
		out.Note = "cluster mode: only the keys of the shard reached through the tunnel are scanned"
	}
	if len(keys) == 0 {
		return out, nil
	}
	pipe := client.Pipeline()
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
	return out, nil
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
	client, err := c.client(in.DB)
	if err != nil {
		return keyValue{}, err
	}
	key := in.Key
	limit := clamp(in.MaxItems, defaultMaxItems, maxItemsLimit)

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

func (c *cache) readString(ctx context.Context, client *redis.Client, out *keyValue) error {
	length, err := client.StrLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	value, err := client.GetRange(ctx, out.Key, 0, maxStringBytes-1).Result()
	if err != nil {
		return err
	}
	out.Value, out.Length, out.Truncated = value, length, length > maxStringBytes
	return nil
}

func (c *cache) readHash(ctx context.Context, client *redis.Client, out *keyValue, limit int) error {
	length, err := client.HLen(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	fields := map[string]string{}
	budget := newReplyBudget()
	var cursor uint64
	cut, full := false, false
	for !full {
		pairs, next, err := client.HScan(ctx, out.Key, cursor, "*", int64(limit)).Result()
		if err != nil {
			return err
		}
		for idx := 0; idx+1 < len(pairs) && len(fields) < limit; idx += 2 {
			value, valueCut := cutString(pairs[idx+1])
			if !budget.fits(pairs[idx], value) {
				full = true
				break
			}
			fields[pairs[idx]], cut = value, cut || valueCut
		}
		if next == 0 || len(fields) >= limit {
			break
		}
		cursor = next
	}
	out.Value, out.Length, out.Truncated = fields, length, cut || full || length > int64(len(fields))
	return nil
}

func (c *cache) readList(ctx context.Context, client *redis.Client, out *keyValue, limit int) error {
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

func (c *cache) readSet(ctx context.Context, client *redis.Client, out *keyValue, limit int) error {
	length, err := client.SCard(ctx, out.Key).Result()
	if err != nil {
		return err
	}
	members := make([]string, 0)
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
			value, valueCut := cutString(member)
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

func (c *cache) readSortedSet(ctx context.Context, client *redis.Client, out *keyValue, limit int) error {
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
		entry := scoredMember{Member: member, Score: z.Score}
		if !budget.fits(entry) {
			cut = true
			break
		}
		members, cut = append(members, entry), cut || memberCut
	}
	out.Value, out.Length, out.Truncated = members, length, cut || length > int64(len(members))
	return nil
}

func (c *cache) readStream(ctx context.Context, client *redis.Client, out *keyValue, limit int) error {
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
				value, fieldCut = cutString(text)
				cut = cut || fieldCut
			}
			entry.Fields[field] = value
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
	client, err := c.client(in.DB)
	if err != nil {
		return commandOutput{}, 0, err
	}
	args := make([]any, len(in.Args))
	for idx, arg := range in.Args {
		args[idx] = arg
	}
	reply, err := client.Do(ctx, args...).Result()
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

// jsonReply converts a RESP reply to JSON values: maps get string keys.
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
			out[fmt.Sprint(key)] = jsonReply(item)
		}
		return out
	case error:
		return v.Error()
	default:
		return v
	}
}

// clamp is requested, else fallback when unset, at most limit.
func clamp(requested, fallback, limit int) int {
	switch {
	case requested <= 0:
		return fallback
	case requested > limit:
		return limit
	default:
		return requested
	}
}
