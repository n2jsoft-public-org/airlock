package s3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	// callTimeout bounds each tool call but get_object.
	callTimeout = 30 * time.Second
	// maxResultBytes caps the JSON of the entries of a listing: entries stop being added past it.
	maxResultBytes = 256 << 10
	defaultLimit   = 100
	maxLimit       = 1000
	// bucketsPage is the number of buckets list_buckets returns per page.
	bucketsPage = 100
	// afterPrefix starts a start rebuilt from the last entry kept: its key goes to StartAfter.
	afterPrefix = "after:"
	// pastPrefix follows a folder in a rebuilt start, so the keys under it are not listed again.
	pastPrefix = "\U0010FFFF"
)

// api is the part of the S3 client the tools use; *s3.Client implements it.
type api interface {
	ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// buckets runs the tool operations; the regions of the buckets are cached for the session.
type buckets struct {
	api     api
	regions map[string]string
	mu      sync.Mutex
}

func newBuckets(a api) *buckets {
	return &buckets{api: a, regions: map[string]string{}}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

func timestamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// region is the region of bucket, asked once per session: list_buckets fills the cache, HeadBucket
// in the client's region answers for the others, through x-amz-bucket-region when it refuses or
// redirects. "" is the client's own region.
func (b *buckets) region(ctx context.Context, bucket string) (string, error) {
	b.mu.Lock()
	region, ok := b.regions[bucket]
	b.mu.Unlock()
	if ok {
		return region, nil
	}
	out, err := b.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		region = aws.ToString(out.BucketRegion)
	} else {
		respErr, isResp := errors.AsType[*awshttp.ResponseError](err)
		notFound := false
		if isResp && respErr.Response != nil && respErr.Response.Response != nil {
			region = respErr.Response.Header.Get("x-amz-bucket-region")
			notFound = respErr.HTTPStatusCode() == http.StatusNotFound
		}
		if region == "" && notFound {
			return "", fmt.Errorf("bucket %s not found", bucket)
		}
		if region == "" {
			return "", mcpserve.AWSError(err)
		}
	}
	b.mu.Lock()
	b.regions[bucket] = region
	b.mu.Unlock()
	return region, nil
}

// at is the per-call option sending a request to the region of bucket.
func (b *buckets) at(ctx context.Context, bucket string) (func(*s3.Options), error) {
	region, err := b.region(ctx, bucket)
	if err != nil {
		return nil, err
	}
	return func(o *s3.Options) {
		if region != "" {
			o.Region = region
		}
	}, nil
}

type listBucketsInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"only the buckets whose name starts with this text"`
	Start  string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type bucketOutput struct {
	Name    string `json:"name"`
	Region  string `json:"region,omitempty"`
	Created string `json:"created,omitempty"`
}

type listBucketsOutput struct {
	Next    string         `json:"next,omitempty"`
	Buckets []bucketOutput `json:"buckets"`
}

func (b *buckets) List(ctx context.Context, in *listBucketsInput) (listBucketsOutput, error) {
	out, err := b.api.ListBuckets(ctx, &s3.ListBucketsInput{
		MaxBuckets: aws.Int32(bucketsPage), Prefix: optional(in.Prefix), ContinuationToken: optional(in.Start),
	})
	if err != nil {
		return listBucketsOutput{Buckets: []bucketOutput{}}, mcpserve.AWSError(err)
	}
	res := listBucketsOutput{Next: aws.ToString(out.ContinuationToken), Buckets: make([]bucketOutput, 0, len(out.Buckets))}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, bucket := range out.Buckets {
		name, region := aws.ToString(bucket.Name), aws.ToString(bucket.BucketRegion)
		if region != "" {
			b.regions[name] = region
		}
		res.Buckets = append(res.Buckets, bucketOutput{Name: name, Region: region, Created: timestamp(bucket.CreationDate)})
	}
	return res, nil
}

type listObjectsInput struct {
	Bucket    string `json:"bucket" jsonschema:"the bucket name"`
	Prefix    string `json:"prefix,omitempty" jsonschema:"only the keys starting with this text, e.g. logs/2026/"`
	Start     string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
	Limit     int    `json:"limit,omitempty" jsonschema:"entries per page: 100 by default, at most 1000"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"list every key under prefix instead of one level"`
}

type objectOutput struct {
	Key          string `json:"key"`
	LastModified string `json:"last_modified,omitempty"`
	ETag         string `json:"etag,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
	Size         int64  `json:"size"`
}

type listObjectsOutput struct {
	Next      string         `json:"next,omitempty"`
	Objects   []objectOutput `json:"objects"`
	Prefixes  []string       `json:"prefixes,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

func (b *buckets) Objects(ctx context.Context, in *listObjectsInput) (listObjectsOutput, error) {
	empty := listObjectsOutput{Objects: []objectOutput{}}
	at, err := b.at(ctx, in.Bucket)
	if err != nil {
		return empty, err
	}
	req := &s3.ListObjectsV2Input{Bucket: aws.String(in.Bucket), Prefix: optional(in.Prefix), MaxKeys: aws.Int32(int32(mcpserve.Limit(in.Limit, defaultLimit, maxLimit)))} //nolint:gosec // at most maxLimit.
	if !in.Recursive {
		req.Delimiter = aws.String("/")
	}
	if after, ok := strings.CutPrefix(in.Start, afterPrefix); ok {
		req.StartAfter = aws.String(after)
	} else {
		req.ContinuationToken = optional(in.Start)
	}
	out, err := b.api.ListObjectsV2(ctx, req, at)
	if err != nil {
		return empty, mcpserve.AWSError(err)
	}
	return capListing(out), nil
}

func objectOf(o *types.Object) objectOutput {
	return objectOutput{
		Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), LastModified: timestamp(o.LastModified),
		ETag: aws.ToString(o.ETag), StorageClass: string(o.StorageClass),
	}
}

// capListing keeps the objects and folders of out, merged in key order, while their JSON stays under
// maxResultBytes. Past it, next restarts after the last entry kept: S3's token would skip the ones
// cut.
func capListing(out *s3.ListObjectsV2Output) listObjectsOutput {
	res := listObjectsOutput{Objects: []objectOutput{}, Next: aws.ToString(out.NextContinuationToken)}
	objects, prefixes := out.Contents, out.CommonPrefixes
	size, last := 0, ""
	for len(objects) > 0 || len(prefixes) > 0 {
		folder := len(objects) == 0 || (len(prefixes) > 0 && aws.ToString(prefixes[0].Prefix) < aws.ToString(objects[0].Key))
		var entry any
		var after string
		if folder {
			p := aws.ToString(prefixes[0].Prefix)
			entry, after = p, p+pastPrefix
		} else {
			o := objectOf(&objects[0])
			entry, after = o, o.Key
		}
		raw, _ := json.Marshal(entry) //nolint:errcheck // a string or plain fields.
		if size+len(raw) > maxResultBytes && last != "" {
			res.Truncated, res.Next = true, afterPrefix+last
			return res
		}
		if folder {
			res.Prefixes, prefixes = append(res.Prefixes, aws.ToString(prefixes[0].Prefix)), prefixes[1:]
		} else {
			res.Objects, objects = append(res.Objects, objectOf(&objects[0])), objects[1:]
		}
		size, last = size+len(raw)+1, after
	}
	return res
}

type objectInput struct {
	Bucket    string `json:"bucket" jsonschema:"the bucket name"`
	Key       string `json:"key" jsonschema:"the object key"`
	VersionID string `json:"version_id,omitempty" jsonschema:"a version of the object; the current one when empty"`
}

type objectInfo struct {
	Metadata        map[string]string `json:"metadata,omitempty"`
	Key             string            `json:"key"`
	ContentType     string            `json:"content_type,omitempty"`
	ContentEncoding string            `json:"content_encoding,omitempty"`
	LastModified    string            `json:"last_modified,omitempty"`
	ETag            string            `json:"etag,omitempty"`
	StorageClass    string            `json:"storage_class,omitempty"`
	VersionID       string            `json:"version_id,omitempty"`
	Size            int64             `json:"size"`
}

// head is the metadata of the object of in, and the option sending requests to its bucket's region.
func (b *buckets) head(ctx context.Context, in *objectInput) (objectInfo, func(*s3.Options), error) {
	at, err := b.at(ctx, in.Bucket)
	if err != nil {
		return objectInfo{}, nil, err
	}
	out, err := b.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(in.Bucket), Key: aws.String(in.Key), VersionId: optional(in.VersionID)}, at)
	if err != nil {
		if apiErr, ok := errors.AsType[smithy.APIError](err); ok && apiErr.ErrorCode() == "NotFound" {
			return objectInfo{}, nil, fmt.Errorf("NoSuchKey: object %s/%s not found", in.Bucket, in.Key)
		}
		return objectInfo{}, nil, mcpserve.AWSError(err)
	}
	return objectInfo{
		Metadata: out.Metadata, Key: in.Key, ContentType: aws.ToString(out.ContentType), ContentEncoding: aws.ToString(out.ContentEncoding),
		LastModified: timestamp(out.LastModified), ETag: aws.ToString(out.ETag), StorageClass: string(out.StorageClass),
		VersionID: aws.ToString(out.VersionId), Size: aws.ToInt64(out.ContentLength),
	}, at, nil
}

func (b *buckets) Head(ctx context.Context, in *objectInput) (objectInfo, error) {
	info, _, err := b.head(ctx, in)
	return info, err
}
