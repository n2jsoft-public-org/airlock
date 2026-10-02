// Package sharecode encodes the strings airlock prints for teammates to paste: a versioned prefix,
// then the base64url of a short checksum followed by the payload.
package sharecode

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// checksumSize is the length of the checksum heading the payload. It only detects strings damaged
// by a copy and paste: anyone can build a valid string, so an import always shows its changes
// before applying them.
const checksumSize = 4

// ErrInvalid refuses a string that is not an intact string of the expected prefix.
var ErrInvalid = errors.New("not a valid airlock string (damaged or incomplete)")

// Encode wraps data in a checksummed, prefixed base64url string.
func Encode(prefix string, data []byte) string {
	sum := sha256.Sum256(data)
	payload := append(sum[:checksumSize:checksumSize], data...)
	return prefix + base64.RawURLEncoding.EncodeToString(payload)
}

// Decode returns the data of a string of Encode with prefix; spaces around it (from a paste) are
// ignored. Any other damage is ErrInvalid.
func Decode(prefix, s string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(s), prefix)
	if !ok {
		return nil, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(payload) <= checksumSize {
		return nil, ErrInvalid
	}
	checksum, data := payload[:checksumSize], payload[checksumSize:]
	if sum := sha256.Sum256(data); !bytes.Equal(checksum, sum[:checksumSize]) {
		return nil, ErrInvalid
	}
	return data, nil
}
