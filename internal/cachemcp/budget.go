package cachemcp

import (
	"encoding/json"
	"sort"
)

// replyBudget counts the JSON bytes of a get_key value or a command reply against maxResultBytes.
// Once an item does not fit, it refuses every other one: items stop being added.
type replyBudget struct {
	left int
}

func newReplyBudget() *replyBudget {
	return &replyBudget{left: maxResultBytes}
}

// fits charges the JSON of values, plus a separator, and reports whether they fit.
func (b *replyBudget) fits(values ...any) bool {
	size := 1
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			b.left = 0
			return false
		}
		size += len(raw)
	}
	if size > b.left {
		b.left = 0
		return false
	}
	b.left -= size
	return true
}

// cutString cuts s at maxStringBytes and reports whether it was cut.
func cutString(s string) (string, bool) {
	if len(s) > maxStringBytes {
		return s[:maxStringBytes], true
	}
	return s, false
}

// fitStrings cuts every item at maxStringBytes and keeps the items fitting b; truncated reports
// a cut or a dropped item.
func fitStrings(items []string, b *replyBudget) (kept []string, truncated bool) {
	kept = make([]string, 0, len(items))
	for _, item := range items {
		value, cut := cutString(item)
		if !b.fits(value) {
			return kept, true
		}
		kept = append(kept, value)
		truncated = truncated || cut
	}
	return kept, truncated
}

// capReply cuts every string at maxStringBytes and every array at maxItemsLimit items, stops
// adding items once the reply reaches maxResultBytes of JSON, and reports whether anything was cut.
func capReply(value any) (result any, truncated bool) {
	result, truncated, _ = capValue(value, newReplyBudget())
	return result, truncated
}

// capValue is capReply charging b; fits is false when value itself does not fit b.
func capValue(value any, b *replyBudget) (result any, truncated, fits bool) {
	switch v := value.(type) {
	case string:
		s, cut := cutString(v)
		if !b.fits(s) {
			return nil, true, false
		}
		return s, cut, true
	case []any:
		if len(v) > maxItemsLimit {
			v, truncated = v[:maxItemsLimit], true
		}
		out := make([]any, 0, len(v))
		for _, item := range v {
			capped, cut, ok := capValue(item, b)
			if !ok {
				return out, true, true
			}
			out = append(out, capped)
			truncated = truncated || cut
		}
		return out, truncated, true
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		// Sorted, so a cut reply always keeps the same entries.
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		for _, key := range keys {
			if !b.fits(key) {
				return out, true, true
			}
			capped, cut, ok := capValue(v[key], b)
			if !ok {
				return out, true, true
			}
			out[key] = capped
			truncated = truncated || cut
		}
		return out, truncated, true
	default:
		if !b.fits(v) {
			return nil, true, false
		}
		return v, false, true
	}
}
