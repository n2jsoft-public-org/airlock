package dynamomcp

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestObjectKeepsNumbersExact(t *testing.T) {
	var o object
	if err := json.Unmarshal([]byte(`{"id": 12345678901234567890, "price": 0.1}`), &o); err != nil {
		t.Fatal(err)
	}
	av, err := attributes(o)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := av["id"].(*types.AttributeValueMemberN); !ok || n.Value != "12345678901234567890" {
		t.Fatalf("id = %#v", av["id"])
	}
	if n, ok := av["price"].(*types.AttributeValueMemberN); !ok || n.Value != "0.1" {
		t.Fatalf("price = %#v", av["price"])
	}
}

func TestAttributesOfNothingIsNil(t *testing.T) {
	if av, err := attributes(nil); err != nil || av != nil {
		t.Fatalf("attributes(nil) = %v, %v", av, err)
	}
	if av, err := attributes(object{}); err != nil || av != nil {
		t.Fatalf("attributes({}) = %v, %v", av, err)
	}
}

func TestPlainItem(t *testing.T) {
	item := map[string]types.AttributeValue{
		"pk":    &types.AttributeValueMemberS{Value: "user#1"},
		"big":   &types.AttributeValueMemberN{Value: "12345678901234567890"},
		"ok":    &types.AttributeValueMemberBOOL{Value: true},
		"none":  &types.AttributeValueMemberNULL{Value: true},
		"tags":  &types.AttributeValueMemberSS{Value: []string{"a", "b"}},
		"nums":  &types.AttributeValueMemberNS{Value: []string{"1", "2.5"}},
		"blob":  &types.AttributeValueMemberB{Value: []byte("hi")},
		"inner": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"n": &types.AttributeValueMemberN{Value: "7"}}},
		"list":  &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberN{Value: "3"}}},
	}
	plain, err := plainItem(item)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"big":12345678901234567890,"blob":"aGk=","inner":{"n":7},"list":[3],"none":null,"nums":[1,2.5],"ok":true,"pk":"user#1","tags":["a","b"]}`
	if string(raw) != want {
		t.Fatalf("plain = %s\nwant    %s", raw, want)
	}
}

func TestPlainItemOfNilIsNil(t *testing.T) {
	if plain, err := plainItem(nil); err != nil || plain != nil {
		t.Fatalf("plainItem(nil) = %v, %v", plain, err)
	}
}
