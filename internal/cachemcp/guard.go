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
		"HGET", "HMGET", "HLEN", "HEXISTS", "HKEYS", "HSTRLEN", "HRANDFIELD",
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
		return nil
	}
	return fmt.Errorf("%w (%s is not a read command)", ErrReadOnlyGuard, name)
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
