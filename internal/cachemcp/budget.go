package cachemcp

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"unicode/utf8"
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

// base64Prefix starts a map key that is not valid UTF-8, given in base64 (see jsonKey).
const base64Prefix = "base64:"

// jsonString is s as a tool returns a value: s itself when it is text (valid UTF-8), else
// {"base64": <s in standard base64>}, as JSON would replace its invalid bytes.
func jsonString(s string) any {
	if utf8.ValidString(s) {
		return s
	}
	return map[string]string{"base64": base64.StdEncoding.EncodeToString([]byte(s))}
}

// jsonKey is s as a tool returns a map key (hash or stream field): s itself when it is text, else
// base64Prefix followed by s in standard base64.
func jsonKey(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return base64Prefix + base64.StdEncoding.EncodeToString([]byte(s))
}

// cutString cuts s at maxStringBytes and reports whether it was cut.
func cutString(s string) (string, bool) {
	if len(s) > maxStringBytes {
		return trimCutRune(s[:maxStringBytes]), true
	}
	return s, false
}

// trimCutRune drops the last bytes of s when they are the start of a character cut off, so cut
// text stays text rather than turn into base64 (see jsonString).
func trimCutRune(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	for n := 1; n < utf8.UTFMax && n < len(s); n++ {
		if utf8.ValidString(s[:len(s)-n]) {
			return s[:len(s)-n]
		}
	}
	return s
}

// fitStrings cuts every item at maxStringBytes and keeps the items fitting b, as jsonString
// returns them; truncated reports a cut or a dropped item.
func fitStrings(items []string, b *replyBudget) (kept []any, truncated bool) {
	kept = make([]any, 0, len(items))
	for _, item := range items {
		s, cut := cutString(item)
		value := jsonString(s)
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
		value := jsonString(s)
		if !b.fits(value) {
			return nil, true, false
		}
		return value, cut, true
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
