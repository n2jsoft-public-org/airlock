// Package cachemcp serves MCP servers giving AI assistants access to Redis OSS and Valkey caches
// reached through jumphost tunnels.
package cachemcp

import "strings"

// EngineFor maps an ElastiCache engine to a supported engine, redis or valkey; Memcached is not.
func EngineFor(engine string) (string, bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(engine)); normalized {
	case "redis", "valkey":
		return normalized, true
	default:
		return "", false
	}
}
