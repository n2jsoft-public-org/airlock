// Package dynamomcp serves the MCP server of a DynamoDB session: the tables of an AWS profile and
// region, read through the SDK, with single-item writes when allowed.
package dynamomcp

import (
	"bytes"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// object is a JSON object from an AI client: a key, an item or expression values. Its numbers
// decode as json.Number, so they reach DynamoDB exactly.
type object map[string]any

func (o *object) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		return err
	}
	*o = m
	return nil
}

// attributes converts o to DynamoDB attribute values; nil when o is empty, as DynamoDB rejects
// empty key, item and expression maps.
func attributes(o object) (map[string]types.AttributeValue, error) {
	if len(o) == 0 {
		return nil, nil
	}
	return attributevalue.MarshalMap(map[string]any(o))
}

// plainItem converts a DynamoDB item to plain JSON values: exact numbers, base64 binary, sets as
// arrays; nil for no item.
func plainItem(item map[string]types.AttributeValue) (map[string]any, error) {
	if item == nil {
		return nil, nil
	}
	var out map[string]any
	if err := attributevalue.UnmarshalMapWithOptions(item, &out, func(o *attributevalue.DecoderOptions) { o.UseNumber = true }); err != nil {
		return nil, err
	}
	for k, v := range out {
		out[k] = jsonNumbers(v)
	}
	return out, nil
}

// jsonNumbers turns the attributevalue.Number values of v into json.Number, which marshals unquoted.
func jsonNumbers(v any) any {
	switch t := v.(type) {
	case attributevalue.Number:
		return json.Number(t)
	case []attributevalue.Number:
		out := make([]any, len(t))
		for i, n := range t {
			out[i] = json.Number(n)
		}
		return out
	case map[string]any:
		for k, x := range t {
			t[k] = jsonNumbers(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = jsonNumbers(x)
		}
		return t
	default:
		return v
	}
}
