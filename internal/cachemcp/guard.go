package cachemcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxCommandLine caps the command line recorded in the audit log.
const maxCommandLine = 1 << 10

// ErrReadOnlyGuard reports a command the read-only command tool refuses.
var ErrReadOnlyGuard = errors.New("refused by the read-only guard: use info, scan_keys, get_key or a read command")

// readCommands run as they are; readSubcommands only with one of their read subcommands.
var (
	readCommands = setOf(
		"GET", "MGET", "GETRANGE", "STRLEN", "EXISTS", "TYPE", "TTL", "PTTL", "EXPIRETIME",
		"HGET", "HMGET", "HLEN", "HEXISTS", "HSTRLEN", "HRANDFIELD",
		"LLEN", "LINDEX", "LRANGE", "LPOS",
		"SCARD", "SISMEMBER", "SMISMEMBER", "SRANDMEMBER",
		"ZCARD", "ZSCORE", "ZMSCORE", "ZRANK", "ZREVRANK", "ZCOUNT", "ZRANGE", "ZRANGEBYSCORE", "ZREVRANGE",
		"XLEN", "XRANGE", "XREVRANGE", "XINFO", "PFCOUNT", "BITCOUNT", "GETBIT", "DBSIZE", "PING", "TIME",
	)
	readSubcommands = map[string]map[string]bool{
		"OBJECT":  setOf("ENCODING", "FREQ", "IDLETIME"),
		"MEMORY":  setOf("USAGE", "STATS"),
		"SLOWLOG": setOf("GET", "LEN"),
		"CLIENT":  setOf("LIST", "INFO"),
	}
)

// deniedCommands are refused even with writes allowed: they wipe or stop the cache, change its
// configuration or replication, block or keep the connection, change its state, or run scripts
// that could do any of it.
var (
	deniedCommands = setOf(
		"FLUSHALL", "FLUSHDB", "SHUTDOWN", "SWAPDB", "MIGRATE",
		"CONFIG", "DEBUG", "MONITOR", "KEYS", "SAVE", "BGSAVE", "BGREWRITEAOF", "REPLICAOF", "SLAVEOF", "FAILOVER", "CLUSTER", "ACL",
		"SYNC", "PSYNC", "REPLCONF", "MODULE",
		"SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "UNSUBSCRIBE", "PUNSUBSCRIBE", "SUNSUBSCRIBE",
		"BLPOP", "BRPOP", "BLMOVE", "BRPOPLPUSH", "BLMPOP", "BZPOPMIN", "BZPOPMAX", "BZMPOP", "WAIT", "WAITAOF",
		"EVAL", "EVALSHA", "EVAL_RO", "EVALSHA_RO", "FCALL", "FCALL_RO", "FUNCTION", "SCRIPT",
		"SELECT", "AUTH", "HELLO", "RESET", "QUIT", "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH",
		"READONLY", "READWRITE", "ASKING",
	)
	deniedSubcommands = map[string]map[string]bool{
		"CLIENT": setOf("KILL", "PAUSE", "NO-EVICT", "REPLY", "SETNAME", "TRACKING", "CACHING", "NO-TOUCH", "SETINFO", "UNBLOCK"),
	}
)

func setOf(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// commandName is the upper-case command of args and its first argument (its subcommand, if any).
func commandName(args []string) (name, sub string, err error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", "", errors.New("no command given: args starts with the command, e.g. [\"GET\", \"key\"]")
	}
	name = strings.ToUpper(strings.TrimSpace(args[0]))
	if len(args) > 1 {
		sub = strings.ToUpper(strings.TrimSpace(args[1]))
	}
	return name, sub, nil
}

// CheckRead accepts only the read commands of the allowlist.
func CheckRead(args []string) error {
	name, sub, err := commandName(args)
	if err != nil {
		return err
	}
	if allowed, ok := readSubcommands[name]; ok {
		if allowed[sub] {
			return nil
		}
		return fmt.Errorf("%w (%s %s is not a read command)", ErrReadOnlyGuard, name, sub)
	}
	if readCommands[name] {
		return checkBounded(name, args)
	}
	return fmt.Errorf("%w (%s is not a read command)", ErrReadOnlyGuard, name)
}

// checkBounded refuses the read commands of args that could read more than maxItemsLimit items:
// the server would build the whole reply before capReply cuts it. A negative SRANDMEMBER or
// HRANDFIELD count returns that many items with repetitions, a 0 -1 range the whole key, and
// XRANGE without COUNT or ZRANGEBYSCORE without LIMIT every match. get_key reads whole keys.
func checkBounded(name string, args []string) error {
	switch name {
	case "SRANDMEMBER", "HRANDFIELD":
		if len(args) > 2 {
			return boundedCount(name, "count", args[2])
		}
	case "LRANGE", "ZREVRANGE":
		return boundedRange(name, args)
	case "ZRANGE":
		if !hasArg(args[min(len(args), 4):], "BYSCORE") && !hasArg(args[min(len(args), 4):], "BYLEX") {
			return boundedRange(name, args)
		}
		return boundedOption(name, args, 4, "LIMIT", 2, true)
	case "ZRANGEBYSCORE":
		return boundedOption(name, args, 4, "LIMIT", 2, true)
	case "XRANGE", "XREVRANGE":
		return boundedOption(name, args, 4, "COUNT", 1, true)
	case "LPOS", "XINFO":
		return boundedOption(name, args, 3, "COUNT", 1, false)
	}
	return nil
}

// boundedCount refuses a count outside 1..maxItemsLimit: COUNT 0 reads every match on LPOS and
// XINFO STREAM FULL.
func boundedCount(name, what, value string) error {
	if count, err := strconv.ParseInt(value, 10, 64); err != nil || count < 1 || count > maxItemsLimit {
		return fmt.Errorf("%w (%s %s must be between 1 and %d)", ErrReadOnlyGuard, name, what, maxItemsLimit)
	}
	return nil
}

// boundedRange refuses an index range (args[2], args[3]) that may hold more than maxItemsLimit
// items: both ends must be on the same side of the key (0 99, -100 -1).
func boundedRange(name string, args []string) error {
	if len(args) < 4 {
		return nil
	}
	start, startErr := strconv.ParseInt(args[2], 10, 64)
	stop, stopErr := strconv.ParseInt(args[3], 10, 64)
	if startErr != nil || stopErr != nil || (start < 0) != (stop < 0) || stop-start >= maxItemsLimit {
		return fmt.Errorf("%w (%s needs a range of at most %d items with both ends on the same side, e.g. 0 99 or -100 -1)",
			ErrReadOnlyGuard, name, maxItemsLimit)
	}
	return nil
}

// boundedOption checks every option opt in args[from:]: its count, the offset-th argument after
// it (LIMIT offset count, COUNT count), must be bounded, and with required it must be given.
func boundedOption(name string, args []string, from int, opt string, offset int, required bool) error {
	found := false
	for idx := from; idx < len(args); idx++ {
		if !strings.EqualFold(strings.TrimSpace(args[idx]), opt) {
			continue
		}
		found = true
		value := ""
		if idx+offset < len(args) {
			value = args[idx+offset]
		}
		if err := boundedCount(name, opt, value); err != nil {
			return err
		}
	}
	if required && !found {
		return fmt.Errorf("%w (%s needs %s with a count of at most %d)", ErrReadOnlyGuard, name, opt, maxItemsLimit)
	}
	return nil
}

// CheckWrite accepts every command but the denylist.
func CheckWrite(args []string) error {
	name, sub, err := commandName(args)
	if err != nil {
		return err
	}
	if deniedCommands[name] || deniedSubcommands[name][sub] {
		return fmt.Errorf("%s is refused through MCP, even with writes allowed", strings.TrimSpace(name+" "+sub))
	}
	if name == "XREAD" || name == "XREADGROUP" {
		for _, arg := range args[1:] {
			if strings.EqualFold(arg, "BLOCK") {
				return fmt.Errorf("%s BLOCK is refused through MCP: it would hold the connection", name)
			}
		}
	}
	return nil
}

// redactedArg replaces the arguments of a command that may carry a password in the audit log.
const redactedArg = "<redacted>"

// commandLine is args as one line for the audit log: arguments quoted when they hold spaces,
// quotes or nothing, secrets redacted (see redactSecrets), cut at maxCommandLine bytes.
func commandLine(args []string) string {
	args = redactSecrets(args)
	parts := make([]string, len(args))
	for idx, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n\"'\\") {
			arg = strconv.Quote(arg)
		}
		parts[idx] = arg
	}
	line := strings.Join(parts, " ")
	if len(line) > maxCommandLine {
		line = strings.ToValidUTF8(line[:maxCommandLine], "") + "…"
	}
	return line
}

// redactSecrets keeps the command (and subcommand) of args and replaces the arguments that may
// carry a password with redactedArg: AUTH, HELLO with AUTH, ACL, and CONFIG SET of requirepass or
// masterauth. Refused commands are audited too, so they never reach the log with their secrets.
func redactSecrets(args []string) []string {
	name, sub, err := commandName(args)
	if err != nil {
		return args
	}
	keep := 0
	switch {
	case name == "AUTH":
		keep = 1
	case name == "HELLO" && hasArg(args[1:], "AUTH"):
		keep = 1
	case name == "ACL":
		keep = 2
	case name == "CONFIG" && sub == "SET" && (hasArg(args[2:], "requirepass") || hasArg(args[2:], "masterauth")):
		keep = 2
	}
	if keep == 0 || len(args) <= keep {
		return args
	}
	return append(append([]string{}, args[:keep]...), redactedArg)
}

// hasArg reports whether args holds want, ignoring case.
func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), want) {
			return true
		}
	}
	return false
}
