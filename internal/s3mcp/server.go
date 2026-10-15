package s3mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// serverVersion is reported to MCP clients.
	serverVersion = "1.0.0"
	// Engine is the engine of the tool events of S3 sessions.
	Engine = "s3"
	// Target is the one target of an S3 session: every bucket of its profile.
	Target = "buckets"
	// getTimeout bounds get_object, which may read a few megabytes.
	getTimeout = 60 * time.Second
)

// ServerConfig describes the buckets an MCP server gives access to.
type ServerConfig struct {
	Name        string
	Profile     string
	Region      string
	AllowWrites bool
}

// command is the readable command of a call: the operation and its non-empty parts; never content.
func command(op string, parts ...string) string {
	words := []string{op}
	for _, p := range parts {
		if p != "" {
			words = append(words, p)
		}
	}
	return strings.Join(words, " ")
}

func object(bucket, key string) string { return bucket + "/" + key }

func flag(on bool, word string) string {
	if on {
		return word
	}
	return ""
}

func versionOf(id string) string {
	if id == "" {
		return ""
	}
	return "v=" + id
}

func offsetOf(offset int64) string {
	if offset == 0 {
		return ""
	}
	return "@" + strconv.FormatInt(offset, 10)
}

// NewServer builds the MCP server of the buckets b; every call is passed to record. b may be nil
// for a server only listed (the gateway's prototype).
func NewServer(cfg *ServerConfig, b *buckets, record func(*mcpserve.ToolEvent)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := &mcp.ToolAnnotations{DestructiveHint: new(true)}
	access := "read-only"
	if cfg.AllowWrites {
		access = "read-write"
	}
	base := mcpserve.ToolEvent{Server: cfg.Name, Profile: cfg.Profile, Region: cfg.Region, Target: Target, Engine: Engine, Access: access}
	start := func(tool, cmd string) (*mcpserve.ToolEvent, func(int64, error)) {
		e, done := mcpserve.StartEvent(&base, tool, record)
		e.Command = cmd
		return e, done
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_buckets", Description: "List the S3 buckets of the account with their region, 100 per page.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listBucketsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("list_buckets", command("list_buckets", in.Prefix))
		out, err := b.List(ctx, &in)
		done(int64(len(out.Buckets)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_objects",
		Description: "List one level of a bucket under a prefix: objects (key, size, date, ETag) and sub-folders in prefixes. " +
			"With recursive, every key under the prefix. One page per call; continue with next as start.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listObjectsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		e, done := start("list_objects", command("list_objects", in.Bucket, in.Prefix, flag(in.Recursive, "recursive")))
		out, err := b.Objects(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(len(out.Objects)+len(out.Prefixes)), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "head_object",
		Description: "Read the metadata of an object: size, content type and encoding, date, ETag, storage class, version and user metadata.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in objectInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("head_object", command("head_object", object(in.Bucket, in.Key), versionOf(in.VersionID)))
		out, err := b.Head(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_object",
		Description: "Read an object: text in slices of 256 KB (continue with next_offset as offset), PNG, JPEG, GIF and WebP " +
			"images and PDF documents up to 3.75 MB, the metadata only for other binaries and larger files.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getObjectInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, getTimeout)
		defer cancel()
		e, done := start("get_object", command("get_object", object(in.Bucket, in.Key), versionOf(in.VersionID), offsetOf(in.Offset)))
		res, truncated, err := b.Get(ctx, &in)
		e.Truncated = truncated
		done(1, err)
		if err != nil {
			return nil, nil, err
		}
		return res, nil, nil
	})

	if !cfg.AllowWrites {
		return server
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "put_object",
		Description: "Create or replace one object with text content. Confirm with the user first; pass if_none_match * to " +
			"create only, or if_match with the ETag from head_object, unless replacing is intended.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in putObjectInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("put_object", command("put_object", object(in.Bucket, in.Key)))
		out, err := b.Put(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_object", Description: "Delete one object, or one version of it. Confirm with the user first.", Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in objectInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, done := start("delete_object", command("delete_object", object(in.Bucket, in.Key), versionOf(in.VersionID)))
		out, err := b.Delete(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(out, err)
	})
	return server
}

func instructions(cfg *ServerConfig) string {
	access := "Access is read-only."
	if cfg.AllowWrites {
		access = "WRITES ARE ENABLED through put_object and delete_object: always confirm with the user before modifying data."
	}
	return fmt.Sprintf(
		"Amazon S3 buckets of AWS profile %s, in every region, reached through the AWS API (no tunnel). %s "+
			"Use list_buckets, then list_objects to browse a bucket one level at a time (recursive lists every key under a "+
			"prefix), head_object for the metadata of an object and get_object for its content: text in slices of 256 KB "+
			"(pass next_offset as offset for the next one), images and PDFs whole up to 3.75 MB, the metadata only for other "+
			"binaries. Listings return one page per call: pass next as start for the next one.",
		cfg.Profile, access,
	)
}
