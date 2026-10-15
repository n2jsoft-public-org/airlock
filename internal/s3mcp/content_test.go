package s3mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const png = "\x89PNG\r\n\x1a\n"

func TestClassify(t *testing.T) {
	cases := []struct {
		contentType, encoding, key string
		want                       class
	}{
		{"text/csv; charset=utf-8", "", "a.csv", classText},
		{"application/json", "", "a", classText},
		{"application/vnd.api+json", "", "a", classText},
		{"image/svg+xml", "", "a.svg", classText},
		{"image/png", "", "a", classImage},
		{"image/jpg", "", "a", classImage},
		{"application/javascript", "", "a.js", classText},
		{"application/pdf", "", "a", classPDF},
		{"application/zip", "", "a.zip", classOther},
		{"application/octet-stream", "", "receipts/2026/r-12.pdf", classPDF},
		{"binary/octet-stream", "", "photo.JPG", classImage},
		{"", "", "photo.jpg", classImage},
		{"application/octet-stream", "", "data", classUnknown},
		{"text/plain", "gzip", "a.log.gz", classOther},
	}
	for _, c := range cases {
		if got, _ := classify(c.contentType, c.encoding, c.key); got != c.want {
			t.Fatalf("classify(%q, %q, %q) = %v, want %v", c.contentType, c.encoding, c.key, got, c.want)
		}
	}
}

// get calls Get on the object b/key of f and returns the result, failing on errors.
func get(t *testing.T, f *fakeAPI, key string, offset int64) *mcp.CallToolResult {
	t.Helper()
	res, _, err := newBuckets(f).Get(t.Context(), &getObjectInput{Bucket: "b", Key: key, Offset: offset})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// decoded decodes the first content of res, a JSON text, into out.
func decoded[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %#v", res.Content[0])
	}
	var out T
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func textObject(body string) *fakeAPI {
	return &fakeAPI{head: headOf(int64(len(body)), "text/plain; charset=utf-8"), body: []byte(body)}
}

func TestGetTextStopsBeforeARuneCutAtTheEndOfTheSlice(t *testing.T) {
	body := strings.Repeat("a", maxTextBytes-1) + "é" + "tail" // é is 2 bytes: the slice ends inside it.
	f := textObject(body)
	first := decoded[textOutput](t, get(t, f, "a.csv", 0))
	if aws.ToString(f.getIn.Range) != "bytes=0-262143" || len(first.Text) != maxTextBytes-1 || !first.Truncated || first.NextOffset != maxTextBytes-1 {
		t.Fatalf("range = %s, text = %d bytes, truncated = %v, next = %d", aws.ToString(f.getIn.Range), len(first.Text), first.Truncated, first.NextOffset)
	}
	second := decoded[textOutput](t, get(t, f, "a.csv", first.NextOffset))
	if second.Text != "étail" || second.Truncated || second.NextOffset != 0 || second.Offset != first.NextOffset {
		t.Fatalf("second = %+v", second)
	}
}

func TestGetTextSkipsTheEndOfARuneCutByTheOffset(t *testing.T) {
	out := decoded[textOutput](t, get(t, textObject("éa"), "a.txt", 1))
	if out.Text != "a" || out.Offset != 2 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetTextPastTheEndIsEmpty(t *testing.T) {
	f := textObject("abc")
	out := decoded[textOutput](t, get(t, f, "a.txt", 10))
	if out.Text != "" || out.Truncated || f.getIn != nil {
		t.Fatalf("out = %+v, get = %+v", out, f.getIn)
	}
}

func TestGetSniffsUTF8WhenTheTypeIsUnknown(t *testing.T) {
	f := &fakeAPI{head: headOf(5, "application/octet-stream"), body: []byte("hello")}
	if out := decoded[textOutput](t, get(t, f, "notes", 0)); out.Text != "hello" {
		t.Fatalf("out = %+v", out)
	}
	f = &fakeAPI{head: headOf(3, "application/octet-stream"), body: []byte{0xff, 0xfe, 0x00}}
	if out := decoded[metadataOutput](t, get(t, f, "blob", 0)); out.Content != "binary: not returned" || out.Size != 3 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetImageReturnsAnImageAndItsMetadata(t *testing.T) {
	f := &fakeAPI{head: headOf(8, "image/png"), body: []byte(png)}
	res := get(t, f, "a.png", 0)
	image, ok := res.Content[1].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/png" || string(image.Data) != png || f.getIn.Range != nil || res.StructuredContent != nil {
		t.Fatalf("content = %#v, range = %v", res.Content, f.getIn.Range)
	}
	if info := decoded[objectInfo](t, res); info.Key != "a.png" || info.Size != 8 {
		t.Fatalf("info = %+v", info)
	}
}

func TestGetImageWithoutImageBytesReturnsTheMetadataOnly(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"empty": {"", "empty"}, "html": {"<html>Not found</html>", "binary: not returned"},
	} {
		f := &fakeAPI{head: headOf(int64(len(c.body)), "image/png"), body: []byte(c.body)}
		res := get(t, f, "a.png", 0)
		if out := decoded[metadataOutput](t, res); len(res.Content) != 1 || out.Content != c.want {
			t.Fatalf("%s: res = %+v, out = %+v", name, res, out)
		}
	}
}

func TestGetPDFUploadedAsOctetStreamReturnsAnEmbeddedResource(t *testing.T) {
	f := &fakeAPI{head: headOf(5, "application/octet-stream"), body: []byte("%PDF-")}
	res := get(t, f, "receipts/r.pdf", 0)
	resource, ok := res.Content[1].(*mcp.EmbeddedResource)
	if !ok || resource.Resource.URI != "s3://b/receipts/r.pdf" || resource.Resource.MIMEType != "application/pdf" || string(resource.Resource.Blob) != "%PDF-" {
		t.Fatalf("content = %#v", res.Content)
	}
}

func TestGetImageOverTheCapReturnsTheMetadataOnly(t *testing.T) {
	f := &fakeAPI{head: headOf(maxBinaryBytes+1, "image/jpeg")}
	out := decoded[metadataOutput](t, get(t, f, "big.jpg", 0))
	if out.Content != "too large: 3750001 bytes" || f.getIn != nil {
		t.Fatalf("out = %+v, get = %+v", out, f.getIn)
	}
}

func TestGetImageRefusesAnOffset(t *testing.T) {
	f := &fakeAPI{head: headOf(8, "image/png"), body: []byte(png)}
	if _, _, err := newBuckets(f).Get(t.Context(), &getObjectInput{Bucket: "b", Key: "a.png", Offset: 2}); err == nil {
		t.Fatal("expected the offset to be refused")
	}
}

func TestGetOtherBinaryReturnsTheMetadataOnly(t *testing.T) {
	f := &fakeAPI{head: headOf(10, "application/zip")}
	if out := decoded[metadataOutput](t, get(t, f, "a.zip", 0)); out.Content != "binary: not returned" || f.getIn != nil {
		t.Fatalf("out = %+v, get = %+v", out, f.getIn)
	}
}
