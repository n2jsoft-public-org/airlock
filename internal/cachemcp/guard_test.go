package cachemcp

import (
	"errors"
	"strings"
	"testing"
)

func TestEngineFor(t *testing.T) {
	cases := []struct{ in, want string }{{"redis", "redis"}, {"Valkey", "valkey"}, {" redis ", "redis"}}
	for _, tc := range cases {
		if got, ok := EngineFor(tc.in); !ok || got != tc.want {
			t.Errorf("EngineFor(%q) = %q, %v", tc.in, got, ok)
		}
	}
	for _, unsupported := range []string{"memcached", ""} {
		if _, ok := EngineFor(unsupported); ok {
			t.Errorf("EngineFor(%q) must not be supported", unsupported)
		}
	}
}

func TestCheckReadAllowsReadCommands(t *testing.T) {
	for _, args := range [][]string{
		{"GET", "user:1"}, {"hget", "user:1", "name"}, {"LRANGE", "q", "0", "9"}, {"ZSCORE", "z", "m"}, {"XINFO", "STREAM", "s"},
		{"OBJECT", "ENCODING", "k"}, {"memory", "usage", "k"}, {"SLOWLOG", "GET", "10"}, {"CLIENT", "LIST"}, {"PING"}, {"DBSIZE"},
		{"SRANDMEMBER", "s"}, {"SRANDMEMBER", "s", "1000"}, {"HRANDFIELD", "h", "5", "WITHVALUES"}, {"LRANGE", "q", "-100", "-1"},
		{"LRANGE", "q", "0", "999"}, {"ZRANGE", "z", "0", "9", "REV", "WITHSCORES"}, {"ZREVRANGE", "z", "0", "9"},
		{"ZRANGE", "z", "-inf", "+inf", "BYSCORE", "LIMIT", "0", "10"}, {"ZRANGEBYSCORE", "z", "0", "5", "WITHSCORES", "LIMIT", "100", "10"},
		{"XRANGE", "s", "-", "+", "COUNT", "10"}, {"xrevrange", "s", "+", "-", "count", "10"}, {"LPOS", "q", "x"}, {"LPOS", "q", "x", "COUNT", "5"},
		{"XINFO", "STREAM", "s", "FULL", "COUNT", "10"},
	} {
		if err := CheckRead(args); err != nil {
			t.Errorf("CheckRead(%v) = %v, want allowed", args, err)
		}
	}
}

func TestCheckReadRefusesEverythingElse(t *testing.T) {
	for _, args := range [][]string{
		{"SET", "k", "v"}, {"DEL", "k"}, {"KEYS", "*"}, {"FLUSHALL"}, {"EVAL", "return 1", "0"}, {"CONFIG", "GET", "*"},
		{"SLOWLOG", "RESET"}, {"CLIENT", "KILL", "ID", "1"}, {"OBJECT", "HELP"}, {"HGETALL", "h"}, {"HKEYS", "h"},
		// Reads that are not bounded by maxItemsLimit.
		{"SRANDMEMBER", "s", "-1000000000"}, {"SRANDMEMBER", "s", "1001"}, {"HRANDFIELD", "h", "-1000000000", "WITHVALUES"},
		{"LRANGE", "q", "0", "-1"}, {"LRANGE", "q", "0", "1000"}, {"LRANGE", "q", "-1001", "-1"}, {"LRANGE", "q", "a", "b"},
		{"ZRANGE", "z", "0", "-1"}, {"ZREVRANGE", "z", "0", "-1"}, {"ZRANGE", "z", "-inf", "+inf", "BYSCORE"},
		{"ZRANGE", "z", "-", "+", "BYLEX", "LIMIT", "0", "-1"}, {"ZRANGEBYSCORE", "z", "-inf", "+inf"},
		{"ZRANGEBYSCORE", "z", "-inf", "+inf", "LIMIT", "0", "10", "LIMIT", "0", "-1"}, {"XRANGE", "s", "-", "+"},
		{"XREVRANGE", "s", "+", "-", "COUNT", "100000"}, {"LPOS", "q", "x", "COUNT", "0"}, {"XINFO", "STREAM", "s", "FULL", "COUNT", "0"},
	} {
		if err := CheckRead(args); !errors.Is(err, ErrReadOnlyGuard) {
			t.Errorf("CheckRead(%v) = %v, want ErrReadOnlyGuard", args, err)
		}
	}
	if err := CheckRead(nil); err == nil || errors.Is(err, ErrReadOnlyGuard) {
		t.Errorf("CheckRead(nil) = %v, want a missing command error", err)
	}
}

func TestCheckWriteAllowsDataCommands(t *testing.T) {
	for _, args := range [][]string{{"SET", "k", "v"}, {"DEL", "k"}, {"HSET", "h", "f", "v"}, {"EXPIRE", "k", "60"}, {"XREAD", "COUNT", "5", "STREAMS", "s", "0"}, {"GET", "k"}} {
		if err := CheckWrite(args); err != nil {
			t.Errorf("CheckWrite(%v) = %v, want allowed", args, err)
		}
	}
}

func TestCheckWriteRefusesTheDenylist(t *testing.T) {
	for _, args := range [][]string{
		{"FLUSHALL"}, {"flushdb", "ASYNC"}, {"SHUTDOWN"}, {"KEYS", "*"}, {"CONFIG", "SET", "maxmemory", "1"}, {"DEBUG", "SLEEP", "1"},
		{"MONITOR"}, {"SUBSCRIBE", "c"}, {"BLPOP", "q", "0"}, {"EVAL", "return 1", "0"}, {"FCALL", "f", "0"}, {"SCRIPT", "FLUSH"},
		{"SELECT", "1"}, {"AUTH", "x"}, {"MULTI"}, {"CLIENT", "KILL", "ID", "1"}, {"CLIENT", "PAUSE", "1000"}, {"CLIENT", "SETNAME", "x"}, {"CLIENT", "TRACKING", "on"}, {"CLIENT", "CACHING", "yes"}, {"CLIENT", "NO-TOUCH", "on"}, {"CLIENT", "SETINFO", "LIB-NAME", "x"}, {"CLIENT", "UNBLOCK", "1"}, {"ACL", "SETUSER", "x"},
		{"SYNC"}, {"PSYNC", "?", "-1"}, {"REPLCONF", "listening-port", "1"}, {"MODULE", "LOAD", "x.so"},
		{"READONLY"}, {"readwrite"}, {"ASKING"},
		{"XREAD", "BLOCK", "0", "STREAMS", "s", "$"}, {"XREADGROUP", "GROUP", "g", "c", "block", "0", "STREAMS", "s", ">"},
	} {
		if err := CheckWrite(args); err == nil {
			t.Errorf("CheckWrite(%v) allowed, want refused", args)
		}
	}
}

func TestCommandLineQuotesAndCuts(t *testing.T) {
	if got := commandLine([]string{"SET", "greeting", "hello world", ""}); got != `SET greeting "hello world" ""` {
		t.Fatalf("commandLine = %s", got)
	}
	long := commandLine([]string{"SET", "k", strings.Repeat("é", maxCommandLine)})
	if len(long) > maxCommandLine+len("…") || !strings.HasSuffix(long, "…") || !strings.HasPrefix(long, "SET k ") {
		t.Fatalf("long command line = %d bytes", len(long))
	}
}

func TestCommandLineRedactsSecrets(t *testing.T) {
	cases := []struct {
		want string
		args []string
	}{
		{"AUTH <redacted>", []string{"AUTH", "s3cret"}},
		{"auth <redacted>", []string{"auth", "app", "s3cret"}},
		{"HELLO <redacted>", []string{"HELLO", "3", "AUTH", "app", "s3cret"}},
		{"HELLO <redacted>", []string{"HELLO", "3", "auth", "app", "s3cret", "SETNAME", "x"}},
		{"ACL SETUSER <redacted>", []string{"ACL", "SETUSER", "app", "on", ">s3cret"}},
		{"acl deluser <redacted>", []string{"acl", "deluser", "app"}},
		{"CONFIG SET <redacted>", []string{"CONFIG", "SET", "requirepass", "s3cret"}},
		{"config set <redacted>", []string{"config", "set", "maxmemory", "1gb", "MASTERAUTH", "s3cret"}},
		// Nothing secret: the line is kept.
		{"HELLO 3", []string{"HELLO", "3"}},
		{"ACL WHOAMI", []string{"ACL", "WHOAMI"}},
		{"CONFIG SET maxmemory 1gb", []string{"CONFIG", "SET", "maxmemory", "1gb"}},
		{"CONFIG GET requirepass", []string{"CONFIG", "GET", "requirepass"}},
		{"GET AUTH", []string{"GET", "AUTH"}},
	}
	for _, tc := range cases {
		if got := commandLine(tc.args); got != tc.want {
			t.Errorf("commandLine(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
