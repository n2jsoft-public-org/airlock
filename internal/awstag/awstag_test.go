package awstag

import "testing"

func TestParse(t *testing.T) {
	tag, err := Parse(" AccessRole = ro-access ")
	if err != nil {
		t.Fatal(err)
	}
	if tag != (Tag{Key: "AccessRole", Value: "ro-access"}) {
		t.Fatalf("Parse = %+v", tag)
	}
	if tag.String() != "AccessRole=ro-access" || tag.IsZero() {
		t.Fatalf("String = %q IsZero = %v", tag.String(), tag.IsZero())
	}
}

func TestParseKeepsEqualsInValue(t *testing.T) {
	tag, err := Parse("Name=a=b")
	if err != nil || tag != (Tag{Key: "Name", Value: "a=b"}) {
		t.Fatalf("Parse = %+v, %v", tag, err)
	}
}

func TestParseEmptyIsZero(t *testing.T) {
	tag, err := Parse("  ")
	if err != nil || !tag.IsZero() || tag.String() != "" {
		t.Fatalf("Parse(blank) = %+v, %v", tag, err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, in := range []string{"Usage", "=jumphost", "Usage=", " = "} {
		if _, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) accepted a malformed tag", in)
		}
	}
}
