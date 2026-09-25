package dbmcp

import (
	"strings"
	"testing"
	"time"
)

func TestJSONValue(t *testing.T) {
	ts := time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		in   any
		want any
		name string
	}{
		{name: "nil", in: nil, want: nil},
		{name: "int", in: int64(42), want: int64(42)},
		{name: "float", in: 1.5, want: 1.5},
		{name: "bool", in: true, want: true},
		{name: "string", in: "hello", want: "hello"},
		{name: "utf8 bytes", in: []byte("12.50"), want: "12.50"},
		{name: "binary bytes", in: []byte{0xff, 0x00}, want: "0xff00"},
		{name: "time", in: ts, want: "2026-09-24T10:30:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonValue(tc.in); got != tc.want {
				t.Fatalf("jsonValue(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateTextKeepsRunesWhole(t *testing.T) {
	text := strings.Repeat("é", maxTextBytes) // 2 bytes per rune
	got := truncateText(text)
	prefix, _, found := strings.Cut(got, "…[truncated ")
	if !found {
		t.Fatalf("missing truncation marker in %q", got[len(got)-40:])
	}
	if len(prefix) != maxTextBytes {
		t.Fatalf("prefix length = %d, want %d", len(prefix), maxTextBytes)
	}
	if !strings.HasSuffix(got, "4096 bytes]") {
		t.Fatalf("unexpected marker: %q", got[len(got)-30:])
	}
}
