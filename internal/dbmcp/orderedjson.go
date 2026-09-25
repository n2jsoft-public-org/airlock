package dbmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// member is one key of a JSON object.
type member struct {
	Key   string
	Value json.RawMessage
}

// orderedObject is a JSON object that keeps its keys in file order, so rewriting a user's
// config file does not reshuffle it.
type orderedObject []member

func (o *orderedObject) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return errors.New("expected a JSON object")
	}
	*o = nil
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return keyErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("unexpected object key %v", keyToken)
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return decodeErr
		}
		*o = append(*o, member{Key: key, Value: value})
	}
	_, err = decoder.Token()
	return err
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(m.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(m.Value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (o orderedObject) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// set replaces key in place, or appends it.
func (o *orderedObject) set(key string, value json.RawMessage) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = value
			return
		}
	}
	*o = append(*o, member{Key: key, Value: value})
}

func (o *orderedObject) remove(key string) {
	kept := (*o)[:0]
	for _, m := range *o {
		if m.Key != key {
			kept = append(kept, m)
		}
	}
	*o = kept
}
