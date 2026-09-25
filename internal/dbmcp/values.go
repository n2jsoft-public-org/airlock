package dbmcp

import (
	"encoding/hex"
	"fmt"
	"time"
	"unicode/utf8"
)

// maxTextBytes caps a single text value returned to the AI assistant.
const maxTextBytes = 4096

// jsonValue converts a database/sql driver value into a JSON-friendly value.
func jsonValue(v any) any {
	switch x := v.(type) {
	case nil, bool, int64, float64:
		return x
	case string:
		return truncateText(x)
	case []byte:
		if utf8.Valid(x) {
			return truncateText(string(x))
		}
		return truncateText("0x" + hex.EncodeToString(x))
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return truncateText(fmt.Sprint(x))
	}
}

// truncateText cuts text longer than maxTextBytes on a rune boundary and says how much was dropped.
func truncateText(text string) string {
	if len(text) <= maxTextBytes {
		return text
	}
	cut := maxTextBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s…[truncated %d bytes]", text[:cut], len(text)-cut)
}
