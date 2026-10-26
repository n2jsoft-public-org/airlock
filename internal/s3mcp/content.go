package s3mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	// maxTextBytes is the slice of a text object one get_object returns.
	maxTextBytes = 256 << 10
	// maxBinaryBytes caps images and PDFs: 5 000 000 bytes once base64-encoded, the Claude API's
	// image limit.
	maxBinaryBytes = 3_750_000
)

// class is what get_object returns for an object.
type class int

const (
	// classUnknown objects are text when their first slice is valid UTF-8.
	classUnknown class = iota
	classText
	classImage
	classPDF
	classOther
)

var (
	textTypes = map[string]bool{
		"application/json": true, "application/xml": true, "application/yaml": true, "application/x-yaml": true,
		"application/x-ndjson": true, "application/csv": true, "application/javascript": true,
		"application/x-sh": true, "application/sql": true, "application/toml": true,
	}
	imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}
	// compressed encodings are not decoded: their objects are binaries.
	compressed = map[string]bool{"gzip": true, "x-gzip": true, "br": true, "deflate": true, "compress": true, "zstd": true}
)

func mediaType(value string) string {
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	media = strings.ToLower(media)
	if media == "image/jpg" {
		return "image/jpeg"
	}
	return media
}

func generic(media string) bool {
	return media == "" || media == "application/octet-stream" || media == "binary/octet-stream"
}

// classify tells what get_object returns for an object of contentType and encoding named key, and
// its media type: its content type, else the type of its key's extension.
func classify(contentType, encoding, key string) (kind class, media string) {
	if compressed[strings.ToLower(strings.TrimSpace(encoding))] {
		return classOther, ""
	}
	media = mediaType(contentType)
	if generic(media) {
		media = mediaType(mime.TypeByExtension(strings.ToLower(path.Ext(key))))
	}
	switch {
	case generic(media):
		return classUnknown, ""
	case strings.HasPrefix(media, "text/"), textTypes[media], strings.HasSuffix(media, "+json"), strings.HasSuffix(media, "+xml"):
		return classText, media
	case imageTypes[media]:
		return classImage, media
	case media == "application/pdf":
		return classPDF, media
	default:
		return classOther, media
	}
}

type getObjectInput struct {
	Bucket    string `json:"bucket" jsonschema:"the bucket name"`
	Key       string `json:"key" jsonschema:"the object key"`
	VersionID string `json:"version_id,omitempty" jsonschema:"a version of the object; the current one when empty"`
	Offset    int64  `json:"offset,omitempty" jsonschema:"text objects only: the byte to read from, next_offset of the previous slice"`
}

func (in *getObjectInput) object() *objectInput {
	return &objectInput{Bucket: in.Bucket, Key: in.Key, VersionID: in.VersionID}
}

type textOutput struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type,omitempty"`
	Text        string `json:"text"`
	Size        int64  `json:"size"`
	Offset      int64  `json:"offset"`
	NextOffset  int64  `json:"next_offset,omitempty"`
	Truncated   bool   `json:"truncated"`
}

// metadataOutput is the answer for an object whose content is not returned, and why.
type metadataOutput struct {
	Content string `json:"content"`
	objectInfo
}

func jsonResult(out any) (*mcp.CallToolResult, error) {
	res, _, err := mcpserve.ToolResult(out, nil)
	return res, err
}

// Get returns the content of an object as get_object answers it, and whether its text was cut.
func (b *buckets) Get(ctx context.Context, in *getObjectInput) (*mcp.CallToolResult, bool, error) {
	if in.Offset < 0 {
		return nil, false, errors.New("offset must not be negative")
	}
	info, at, err := b.head(ctx, in.object())
	if err != nil {
		return nil, false, err
	}
	switch cls, media := classify(info.ContentType, info.ContentEncoding, info.Key); cls {
	case classImage, classPDF:
		res, err := b.binary(ctx, in, &info, at, cls, media)
		return res, false, err
	case classOther:
		res, err := jsonResult(metadataOutput{objectInfo: info, Content: "binary: not returned"})
		return res, false, err
	default:
		return b.text(ctx, in, &info, at, cls == classUnknown)
	}
}

// read reads at most limit bytes of the object of in, of the bytes rng only when not empty.
func (b *buckets) read(ctx context.Context, in *getObjectInput, rng string, limit int64, at func(*s3.Options)) ([]byte, error) {
	out, err := b.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(in.Bucket), Key: aws.String(in.Key), VersionId: optional(in.VersionID), Range: optional(rng),
	}, at)
	if err != nil {
		return nil, mcpserve.AWSError(err)
	}
	defer out.Body.Close() //nolint:errcheck // a read body.
	return io.ReadAll(io.LimitReader(out.Body, limit))
}

// binary returns an image or a PDF whole, with its metadata, or the metadata alone past maxBinaryBytes.
func (b *buckets) binary(ctx context.Context, in *getObjectInput, info *objectInfo, at func(*s3.Options), cls class, media string) (*mcp.CallToolResult, error) {
	if in.Offset != 0 {
		return nil, errors.New("offset applies to text objects only")
	}
	tooLarge := func(size int64) (*mcp.CallToolResult, error) {
		return jsonResult(metadataOutput{objectInfo: *info, Content: fmt.Sprintf("too large: %d bytes", size)})
	}
	if info.Size > maxBinaryBytes {
		return tooLarge(info.Size)
	}
	data, err := b.read(ctx, in, "", maxBinaryBytes+1, at)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBinaryBytes { // the object grew since HeadObject.
		return tooLarge(int64(len(data)))
	}
	// The Claude API rejects an empty or mislabelled image: only bytes that match are sent.
	if len(data) == 0 {
		return jsonResult(metadataOutput{objectInfo: *info, Content: "empty"})
	}
	if http.DetectContentType(data) != media {
		return jsonResult(metadataOutput{objectInfo: *info, Content: "binary: not returned"})
	}
	res, err := jsonResult(info)
	if err != nil {
		return nil, err
	}
	res.StructuredContent = nil // a client preferring it could drop the image.
	if cls == classImage {
		res.Content = append(res.Content, &mcp.ImageContent{Data: data, MIMEType: media})
	} else {
		res.Content = append(res.Content, &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
			URI: "s3://" + in.Bucket + "/" + in.Key, MIMEType: media, Blob: data,
		}})
	}
	return res, nil
}

// text returns a slice of maxTextBytes from in.Offset, cut back to whole runes. An unknown object
// whose slice is not UTF-8 is a binary: its metadata only.
func (b *buckets) text(ctx context.Context, in *getObjectInput, info *objectInfo, at func(*s3.Options), sniff bool) (*mcp.CallToolResult, bool, error) {
	out := textOutput{Key: info.Key, ContentType: info.ContentType, Size: info.Size, Offset: in.Offset}
	if in.Offset >= info.Size {
		res, err := jsonResult(out)
		return res, false, err
	}
	data, err := b.read(ctx, in, fmt.Sprintf("bytes=%d-%d", in.Offset, in.Offset+maxTextBytes-1), maxTextBytes, at)
	if err != nil {
		return nil, false, err
	}
	more := in.Offset+int64(len(data)) < info.Size
	start, end := runeStart(data), len(data)
	if more {
		end = completeRunes(data)
	}
	if start > end {
		start = end
	}
	if sniff && !utf8.Valid(data[start:end]) {
		res, jsonErr := jsonResult(metadataOutput{objectInfo: *info, Content: "binary: not returned"})
		return res, false, jsonErr
	}
	out.Offset, out.Text = in.Offset+int64(start), string(data[start:end])
	if more {
		out.Truncated, out.NextOffset = true, in.Offset+int64(end)
	}
	res, err := jsonResult(out)
	return res, out.Truncated, err
}

// runeStart is the number of bytes data starts with that end a rune begun before it.
func runeStart(data []byte) int {
	n := 0
	for n < len(data) && n < utf8.UTFMax-1 && !utf8.RuneStart(data[n]) {
		n++
	}
	return n
}

// completeRunes is the length of data without a rune cut at its end.
func completeRunes(data []byte) int {
	for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
		if utf8.RuneStart(data[i]) {
			if utf8.FullRune(data[i:]) {
				return len(data)
			}
			return i
		}
	}
	return len(data)
}
