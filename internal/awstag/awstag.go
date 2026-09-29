// Package awstag holds an AWS resource tag written as Key=Value.
package awstag

import (
	"fmt"
	"strings"
)

// Tag is an AWS resource tag; the zero Tag means "not set".
type Tag struct {
	Key   string
	Value string
}

// Parse reads "Key=Value" (spaces around both sides are trimmed). A blank string is the zero Tag;
// a missing "=" or an empty key or value is an error.
func Parse(s string) (Tag, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Tag{}, nil
	}
	key, value, ok := strings.Cut(s, "=")
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if !ok || key == "" || value == "" {
		return Tag{}, fmt.Errorf("invalid tag %q: expected Key=Value", s)
	}
	return Tag{Key: key, Value: value}, nil
}

// String is "Key=Value", or "" for the zero Tag.
func (t Tag) String() string {
	if t.IsZero() {
		return ""
	}
	return t.Key + "=" + t.Value
}

// IsZero reports whether the tag is not set.
func (t Tag) IsZero() bool {
	return t.Key == "" && t.Value == ""
}
