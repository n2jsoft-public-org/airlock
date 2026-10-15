package dynamomcp

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type putItemInput struct {
	Item      object            `json:"item" jsonschema:"the whole item as plain JSON, its key included; it replaces any item with that key"`
	Values    object            `json:"values,omitempty" jsonschema:"expression attribute values of the condition"`
	Names     map[string]string `json:"names,omitempty" jsonschema:"expression attribute names, e.g. {\"#s\": \"status\"}"`
	Table     string            `json:"table" jsonschema:"the table name"`
	Condition string            `json:"condition,omitempty" jsonschema:"condition expression the existing item must meet, e.g. attribute_not_exists(pk)"`
}

type updateItemInput struct {
	Key       object            `json:"key" jsonschema:"the full primary key as plain JSON"`
	Values    object            `json:"values,omitempty" jsonschema:"expression attribute values, e.g. {\":s\": \"shipped\"}"`
	Names     map[string]string `json:"names,omitempty" jsonschema:"expression attribute names, e.g. {\"#s\": \"status\"}"`
	Table     string            `json:"table" jsonschema:"the table name"`
	Update    string            `json:"update" jsonschema:"update expression, e.g. SET #s = :s REMOVE obsolete"`
	Condition string            `json:"condition,omitempty" jsonschema:"condition expression the existing item must meet"`
}

type deleteItemInput struct {
	Key       object            `json:"key" jsonschema:"the full primary key as plain JSON"`
	Values    object            `json:"values,omitempty" jsonschema:"expression attribute values of the condition"`
	Names     map[string]string `json:"names,omitempty" jsonschema:"expression attribute names"`
	Table     string            `json:"table" jsonschema:"the table name"`
	Condition string            `json:"condition,omitempty" jsonschema:"condition expression the existing item must meet"`
}

type writeOutput struct {
	Item    map[string]any `json:"item,omitempty" jsonschema:"the item after update_item"`
	Written bool           `json:"written"`
}

func (t *tables) Put(ctx context.Context, in *putItemInput) (writeOutput, error) {
	item, err := attributes(in.Item)
	if err != nil {
		return writeOutput{}, err
	}
	values, err := attributes(in.Values)
	if err != nil {
		return writeOutput{}, err
	}
	if _, err = t.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(in.Table), Item: item, ConditionExpression: optional(in.Condition),
		ExpressionAttributeValues: values, ExpressionAttributeNames: names(in.Names),
	}); err != nil {
		return writeOutput{}, awsError(err)
	}
	return writeOutput{Written: true}, nil
}

func (t *tables) Update(ctx context.Context, in *updateItemInput) (writeOutput, error) {
	key, err := attributes(in.Key)
	if err != nil {
		return writeOutput{}, err
	}
	values, err := attributes(in.Values)
	if err != nil {
		return writeOutput{}, err
	}
	out, err := t.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(in.Table), Key: key, UpdateExpression: aws.String(in.Update), ConditionExpression: optional(in.Condition),
		ExpressionAttributeValues: values, ExpressionAttributeNames: names(in.Names), ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return writeOutput{}, awsError(err)
	}
	item, err := plainItem(out.Attributes)
	return writeOutput{Item: item, Written: err == nil}, err
}

func (t *tables) Delete(ctx context.Context, in *deleteItemInput) (writeOutput, error) {
	key, err := attributes(in.Key)
	if err != nil {
		return writeOutput{}, err
	}
	values, err := attributes(in.Values)
	if err != nil {
		return writeOutput{}, err
	}
	if _, err = t.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(in.Table), Key: key, ConditionExpression: optional(in.Condition),
		ExpressionAttributeValues: values, ExpressionAttributeNames: names(in.Names),
	}); err != nil {
		return writeOutput{}, awsError(err)
	}
	return writeOutput{Written: true}, nil
}
