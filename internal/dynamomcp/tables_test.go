package dynamomcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func TestListTablesPages(t *testing.T) {
	f := &fakeAPI{tables: []string{"orders"}}
	out, err := newTables(f).List(context.Background(), &listTablesInput{Start: "customers"})
	if err != nil || len(out.Tables) != 1 || aws.ToInt32(f.listIn.Limit) != tablesPage || aws.ToString(f.listIn.ExclusiveStartTableName) != "customers" {
		t.Fatalf("out = %+v, in = %+v, err = %v", out, f.listIn, err)
	}
}

func TestDescribeTableIsCachedAndSummarized(t *testing.T) {
	f := &fakeAPI{table: ordersTable(), ttl: "expires_at"}
	tb := newTables(f)
	out, err := tb.Describe(context.Background(), &describeInput{Table: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Key != (keyOutput{Partition: "pk", Sort: "sk"}) || out.BillingMode != "PAY_PER_REQUEST" || out.TTLAttribute != "expires_at" ||
		len(out.Indexes) != 1 || out.Indexes[0].Name != "by-email" || out.Indexes[0].Kind != "global" || out.Attributes["email"] != "S" || out.ItemCount != 42 {
		t.Fatalf("out = %+v", out)
	}
	if _, err := tb.Describe(context.Background(), &describeInput{Table: "orders"}); err != nil || f.describes != 1 {
		t.Fatalf("describe not cached: %d calls (%v)", f.describes, err)
	}
}

func TestQueryWithoutNamesNorValuesSendsNil(t *testing.T) {
	f := &fakeAPI{}
	if _, err := newTables(f).Query(context.Background(), &queryInput{Table: "orders", KeyCondition: "pk = :pk", Values: object{":pk": "customer#1"}}); err != nil {
		t.Fatal(err)
	}
	if f.queryIn.ExpressionAttributeNames != nil || aws.ToInt32(f.queryIn.Limit) != defaultLimit {
		t.Fatalf("query = %+v", f.queryIn)
	}
	f = &fakeAPI{}
	if _, err := newTables(f).Scan(context.Background(), &scanInput{Table: "orders"}); err != nil {
		t.Fatal(err)
	}
	if f.scanIn.ExpressionAttributeNames != nil || f.scanIn.ExpressionAttributeValues != nil {
		t.Fatalf("scan = %+v", f.scanIn)
	}
}

func TestLimitIsCapped(t *testing.T) {
	f := &fakeAPI{}
	if _, err := newTables(f).Scan(context.Background(), &scanInput{Table: "orders", Limit: 10000}); err != nil {
		t.Fatal(err)
	}
	if aws.ToInt32(f.scanIn.Limit) != maxLimit {
		t.Fatalf("limit = %d", aws.ToInt32(f.scanIn.Limit))
	}
}

func TestQueryReturnsDynamoNextKeyWhenNothingIsCut(t *testing.T) {
	f := &fakeAPI{items: []map[string]types.AttributeValue{order(0, 10)}, lastKey: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "customer#1"}, "sk": &types.AttributeValueMemberS{Value: "order#a"},
	}}
	out, err := newTables(f).Query(context.Background(), &queryInput{Table: "orders", KeyCondition: "pk = :pk", Values: object{":pk": "customer#1"}})
	if err != nil || out.Truncated || out.Count != 1 || out.NextKey["sk"] != "order#a" {
		t.Fatalf("out = %+v, %v", out, err)
	}
}

func TestTruncatedGSIQueryRebuildsNextKeyFromLastKeptItem(t *testing.T) {
	items := []map[string]types.AttributeValue{order(0, 100<<10), order(1, 100<<10), order(2, 100<<10)}
	f := &fakeAPI{table: ordersTable(), items: items}
	out, err := newTables(f).Query(context.Background(), &queryInput{Table: "orders", Index: "by-email", KeyCondition: "email = :e", Values: object{":e": "a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Items) != 2 || out.Count != 2 {
		t.Fatalf("truncated = %v, items = %d, count = %d", out.Truncated, len(out.Items), out.Count)
	}
	want := map[string]any{"pk": "customer#1", "sk": "order#b", "email": "a@example.com"}
	if len(out.NextKey) != len(want) {
		t.Fatalf("next_key = %v, want %v", out.NextKey, want)
	}
	for k, v := range want {
		if out.NextKey[k] != v {
			t.Fatalf("next_key = %v, want %v", out.NextKey, want)
		}
	}
}

func TestOversizedSingleItemIsShrunk(t *testing.T) {
	f := &fakeAPI{table: ordersTable(), items: []map[string]types.AttributeValue{order(0, 300<<10)}}
	out, err := newTables(f).Scan(context.Background(), &scanInput{Table: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out.Items) //nolint:errcheck // plain values.
	if !out.Truncated || len(out.Items) != 1 || out.Items[0]["note"] != truncatedValue || out.Items[0]["pk"] != "customer#1" || len(raw) > maxResultBytes {
		t.Fatalf("truncated = %v, item = %.200s", out.Truncated, raw)
	}
}

func TestGetItemFoundAndMissing(t *testing.T) {
	f := &fakeAPI{item: order(0, 10)}
	out, err := newTables(f).Get(context.Background(), &getItemInput{Table: "orders", Key: object{"pk": "customer#1", "sk": "order#a"}, ConsistentRead: true})
	if err != nil || !out.Found || out.Item["email"] != "a@example.com" || !aws.ToBool(f.getIn.ConsistentRead) {
		t.Fatalf("out = %+v, %v", out, err)
	}
	f.item = nil
	if out, err = newTables(f).Get(context.Background(), &getItemInput{Table: "orders", Key: object{"pk": "x", "sk": "y"}}); err != nil || out.Found || out.Item != nil {
		t.Fatalf("missing = %+v, %v", out, err)
	}
}

func TestAWSErrorKeepsCodeAndMessage(t *testing.T) {
	err := mcpserve.AWSError(&smithy.GenericAPIError{Code: "ValidationException", Message: "Query condition missed key schema element: sk"})
	if err.Error() != "ValidationException: Query condition missed key schema element: sk" {
		t.Fatalf("err = %v", err)
	}
	credErr := &mcpserve.CredentialsError{Err: errors.New("the SSO session has expired")}
	chained := fmt.Errorf("operation error DynamoDB: Query, get identity: get credentials: %w", credErr)
	if got := mcpserve.AWSError(chained); got.Error() != credErr.Error() {
		t.Fatalf("err = %v", got)
	}
}

// noted is order n as DynamoDB returns it for the projection note and the key placeholders.
func noted(n int) map[string]types.AttributeValue {
	item := order(n, 100<<10)
	delete(item, "email")
	return item
}

func TestTruncatedProjectedQueryStillReturnsNextKey(t *testing.T) {
	f := &fakeAPI{table: ordersTable(), items: []map[string]types.AttributeValue{noted(0), noted(1), noted(2)}}
	given := map[string]string{"#n": "note"}
	out, err := newTables(f).Query(context.Background(), &queryInput{
		Table: "orders", KeyCondition: "pk = :pk", Values: object{":pk": "customer#1"}, Projection: "#n", Names: given,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.queryIn.ProjectionExpression) != "#n, #airlock_k0, #airlock_k1" || f.queryIn.ExpressionAttributeNames["#airlock_k0"] != "pk" ||
		f.queryIn.ExpressionAttributeNames["#airlock_k1"] != "sk" || f.queryIn.ExpressionAttributeNames["#n"] != "note" || len(given) != 1 {
		t.Fatalf("projection = %q, names = %v, given = %v", aws.ToString(f.queryIn.ProjectionExpression), f.queryIn.ExpressionAttributeNames, given)
	}
	if !out.Truncated || len(out.Items) != 2 || out.NextKey["pk"] != "customer#1" || out.NextKey["sk"] != "order#b" {
		t.Fatalf("truncated = %v, items = %d, next_key = %v", out.Truncated, len(out.Items), out.NextKey)
	}
}

func TestProjectionKeepsTheKeysItAlreadyNames(t *testing.T) {
	f := &fakeAPI{table: ordersTable()}
	if _, err := newTables(f).Scan(context.Background(), &scanInput{Table: "orders", Projection: "note, pk", Names: map[string]string{"#airlock_k0": "x"}}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.scanIn.ProjectionExpression) != "note, pk, #airlock_k1" || f.scanIn.ExpressionAttributeNames["#airlock_k1"] != "sk" {
		t.Fatalf("projection = %q, names = %v", aws.ToString(f.scanIn.ProjectionExpression), f.scanIn.ExpressionAttributeNames)
	}
}

func TestCutPageWithoutKeysIsAnError(t *testing.T) {
	note := func() map[string]types.AttributeValue {
		return map[string]types.AttributeValue{"note": order(0, 100<<10)["note"]}
	}
	f := &fakeAPI{table: ordersTable(), items: []map[string]types.AttributeValue{note(), note(), note()}}
	if _, err := newTables(f).Scan(context.Background(), &scanInput{Table: "orders"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v", err)
	}
}
