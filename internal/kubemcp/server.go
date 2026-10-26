package kubemcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// serverVersion is reported to MCP clients.
const serverVersion = "1.0.0"

// ServerConfig describes the cluster an MCP server gives access to.
type ServerConfig struct {
	Name    string
	Target  string
	Profile string
	Region  string
}

// cluster is what the tools read; apiCluster in production.
type cluster interface {
	Namespaces(ctx context.Context) ([]namespaceSummary, error)
	ResourceTypes(ctx context.Context) ([]resourceType, error)
	List(ctx context.Context, in *listResourcesInput) (listResourcesOutput, error)
	Get(ctx context.Context, in *getResourceInput) (map[string]any, error)
	Logs(ctx context.Context, in *podLogsInput) (podLogsOutput, error)
	Events(ctx context.Context, in *listEventsInput) (events []eventSummary, more bool, err error)
}

type namespacesOutput struct {
	Namespaces []namespaceSummary `json:"namespaces"`
}

type resourceTypesOutput struct {
	Types []resourceType `json:"types"`
}

type eventsOutput struct {
	// Note is set when the cluster has more events than list_events sorts.
	Note   string         `json:"note,omitempty"`
	Events []eventSummary `json:"events"`
}

// NewServer builds the read-only MCP server of a cluster; every call is passed to record.
func NewServer(cfg *ServerConfig, c cluster, record func(*mcpserve.ToolEvent)) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions(cfg)},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	base := mcpserve.ToolEvent{
		Server: cfg.Name, Profile: cfg.Profile, Region: cfg.Region, Target: cfg.Target, Engine: "kubernetes", Access: "read-only",
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_namespaces", Description: "List the namespaces of the cluster with their phase.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		_, done := mcpserve.StartEvent(&base, "list_namespaces", record)
		namespaces, err := c.Namespaces(ctx)
		done(int64(len(namespaces)), err)
		if namespaces == nil {
			namespaces = []namespaceSummary{}
		}
		return mcpserve.ToolResult(namespacesOutput{Namespaces: namespaces}, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_resource_types",
		Description: "List the resource types this cluster serves (built-in and custom), with group, version, kind and short names.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		_, done := mcpserve.StartEvent(&base, "list_resource_types", record)
		types, err := c.ResourceTypes(ctx)
		done(int64(len(types)), err)
		if types == nil {
			types = []resourceType{}
		}
		return mcpserve.ToolResult(resourceTypesOutput{Types: types}, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_resources",
		Description: "List objects of one resource type, summarized (name, namespace, age, status). Use get_resource for a full object.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listResourcesInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		e, done := mcpserve.StartEvent(&base, "list_resources", record)
		e.Namespace, e.Resource = in.Namespace, in.Resource
		out, err := c.List(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(len(out.Items)), err)
		if out.Items == nil {
			out.Items = []itemSummary{}
		}
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_resource",
		Description: "Get one object as JSON (managed fields removed). Secret values are always <redacted>.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getResourceInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		e, done := mcpserve.StartEvent(&base, "get_resource", record)
		e.Namespace, e.Resource, e.Name = in.Namespace, in.Resource, in.Name
		obj, err := c.Get(ctx, &in)
		done(1, err)
		return mcpserve.ToolResult(obj, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "pod_logs",
		Description: fmt.Sprintf("Read the last lines of a container's logs (default %d, max %d lines, at most 256 KiB).", defaultTailLines, maxTailLines),
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in podLogsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		e, done := mcpserve.StartEvent(&base, "pod_logs", record)
		e.Namespace, e.Resource, e.Name = in.Namespace, "pods", in.Pod
		out, err := c.Logs(ctx, &in)
		e.Truncated = out.Truncated
		done(int64(strings.Count(out.Log, "\n")), err)
		return mcpserve.ToolResult(out, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_events", Description: "List recent events, newest first, optionally of one object.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		e, done := mcpserve.StartEvent(&base, "list_events", record)
		e.Namespace, e.Resource, e.Name = in.Namespace, "events", in.Name
		events, more, err := c.Events(ctx, &in)
		done(int64(len(events)), err)
		if events == nil {
			events = []eventSummary{}
		}
		out := eventsOutput{Events: events}
		if more {
			out.Note = moreEventsNote
		}
		return mcpserve.ToolResult(out, err)
	})

	return server
}

func instructions(cfg *ServerConfig) string {
	return fmt.Sprintf(
		"Kubernetes cluster %s (Amazon EKS), reached through an airlock SSM tunnel. AWS profile %s, region %s. "+
			"Access is read-only: nothing can be changed. Secret values are always replaced with %s. "+
			"Start with list_namespaces or list_resource_types, then list_resources (summaries) and get_resource (one full object). "+
			"pod_logs reads container logs; list_events shows recent events, newest first.",
		cfg.Target, cfg.Profile, cfg.Region, redactedValue,
	)
}
