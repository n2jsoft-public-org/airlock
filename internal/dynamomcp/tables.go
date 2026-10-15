package dynamomcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// callTimeout bounds each tool call.
	callTimeout = 30 * time.Second
	// maxResultBytes caps the JSON of the items of a result: items stop being added past it.
	maxResultBytes = 256 << 10
	defaultLimit   = 50
	maxLimit       = 500
	// tablesPage is the number of table names list_tables returns per page.
	tablesPage = 100
	// truncatedValue replaces the largest values of an item too large for a result on its own.
	truncatedValue = "<truncated>"
)

// api is the part of the DynamoDB client the tools use; *dynamodb.Client implements it.
type api interface {
	ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

// tables runs the tool operations; the descriptions of the tables are cached for the session.
type tables struct {
	api       api
	described map[string]*types.TableDescription
	mu        sync.Mutex
}

func newTables(a api) *tables {
	return &tables{api: a, described: map[string]*types.TableDescription{}}
}

type listTablesInput struct {
	Start string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type listTablesOutput struct {
	Next   string   `json:"next,omitempty"`
	Tables []string `json:"tables"`
}

func (t *tables) List(ctx context.Context, in *listTablesInput) (listTablesOutput, error) {
	req := &dynamodb.ListTablesInput{Limit: aws.Int32(tablesPage)}
	if in.Start != "" {
		req.ExclusiveStartTableName = aws.String(in.Start)
	}
	out, err := t.api.ListTables(ctx, req)
	if err != nil {
		return listTablesOutput{Tables: []string{}}, mcpserve.AWSError(err)
	}
	res := listTablesOutput{Tables: out.TableNames, Next: aws.ToString(out.LastEvaluatedTableName)}
	if res.Tables == nil {
		res.Tables = []string{}
	}
	return res, nil
}

type describeInput struct {
	Table string `json:"table" jsonschema:"the table name"`
}

type keyOutput struct {
	Partition string `json:"partition"`
	Sort      string `json:"sort,omitempty"`
}

type indexOutput struct {
	Name             string    `json:"name"`
	Kind             string    `json:"kind" jsonschema:"global or local"`
	Projection       string    `json:"projection"`
	Key              keyOutput `json:"key"`
	NonKeyAttributes []string  `json:"non_key_attributes,omitempty"`
}

type describeOutput struct {
	Attributes   map[string]string `json:"attributes" jsonschema:"type (S, N or B) of each key attribute"`
	Table        string            `json:"table"`
	Status       string            `json:"status"`
	BillingMode  string            `json:"billing_mode"`
	TTLAttribute string            `json:"ttl_attribute,omitempty"`
	Stream       string            `json:"stream,omitempty"`
	Key          keyOutput         `json:"key"`
	Indexes      []indexOutput     `json:"indexes,omitempty"`
	ItemCount    int64             `json:"approximate_item_count"`
	SizeBytes    int64             `json:"approximate_size_bytes"`
}

// table returns the description of name, described once per session.
func (t *tables) table(ctx context.Context, name string) (*types.TableDescription, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d, ok := t.described[name]; ok {
		return d, nil
	}
	out, err := t.api.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err != nil {
		return nil, mcpserve.AWSError(err)
	}
	t.described[name] = out.Table
	return out.Table, nil
}

func keyOf(schema []types.KeySchemaElement) keyOutput {
	var k keyOutput
	for _, e := range schema {
		if e.KeyType == types.KeyTypeHash {
			k.Partition = aws.ToString(e.AttributeName)
		} else {
			k.Sort = aws.ToString(e.AttributeName)
		}
	}
	return k
}

func indexOf(kind string, name *string, schema []types.KeySchemaElement, p *types.Projection) indexOutput {
	idx := indexOutput{Name: aws.ToString(name), Kind: kind, Key: keyOf(schema)}
	if p != nil {
		idx.Projection, idx.NonKeyAttributes = string(p.ProjectionType), p.NonKeyAttributes
	}
	return idx
}

func (t *tables) Describe(ctx context.Context, in *describeInput) (describeOutput, error) {
	d, err := t.table(ctx, in.Table)
	if err != nil {
		return describeOutput{}, err
	}
	out := describeOutput{
		Table: aws.ToString(d.TableName), Status: string(d.TableStatus), Key: keyOf(d.KeySchema),
		Attributes: map[string]string{}, BillingMode: string(types.BillingModeProvisioned),
		ItemCount: aws.ToInt64(d.ItemCount), SizeBytes: aws.ToInt64(d.TableSizeBytes),
	}
	for _, a := range d.AttributeDefinitions {
		out.Attributes[aws.ToString(a.AttributeName)] = string(a.AttributeType)
	}
	if d.BillingModeSummary != nil {
		out.BillingMode = string(d.BillingModeSummary.BillingMode)
	}
	for _, g := range d.GlobalSecondaryIndexes {
		out.Indexes = append(out.Indexes, indexOf("global", g.IndexName, g.KeySchema, g.Projection))
	}
	for _, l := range d.LocalSecondaryIndexes {
		out.Indexes = append(out.Indexes, indexOf("local", l.IndexName, l.KeySchema, l.Projection))
	}
	if s := d.StreamSpecification; s != nil && aws.ToBool(s.StreamEnabled) {
		out.Stream = string(s.StreamViewType)
	}
	// The TTL is a detail: a profile not allowed to read it still describes the table.
	if ttl, ttlErr := t.api.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: d.TableName}); ttlErr == nil &&
		ttl.TimeToLiveDescription != nil && ttl.TimeToLiveDescription.TimeToLiveStatus == types.TimeToLiveStatusEnabled {
		out.TTLAttribute = aws.ToString(ttl.TimeToLiveDescription.AttributeName)
	}
	return out, nil
}

type getItemInput struct {
	Key            object            `json:"key" jsonschema:"the full primary key as plain JSON, e.g. {\"pk\": \"user#1\", \"sk\": \"profile\"}"`
	Names          map[string]string `json:"names,omitempty" jsonschema:"expression attribute names, e.g. {\"#s\": \"status\"}"`
	Table          string            `json:"table" jsonschema:"the table name"`
	Projection     string            `json:"projection,omitempty" jsonschema:"projection expression: the attributes to return"`
	ConsistentRead bool              `json:"consistent_read,omitempty"`
}

type getItemOutput struct {
	Item      map[string]any `json:"item,omitempty"`
	Found     bool           `json:"found"`
	Truncated bool           `json:"truncated,omitempty"`
}

func (t *tables) Get(ctx context.Context, in *getItemInput) (getItemOutput, error) {
	key, err := attributes(in.Key)
	if err != nil {
		return getItemOutput{}, err
	}
	out, err := t.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(in.Table), Key: key, ConsistentRead: aws.Bool(in.ConsistentRead),
		ProjectionExpression: optional(in.Projection), ExpressionAttributeNames: names(in.Names),
	})
	if err != nil {
		return getItemOutput{}, mcpserve.AWSError(err)
	}
	item, err := plainItem(out.Item)
	if err != nil || item == nil {
		return getItemOutput{}, err
	}
	res := getItemOutput{Item: item, Found: true}
	if jsonSize(item) > maxResultBytes {
		keys, keysErr := t.keyAttributes(ctx, in.Table, "")
		if keysErr != nil {
			return getItemOutput{}, keysErr
		}
		res.Item, res.Truncated = shrink(item, keys), true
	}
	return res, nil
}

type queryInput struct {
	Values       object            `json:"values,omitempty" jsonschema:"expression attribute values as plain JSON, e.g. {\":pk\": \"user#1\"}"`
	StartKey     object            `json:"start_key,omitempty" jsonschema:"next_key of the previous page"`
	Names        map[string]string `json:"names,omitempty" jsonschema:"expression attribute names, e.g. {\"#s\": \"status\"}"`
	Table        string            `json:"table" jsonschema:"the table name"`
	Index        string            `json:"index,omitempty" jsonschema:"a global or local secondary index to query instead of the table"`
	KeyCondition string            `json:"key_condition" jsonschema:"key condition expression, e.g. pk = :pk AND begins_with(sk, :prefix)"`
	Filter       string            `json:"filter,omitempty" jsonschema:"filter expression applied after the key condition"`
	Projection   string            `json:"projection,omitempty" jsonschema:"projection expression: the attributes to return"`
	Limit        int               `json:"limit,omitempty" jsonschema:"items to evaluate (default 50, max 500)"`
	Descending   bool              `json:"descending,omitempty" jsonschema:"sort key order, descending"`
}

type scanInput struct {
	Values     object            `json:"values,omitempty" jsonschema:"expression attribute values as plain JSON, e.g. {\":s\": \"open\"}"`
	StartKey   object            `json:"start_key,omitempty" jsonschema:"next_key of the previous page"`
	Names      map[string]string `json:"names,omitempty" jsonschema:"expression attribute names, e.g. {\"#s\": \"status\"}"`
	Table      string            `json:"table" jsonschema:"the table name"`
	Index      string            `json:"index,omitempty" jsonschema:"a global or local secondary index to scan instead of the table"`
	Filter     string            `json:"filter,omitempty" jsonschema:"filter expression"`
	Projection string            `json:"projection,omitempty" jsonschema:"projection expression: the attributes to return"`
	Limit      int               `json:"limit,omitempty" jsonschema:"items to evaluate (default 50, max 500)"`
}

type pageOutput struct {
	NextKey      map[string]any   `json:"next_key,omitempty" jsonschema:"pass it as start_key for the next page; absent on the last page"`
	Items        []map[string]any `json:"items"`
	Count        int              `json:"count"`
	ScannedCount int              `json:"scanned_count"`
	Truncated    bool             `json:"truncated,omitempty"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

// names is nil for no names: DynamoDB rejects an empty map.
func names(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func limit(n int) *int32 {
	switch {
	case n <= 0:
		n = defaultLimit
	case n > maxLimit:
		n = maxLimit
	}
	return aws.Int32(int32(n)) // #nosec G115 -- capped at maxLimit above.
}

func (t *tables) Query(ctx context.Context, in *queryInput) (pageOutput, error) {
	values, err := attributes(in.Values)
	if err != nil {
		return pageOutput{}, err
	}
	start, err := attributes(in.StartKey)
	if err != nil {
		return pageOutput{}, err
	}
	projection, given, err := t.withKeys(ctx, in.Table, in.Index, in.Projection, in.Names)
	if err != nil {
		return pageOutput{}, err
	}
	out, err := t.api.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(in.Table), IndexName: optional(in.Index), KeyConditionExpression: aws.String(in.KeyCondition),
		FilterExpression: optional(in.Filter), ProjectionExpression: projection,
		ExpressionAttributeValues: values, ExpressionAttributeNames: given,
		Limit: limit(in.Limit), ScanIndexForward: aws.Bool(!in.Descending), ExclusiveStartKey: start,
	})
	if err != nil {
		return pageOutput{}, mcpserve.AWSError(err)
	}
	return t.page(ctx, in.Table, in.Index, out.Items, out.LastEvaluatedKey, int(out.ScannedCount))
}

func (t *tables) Scan(ctx context.Context, in *scanInput) (pageOutput, error) {
	values, err := attributes(in.Values)
	if err != nil {
		return pageOutput{}, err
	}
	start, err := attributes(in.StartKey)
	if err != nil {
		return pageOutput{}, err
	}
	projection, given, err := t.withKeys(ctx, in.Table, in.Index, in.Projection, in.Names)
	if err != nil {
		return pageOutput{}, err
	}
	out, err := t.api.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(in.Table), IndexName: optional(in.Index),
		FilterExpression: optional(in.Filter), ProjectionExpression: projection,
		ExpressionAttributeValues: values, ExpressionAttributeNames: given,
		Limit: limit(in.Limit), ExclusiveStartKey: start,
	})
	if err != nil {
		return pageOutput{}, mcpserve.AWSError(err)
	}
	return t.page(ctx, in.Table, in.Index, out.Items, out.LastEvaluatedKey, int(out.ScannedCount))
}

// page converts the items of a query or scan, up to maxResultBytes of JSON. When items are cut,
// next_key is the key of the last item kept, so the next page starts right after it.
func (t *tables) page(ctx context.Context, table, index string, items []map[string]types.AttributeValue, last map[string]types.AttributeValue, scanned int) (pageOutput, error) {
	out := pageOutput{Items: []map[string]any{}, ScannedCount: scanned}
	used := 0
	for i, raw := range items {
		item, err := plainItem(raw)
		if err != nil {
			return pageOutput{}, err
		}
		size := jsonSize(item)
		if used+size > maxResultBytes {
			keys, err := t.keyAttributes(ctx, table, index)
			if err != nil {
				return pageOutput{}, err
			}
			out.Truncated = true
			if i > 0 {
				out.Count = len(out.Items)
				key := pick(items[i-1], keys)
				if len(key) != len(keys) {
					return pageOutput{}, errors.New("the page was cut but its last item misses key attributes, so the next page could not start after it: lower limit")
				}
				out.NextKey, err = plainItem(key)
				return out, err
			}
			// A single item past the cap is returned alone, its largest values cut.
			item = shrink(item, keys)
			size = jsonSize(item)
		}
		used += size
		out.Items = append(out.Items, item)
	}
	out.Count = len(out.Items)
	if len(last) > 0 {
		var err error
		out.NextKey, err = plainItem(last)
		if err != nil {
			return pageOutput{}, err
		}
	}
	return out, nil
}

// keyAttributes lists the key attributes of table, plus those of index when given: the attributes
// of a LastEvaluatedKey.
func (t *tables) keyAttributes(ctx context.Context, table, index string) ([]string, error) {
	d, err := t.table(ctx, table)
	if err != nil {
		return nil, err
	}
	schemas := [][]types.KeySchemaElement{d.KeySchema}
	for _, g := range d.GlobalSecondaryIndexes {
		if aws.ToString(g.IndexName) == index {
			schemas = append(schemas, g.KeySchema)
		}
	}
	for _, l := range d.LocalSecondaryIndexes {
		if aws.ToString(l.IndexName) == index {
			schemas = append(schemas, l.KeySchema)
		}
	}
	var keys []string
	seen := map[string]bool{}
	for _, schema := range schemas {
		for _, e := range schema {
			if name := aws.ToString(e.AttributeName); !seen[name] {
				seen[name] = true
				keys = append(keys, name)
			}
		}
	}
	return keys, nil
}

// withKeys adds the key attributes of table (and index) missing from projection, under names of
// their own, so that a page cut short still has the key of its last item; given is not modified.
func (t *tables) withKeys(ctx context.Context, table, index, projection string, given map[string]string) (expression *string, all map[string]string, err error) {
	if projection == "" {
		return nil, names(given), nil
	}
	keys, err := t.keyAttributes(ctx, table, index)
	if err != nil {
		return nil, nil, err
	}
	all = maps.Clone(given)
	if all == nil {
		all = map[string]string{}
	}
	named := map[string]bool{}
	for _, path := range strings.Split(projection, ",") {
		path = strings.TrimSpace(path)
		if end := strings.IndexAny(path, ".["); end >= 0 {
			path = path[:end]
		}
		if name, ok := all[path]; ok {
			path = name
		}
		named[path] = true
	}
	n := 0
	for _, k := range keys {
		if named[k] { // DynamoDB rejects a projection naming an attribute twice.
			continue
		}
		for all[fmt.Sprintf("#airlock_k%d", n)] != "" {
			n++
		}
		name := fmt.Sprintf("#airlock_k%d", n)
		all[name] = k
		projection += ", " + name
	}
	return &projection, names(all), nil
}

// pick returns the attributes of item named in keys.
func pick(item map[string]types.AttributeValue, keys []string) map[string]types.AttributeValue {
	out := map[string]types.AttributeValue{}
	for _, k := range keys {
		if v, ok := item[k]; ok {
			out[k] = v
		}
	}
	return out
}

// shrink replaces the largest non-key values of item with truncatedValue until its JSON fits in
// maxResultBytes (or only keys are left).
func shrink(item map[string]any, keys []string) map[string]any {
	key := map[string]bool{}
	for _, k := range keys {
		key[k] = true
	}
	for jsonSize(item) > maxResultBytes {
		largest, largestSize := "", 0
		for k, v := range item {
			if size := jsonSize(v); !key[k] && v != truncatedValue && size > largestSize {
				largest, largestSize = k, size
			}
		}
		if largest == "" {
			break
		}
		item[largest] = truncatedValue
	}
	return item
}

func jsonSize(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}
