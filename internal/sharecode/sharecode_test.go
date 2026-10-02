package sharecode

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testPrefix = "airlocktest1:"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	data := []byte(`{"version":1,"key":"value"}`)
	shared := Encode(testPrefix, data)
	if !strings.HasPrefix(shared, testPrefix) || strings.ContainsAny(shared, " \n{}\"") {
		t.Fatalf("shared = %q, want one opaque %s string", shared, testPrefix)
	}
	decoded, err := Decode(testPrefix, shared)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("decoded = %q, want %q", decoded, data)
	}
}

func TestDecodeTrimsSurroundingSpaces(t *testing.T) {
	shared := Encode(testPrefix, []byte("payload"))
	decoded, err := Decode(testPrefix, "  "+shared+"\n")
	if err != nil || string(decoded) != "payload" {
		t.Fatalf("decoded = %q, %v; surrounding spaces from a paste must be accepted", decoded, err)
	}
}

func TestDecodeRejectsDamagedStrings(t *testing.T) {
	shared := Encode(testPrefix, []byte(`{"defaultRegion":"eu-west-3"}`))
	altered := []byte(shared)
	last := len(altered) - 1
	if altered[last] == 'A' {
		altered[last] = 'B'
	} else {
		altered[last] = 'A'
	}
	for name, s := range map[string]string{
		"truncated":    shared[:len(shared)-4],
		"altered":      string(altered),
		"wrong prefix": "airlocktest2:" + strings.TrimPrefix(shared, testPrefix),
		"not base64":   testPrefix + "!!!",
		"empty":        testPrefix,
		"not a string": "hello",
	} {
		if _, err := Decode(testPrefix, s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
