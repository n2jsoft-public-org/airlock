package gateway

import (
	"strings"
	"testing"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		in   string
		want string
		max  int
	}{
		{in: "  check\x1b[2K\x1b[1A invoices\nnow\t ok \r", max: 500, want: "check[2K[1A invoices now ok"},
		{in: "a\x07b\u009bc\x7fd", max: 500, want: "abcd"},
		{in: "été déjà", max: 4, want: "été"},
		{in: "", max: 10, want: ""},
	}
	for _, tc := range cases {
		if got := cleanText(tc.in, tc.max); got != tc.want {
			t.Errorf("cleanText(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
	if got := cleanText(strings.Repeat("x", 600), maxReason); len([]rune(got)) != maxReason {
		t.Fatalf("len = %d", len([]rune(got)))
	}
}
