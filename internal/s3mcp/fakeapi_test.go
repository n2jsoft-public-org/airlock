package s3mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// fakeAPI answers with its fields, records the last request of each operation and the region
// every call was sent to ("" for the client's own).
type fakeAPI struct {
	err          error
	headBucket   error
	head         *s3.HeadObjectOutput
	listBuckets  *s3.ListBucketsInput
	listObjects  *s3.ListObjectsV2Input
	getIn        *s3.GetObjectInput
	putIn        *s3.PutObjectInput
	deleteIn     *s3.DeleteObjectInput
	bucketRegion string
	nextToken    string
	putBody      string
	buckets      []types.Bucket
	objects      []types.Object
	prefixes     []string
	body         []byte
	regions      []string
	headBuckets  int
}

func (f *fakeAPI) record(opts []func(*s3.Options)) {
	var o s3.Options
	for _, opt := range opts {
		opt(&o)
	}
	f.regions = append(f.regions, o.Region)
}

func (f *fakeAPI) ListBuckets(_ context.Context, in *s3.ListBucketsInput, opts ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	f.listBuckets = in
	f.record(opts)
	return &s3.ListBucketsOutput{Buckets: f.buckets, ContinuationToken: optional(f.nextToken)}, f.err
}

func (f *fakeAPI) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	f.headBuckets++
	f.record(opts)
	if f.headBucket != nil {
		return nil, f.headBucket
	}
	return &s3.HeadBucketOutput{BucketRegion: optional(f.bucketRegion)}, nil
}

func (f *fakeAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listObjects = in
	f.record(opts)
	out := &s3.ListObjectsV2Output{Contents: f.objects, NextContinuationToken: optional(f.nextToken)}
	for _, p := range f.prefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, types.CommonPrefix{Prefix: aws.String(p)})
	}
	return out, f.err
}

func (f *fakeAPI) HeadObject(_ context.Context, _ *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.record(opts)
	if f.head == nil {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("NotFound")
	}
	return f.head, f.err
}

// GetObject serves body, or the bytes of the request's range of it.
func (f *fakeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getIn = in
	f.record(opts)
	data := f.body
	if in.Range != nil {
		var first, last int
		if _, err := fmt.Sscanf(*in.Range, "bytes=%d-%d", &first, &last); err != nil {
			return nil, err
		}
		data = data[min(first, len(data)):min(last+1, len(data))]
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, f.err
}

func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putIn = in
	f.record(opts)
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.putBody = string(body)
	return &s3.PutObjectOutput{ETag: aws.String(`"new"`), VersionId: aws.String("v2")}, f.err
}

func (f *fakeAPI) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteIn = in
	f.record(opts)
	return &s3.DeleteObjectOutput{DeleteMarker: aws.Bool(true), VersionId: aws.String("v3")}, f.err
}

// responseError is the error of an S3 answer with status and, when not empty, the bucket's region header.
func responseError(status int, region string) error {
	header := http.Header{}
	if region != "" {
		header.Set("x-amz-bucket-region", region)
	}
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status, Header: header}},
		Err:      errors.New(http.StatusText(status)),
	}}
}

// headOf is the HeadObject answer for an object of size bytes and contentType.
func headOf(size int64, contentType string) *s3.HeadObjectOutput {
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(size), ContentType: optional(contentType), ETag: aws.String(`"abc"`)}
}
