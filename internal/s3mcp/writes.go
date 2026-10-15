package s3mcp

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// defaultContentType is the type of the objects put_object writes when none is given.
const defaultContentType = "text/plain; charset=utf-8"

type putObjectInput struct {
	Bucket      string `json:"bucket" jsonschema:"the bucket name"`
	Key         string `json:"key" jsonschema:"the object key"`
	Content     string `json:"content" jsonschema:"the new content, as UTF-8 text"`
	ContentType string `json:"content_type,omitempty" jsonschema:"text/plain; charset=utf-8 when empty"`
	IfNoneMatch string `json:"if_none_match,omitempty" jsonschema:"* to create the object only when it does not exist"`
	IfMatch     string `json:"if_match,omitempty" jsonschema:"an ETag from head_object: replace the object only while it is that version"`
}

type putObjectOutput struct {
	ETag      string `json:"etag,omitempty"`
	VersionID string `json:"version_id,omitempty"`
}

func (b *buckets) Put(ctx context.Context, in *putObjectInput) (putObjectOutput, error) {
	at, err := b.at(ctx, in.Bucket)
	if err != nil {
		return putObjectOutput{}, err
	}
	contentType := in.ContentType
	if contentType == "" {
		contentType = defaultContentType
	}
	out, err := b.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(in.Bucket), Key: aws.String(in.Key), Body: strings.NewReader(in.Content),
		ContentLength: aws.Int64(int64(len(in.Content))), ContentType: aws.String(contentType),
		IfNoneMatch: optional(in.IfNoneMatch), IfMatch: optional(in.IfMatch),
	}, at)
	if err != nil {
		return putObjectOutput{}, mcpserve.AWSError(err)
	}
	return putObjectOutput{ETag: aws.ToString(out.ETag), VersionID: aws.ToString(out.VersionId)}, nil
}

type deleteObjectOutput struct {
	VersionID    string `json:"version_id,omitempty"`
	DeleteMarker bool   `json:"delete_marker"`
}

func (b *buckets) Delete(ctx context.Context, in *objectInput) (deleteObjectOutput, error) {
	at, err := b.at(ctx, in.Bucket)
	if err != nil {
		return deleteObjectOutput{}, err
	}
	out, err := b.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(in.Bucket), Key: aws.String(in.Key), VersionId: optional(in.VersionID)}, at)
	if err != nil {
		return deleteObjectOutput{}, mcpserve.AWSError(err)
	}
	return deleteObjectOutput{VersionID: aws.ToString(out.VersionId), DeleteMarker: aws.ToBool(out.DeleteMarker)}, nil
}
