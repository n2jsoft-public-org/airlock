package mcpserve

// Limit is the count a tool returns: requested, else fallback when unset, at most limit.
func Limit(requested, fallback, limit int) int {
	if requested <= 0 {
		return fallback
	}
	return min(requested, limit)
}
