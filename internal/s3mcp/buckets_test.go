package s3mcp

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func TestListBucketsFillsTheRegionCache(t *testing.T) {
	f := &fakeAPI{buckets: []types.Bucket{{Name: aws.String("logs"), BucketRegion: aws.String("eu-west-1")}}, nextToken: "tok"}
	b := newBuckets(f)
	out, err := b.List(t.Context(), &listBucketsInput{Prefix: "lo"})
	if err != nil || len(out.Buckets) != 1 || out.Buckets[0].Region != "eu-west-1" || out.Next != "tok" ||
		aws.ToInt32(f.listBuckets.MaxBuckets) != bucketsPage || aws.ToString(f.listBuckets.Prefix) != "lo" {
		t.Fatalf("out = %+v, in = %+v, %v", out, f.listBuckets, err)
	}
	if _, err := b.Objects(t.Context(), &listObjectsInput{Bucket: "logs"}); err != nil {
		t.Fatal(err)
	}
	if f.headBuckets != 0 || f.regions[len(f.regions)-1] != "eu-west-1" {
		t.Fatalf("head buckets = %d, regions = %v", f.headBuckets, f.regions)
	}
}

func TestBucketRegionIsAskedOnceAndFollowsTheRedirect(t *testing.T) {
	f := &fakeAPI{headBucket: responseError(http.StatusMovedPermanently, "ap-south-1")}
	b := newBuckets(f)
	for range 2 {
		if _, err := b.Objects(t.Context(), &listObjectsInput{Bucket: "far"}); err != nil {
			t.Fatal(err)
		}
	}
	if f.headBuckets != 1 || f.regions[len(f.regions)-1] != "ap-south-1" {
		t.Fatalf("head buckets = %d, regions = %v", f.headBuckets, f.regions)
	}
}

func TestBucketRegionFromASuccessfulHeadBucket(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-central-1"}
	if _, err := newBuckets(f).Objects(t.Context(), &listObjectsInput{Bucket: "near"}); err != nil {
		t.Fatal(err)
	}
	if f.regions[len(f.regions)-1] != "eu-central-1" {
		t.Fatalf("regions = %v", f.regions)
	}
}

func TestUnknownBucketIsNotFound(t *testing.T) {
	f := &fakeAPI{headBucket: responseError(http.StatusNotFound, "")}
	_, err := newBuckets(f).Objects(t.Context(), &listObjectsInput{Bucket: "gone"})
	if err == nil || err.Error() != "bucket gone not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestListObjectsListsOneLevelByDefault(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-west-3", objects: []types.Object{{Key: aws.String("logs/a.txt"), Size: aws.Int64(3)}},
		prefixes: []string{"logs/2026/"}, nextToken: "tok"}
	out, err := newBuckets(f).Objects(t.Context(), &listObjectsInput{Bucket: "b", Prefix: "logs/"})
	if err != nil || aws.ToString(f.listObjects.Delimiter) != "/" || aws.ToInt32(f.listObjects.MaxKeys) != defaultLimit ||
		aws.ToString(f.listObjects.Prefix) != "logs/" || len(out.Objects) != 1 || out.Objects[0].Size != 3 ||
		len(out.Prefixes) != 1 || out.Next != "tok" || out.Truncated {
		t.Fatalf("out = %+v, in = %+v, %v", out, f.listObjects, err)
	}
}

func TestListObjectsRecursiveHasNoDelimiterAndCapsTheLimit(t *testing.T) {
	f := &fakeAPI{}
	if _, err := newBuckets(f).Objects(t.Context(), &listObjectsInput{Bucket: "b", Recursive: true, Limit: 5000}); err != nil {
		t.Fatal(err)
	}
	if f.listObjects.Delimiter != nil || aws.ToInt32(f.listObjects.MaxKeys) != maxLimit {
		t.Fatalf("in = %+v", f.listObjects)
	}
}

func TestListObjectsTakesATokenOrARebuiltStart(t *testing.T) {
	f := &fakeAPI{}
	b := newBuckets(f)
	if _, err := b.Objects(t.Context(), &listObjectsInput{Bucket: "b", Start: "after:logs/z"}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.listObjects.StartAfter) != "logs/z" || f.listObjects.ContinuationToken != nil {
		t.Fatalf("in = %+v", f.listObjects)
	}
	if _, err := b.Objects(t.Context(), &listObjectsInput{Bucket: "b", Start: "tok"}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(f.listObjects.ContinuationToken) != "tok" || f.listObjects.StartAfter != nil {
		t.Fatalf("in = %+v", f.listObjects)
	}
}

// longKeys are n objects whose keys are about 1 KB long, starting with first.
func longKeys(first string, n int) []types.Object {
	long := strings.Repeat("x", 1000)
	objects := make([]types.Object, 0, n)
	for i := range n {
		objects = append(objects, types.Object{Key: aws.String(fmt.Sprintf("%s%03d%s", first, i, long)), Size: aws.Int64(1)})
	}
	return objects
}

func TestCapListingCutsInKeyOrder(t *testing.T) {
	// m/ sorts after every object: cut among the objects, it must not be returned.
	out := &s3.ListObjectsV2Output{Contents: longKeys("k", 300), CommonPrefixes: []types.CommonPrefix{{Prefix: aws.String("m/")}},
		NextContinuationToken: aws.String("s3-token")}
	res := capListing(out)
	if !res.Truncated || len(res.Prefixes) != 0 || len(res.Objects) == 0 || len(res.Objects) >= 300 ||
		res.Next != "after:"+res.Objects[len(res.Objects)-1].Key {
		t.Fatalf("objects = %d, prefixes = %v, next = %.20q, truncated = %v", len(res.Objects), res.Prefixes, res.Next, res.Truncated)
	}
}

func TestCapListingRestartsPastACutFolder(t *testing.T) {
	out := &s3.ListObjectsV2Output{}
	for _, o := range longKeys("p", 300) {
		out.CommonPrefixes = append(out.CommonPrefixes, types.CommonPrefix{Prefix: aws.String(*o.Key + "/")})
	}
	res := capListing(out)
	if !res.Truncated || len(res.Prefixes) >= 300 || res.Next != "after:"+res.Prefixes[len(res.Prefixes)-1]+"\U0010FFFF" {
		t.Fatalf("prefixes = %d, next = %.20q, truncated = %v", len(res.Prefixes), res.Next, res.Truncated)
	}
}

func TestCapListingKeepsTheS3TokenWhenNothingIsCut(t *testing.T) {
	res := capListing(&s3.ListObjectsV2Output{Contents: longKeys("k", 3), NextContinuationToken: aws.String("s3-token")})
	if res.Truncated || len(res.Objects) != 3 || res.Next != "s3-token" {
		t.Fatalf("res = %+v", res)
	}
}

func TestHeadObjectOfAMissingKeyIsNoSuchKey(t *testing.T) {
	f := &fakeAPI{err: &smithy.GenericAPIError{Code: "NotFound"}}
	_, err := newBuckets(f).Head(t.Context(), &objectInput{Bucket: "b", Key: "gone.txt"})
	if err == nil || err.Error() != "NoSuchKey: object b/gone.txt not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestHeadObjectReturnsTheMetadata(t *testing.T) {
	head := headOf(42, "application/json")
	head.Metadata = map[string]string{"owner": "billing"}
	head.LastModified = aws.Time(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	f := &fakeAPI{head: head}
	info, err := newBuckets(f).Head(t.Context(), &objectInput{Bucket: "b", Key: "k.json"})
	if err != nil || info.Key != "k.json" || info.Size != 42 || info.ContentType != "application/json" || info.Metadata["owner"] != "billing" ||
		info.LastModified != "2026-10-15T09:00:00Z" || info.ETag != `"abc"` {
		t.Fatalf("info = %+v, %v", info, err)
	}
}
