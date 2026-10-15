package dynamomcp

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeAPI answers with its fields and records the last request of each operation.
type fakeAPI struct {
	err       error
	table     *types.TableDescription
	item      map[string]types.AttributeValue
	lastKey   map[string]types.AttributeValue
	listIn    *dynamodb.ListTablesInput
	getIn     *dynamodb.GetItemInput
	queryIn   *dynamodb.QueryInput
	scanIn    *dynamodb.ScanInput
	putIn     *dynamodb.PutItemInput
	updateIn  *dynamodb.UpdateItemInput
	deleteIn  *dynamodb.DeleteItemInput
	ttl       string
	tables    []string
	items     []map[string]types.AttributeValue
	describes int
}

func (f *fakeAPI) ListTables(_ context.Context, in *dynamodb.ListTablesInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
	f.listIn = in
	return &dynamodb.ListTablesOutput{TableNames: f.tables}, f.err
}

func (f *fakeAPI) DescribeTable(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	f.describes++
	if f.table == nil {
		return nil, errors.New("no table")
	}
	return &dynamodb.DescribeTableOutput{Table: f.table}, f.err
}

func (f *fakeAPI) DescribeTimeToLive(_ context.Context, _ *dynamodb.DescribeTimeToLiveInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	if f.ttl == "" {
		return &dynamodb.DescribeTimeToLiveOutput{}, nil
	}
	return &dynamodb.DescribeTimeToLiveOutput{TimeToLiveDescription: &types.TimeToLiveDescription{
		AttributeName: aws.String(f.ttl), TimeToLiveStatus: types.TimeToLiveStatusEnabled,
	}}, nil
}

func (f *fakeAPI) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.getIn = in
	return &dynamodb.GetItemOutput{Item: f.item}, f.err
}

func (f *fakeAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.queryIn = in
	return &dynamodb.QueryOutput{Items: f.items, Count: int32(len(f.items)), ScannedCount: int32(len(f.items)), LastEvaluatedKey: f.lastKey}, f.err //nolint:gosec // a few items.
}

func (f *fakeAPI) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.scanIn = in
	return &dynamodb.ScanOutput{Items: f.items, Count: int32(len(f.items)), ScannedCount: int32(len(f.items)), LastEvaluatedKey: f.lastKey}, f.err //nolint:gosec // a few items.
}

func (f *fakeAPI) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putIn = in
	return &dynamodb.PutItemOutput{}, f.err
}

func (f *fakeAPI) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updateIn = in
	return &dynamodb.UpdateItemOutput{Attributes: f.item}, f.err
}

func (f *fakeAPI) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.deleteIn = in
	return &dynamodb.DeleteItemOutput{}, f.err
}

// ordersTable has key pk/sk and a GSI by-email keyed by email.
func ordersTable() *types.TableDescription {
	return &types.TableDescription{
		TableName:   aws.String("orders"),
		TableStatus: types.TableStatusActive,
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("email"), AttributeType: types.ScalarAttributeTypeS},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{{
			IndexName:  aws.String("by-email"),
			KeySchema:  []types.KeySchemaElement{{AttributeName: aws.String("email"), KeyType: types.KeyTypeHash}},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
		BillingModeSummary: &types.BillingModeSummary{BillingMode: types.BillingModePayPerRequest},
		ItemCount:          aws.Int64(42),
		TableSizeBytes:     aws.Int64(4096),
	}
}

// order is an item of ordersTable whose note attribute is size bytes long.
func order(n, size int) map[string]types.AttributeValue {
	note := make([]byte, size)
	for i := range note {
		note[i] = 'x'
	}
	return map[string]types.AttributeValue{
		"pk":    &types.AttributeValueMemberS{Value: "customer#1"},
		"sk":    &types.AttributeValueMemberS{Value: "order#" + string(rune('a'+n))}, //nolint:gosec // n is a small test index.
		"email": &types.AttributeValueMemberS{Value: "a@example.com"},
		"note":  &types.AttributeValueMemberS{Value: string(note)},
	}
}
