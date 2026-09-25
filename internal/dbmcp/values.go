package dbmcp

import (
	"encoding/hex"
	"fmt"
	"time"
	"unicode/utf8"

	mssql "github.com/microsoft/go-mssqldb"
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

// mssqlGUID renders a SQL Server uniqueidentifier's 16 raw wire-order bytes (as returned by
// go-mssqldb for a UNIQUEIDENTIFIER column) in the standard 8-4-4-4-12 form.
func mssqlGUID(raw []byte) string {
	var id mssql.UniqueIdentifier
	if err := id.Scan(raw); err != nil {
		// Only a wrong-length slice reaches here; the caller already checked len(raw) == 16,
		// so fall back to the same hex rendering used for other binary columns.
		return "0x" + hex.EncodeToString(raw)
	}
	return id.String()
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
