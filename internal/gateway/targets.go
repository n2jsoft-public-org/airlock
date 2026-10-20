package gateway

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// Discovery lists and resolves the targets; *jumphost.Runner implements it.
type Discovery interface {
	ListAWSProfileSummaries(ctx context.Context) ([]jumphost.AWSProfileSummary, error)
	ListSavedConnections(ctx context.Context, profile, region string) ([]jumphost.SavedConnection, error)
	SearchResources(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error)
	LoadKeychainPassword(ctx context.Context, options jumphost.Options) (string, bool)
	CheckLocalPort(port int) error
}

// toolset serves the gateway's own tools.
type toolset struct {
	manager   *sessions.Manager
	relay     *relay
	settings  func() Settings
	discovery func() (Discovery, error)
	// databases finds the direct databases; nil when they are unavailable.
	databases jumphost.DirectDatabases
	// containers finds the database containers of the local Docker; nil when unavailable.
	containers jumphost.DockerContainers
	// record writes an audit event of the gateway tools.
	record func(*mcpserve.ToolEvent)
	now    func() time.Time
	// name is the gateway's server name, in its audit events.
	name string
	// startTimeout bounds how long open_session waits for the session to run.
	startTimeout time.Duration
}

// add declares the gateway tools on server.
func (t *toolset) add(server *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_targets", Annotations: readOnly,
		Description: "List what open_session can open. Without profile: the saved connections, direct databases and local Docker databases and caches, " +
			"no AWS call. With profile: a live search of that AWS profile. Each target says whether it opens at once " +
			"(opens: auto) or after the user approves it in the airlock dashboard (opens: prompt).",
	}, t.listTargets)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_sessions", Annotations: readOnly,
		Description: "List the running airlock sessions: their id (the session argument of the db_, k8s_, cache_, dynamo_ and s3_ tools), " +
			"target, who opened them, whether they allow writes, and which tools apply (tools: db, k8s, cache, dynamo, s3 or none).",
	}, t.listSessions)
	mcp.AddTool(server, &mcp.Tool{
		Name: "open_session",
		Description: "Open a target listed by list_targets and return its session id once it runs. Depending on the " +
			"user's settings it opens at once or waits (up to 2 minutes) for the user to approve it in the airlock " +
			"dashboard. Give a short reason: the user reads it. writes asks for tools that modify data; it is always " +
			"approved by the user, when the user allows agents to ask at all.",
	}, t.openSession)
	// close_session takes its session as the relayed tools do: a quoted id reads as a number.
	closeSchema, _ := withSession(map[string]any{}) //nolint:errcheck // a literal schema.
	server.AddTool(&mcp.Tool{
		Name: "close_session", Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true)}, InputSchema: closeSchema,
		Description: "Close a session an agent opened, once done with it. Sessions the user opened stay open.",
	}, t.closeSession)
}

type listTargetsInput struct {
	Profile string `json:"profile,omitempty" jsonschema:"AWS profile to search live; without it the saved connections are listed"`
	Type    string `json:"type,omitempty" jsonschema:"only targets of this type: rds, eks, elasticache, dynamodb, s3 or direct"`
	Query   string `json:"query,omitempty" jsonschema:"only targets whose name contains this text"`
}

type targetInfo struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Profile     string `json:"profile,omitempty"`
	Region      string `json:"region,omitempty"`
	Description string `json:"description,omitempty"`
	// Opens is auto, prompt, or "refused: <reason>", for a read-only opening.
	Opens      string `json:"opens"`
	Production bool   `json:"production"`
	Saved      bool   `json:"saved"`
}

type targetsOutput struct {
	Failures map[string]string `json:"failures,omitempty"`
	Targets  []targetInfo      `json:"targets"`
	Profiles []string          `json:"profiles"`
}

func (t *toolset) listTargets(ctx context.Context, _ *mcp.CallToolRequest, in listTargetsInput) (*mcp.CallToolResult, any, error) {
	discovery, err := t.discovery()
	if err != nil {
		return refusal("airlock cannot list targets: %v", err), nil, nil
	}
	settings := t.settings()
	views := t.manager.Views()
	out := targetsOutput{Targets: []targetInfo{}, Profiles: []string{}}
	if profiles, profilesErr := discovery.ListAWSProfileSummaries(ctx); profilesErr == nil {
		for _, p := range profiles {
			out.Profiles = append(out.Profiles, p.Name)
		}
	}
	profile := strings.TrimSpace(in.Profile)
	wantType, query := strings.ToLower(strings.TrimSpace(in.Type)), strings.ToLower(strings.TrimSpace(in.Query))
	if wantType != "" && !slices.Contains(openableTypes, wantType) {
		return refusal("unknown type %q (valid: %s)", in.Type, strings.Join(openableTypes, ", ")), nil, nil
	}
	keep := func(targetType, name string) bool {
		return (wantType == "" || wantType == targetType) && !settings.disabled(targetType) && (query == "" || strings.Contains(strings.ToLower(name), query))
	}
	saved, err := discovery.ListSavedConnections(ctx, profile, "")
	if err != nil {
		return refusal("airlock cannot read the saved connections: %v", err), nil, nil
	}
	if profile == "" {
		for idx := range saved {
			conn := &saved[idx]
			if conn.ProfileMissing || conn.TargetMissing || !keep(conn.Ref.Type, conn.Ref.Name) {
				continue
			}
			production := t.savedProduction(conn, &settings)
			target := targetInfo{
				Type: conn.Ref.Type, Name: conn.Ref.Name, Profile: conn.Prefs.Profile, Region: conn.Ref.Region,
				Saved: true, Production: production, Opens: opens(&settings, production, views),
			}
			if conn.Ref.Type == sessions.DirectResourceType {
				// Reached outside AWS: the region of its saved entry is a placeholder.
				target.Profile, target.Region = "", ""
			}
			out.Targets = append(out.Targets, target)
		}
		if t.containers != nil && (wantType == "" || wantType == sessions.DockerResourceType || wantType == sessions.DockerCacheResourceType) {
			t.addDockerTargets(ctx, &out, &settings, views, keep)
		}
		return jsonResult(out)
	}
	search := jumphost.Options{AwsProfile: profile, Unattended: true}
	found, failures, err := discovery.SearchResources(ctx, &search)
	if err != nil {
		return signInFailure(err, profile), nil, nil
	}
	production := settings.Environments.Match(profile).Production
	for _, r := range found {
		if !keep(r.Type, r.Name) {
			continue
		}
		out.Targets = append(out.Targets, targetInfo{
			Type: r.Type, Name: r.Name, Description: r.Description, Profile: profile, Region: search.AwsRegion,
			Saved: isSaved(saved, r.Type, r.Name), Production: production, Opens: opens(&settings, production, views),
		})
	}
	if len(failures) > 0 {
		out.Failures = map[string]string{}
		for resourceType, failure := range failures {
			out.Failures[resourceType] = failure.Error()
		}
	}
	return jsonResult(out)
}

// dockerType is the session type of container c: docker-cache for Redis / Valkey, docker otherwise.
func dockerType(c *dockerdb.Container) string {
	if c.Cache() {
		return sessions.DockerCacheResourceType
	}
	return sessions.DockerResourceType
}

// addDockerTargets lists the database containers open_session can open; a discovery failure goes
// to out.Failures, listing none.
func (t *toolset) addDockerTargets(ctx context.Context, out *targetsOutput, settings *Settings, views int, keep func(targetType, name string) bool) {
	containers, err := t.containers.List(ctx)
	if err != nil {
		if out.Failures == nil {
			out.Failures = map[string]string{}
		}
		out.Failures[sessions.DockerResourceType] = err.Error()
		return
	}
	for idx := range containers {
		c := &containers[idx]
		typ := dockerType(c)
		if c.Problem != "" || !keep(typ, c.Name) {
			continue
		}
		out.Targets = append(out.Targets, targetInfo{
			Type: typ, Name: c.Name, Description: c.Description(), Opens: opens(settings, false, views),
		})
	}
}

// savedProduction reports whether a saved connection reaches production.
func (t *toolset) savedProduction(conn *jumphost.SavedConnection, settings *Settings) bool {
	if conn.Ref.Type != sessions.DirectResourceType {
		return settings.Environments.Match(conn.Prefs.Profile).Production
	}
	if t.databases == nil {
		return false
	}
	db, found, err := t.databases.Get(conn.Ref.Name)
	return err == nil && found && settings.Environments.Lookup(db.Environment).Production
}

func isSaved(saved []jumphost.SavedConnection, resourceType, name string) bool {
	for idx := range saved {
		if saved[idx].Ref.Type == resourceType && strings.EqualFold(saved[idx].Ref.Name, name) {
			return true
		}
	}
	return false
}

// opens tells how a read-only opening would go under settings.
func opens(settings *Settings, production bool, views int) string {
	switch decision := Decide(false, settings, production, views); decision.Verdict {
	case Auto:
		return "auto"
	case Prompt:
		return "prompt"
	default:
		return "refused: " + decision.Reason
	}
}

// signInFailure is the refusal of an AWS call that failed on profile.
func signInFailure(err error, profile string) *mcp.CallToolResult {
	if errors.Is(err, jumphost.ErrSignInRequired) {
		return refusal("AWS sign-in required: ask the user to sign in to profile %s again from airlock, then retry", profile)
	}
	return refusal("%v", err)
}

type sessionInfo struct {
	Type     string `json:"type"`
	Target   string `json:"target"`
	Profile  string `json:"profile,omitempty"`
	Region   string `json:"region,omitempty"`
	State    string `json:"state"`
	OpenedBy string `json:"openedBy"`
	// Tools is the prefix of the relayed tools that apply: db, k8s, cache, dynamo, s3, or none.
	Tools string `json:"tools"`
	About string `json:"about,omitempty"`
	// MCP is the dashboard's MCP line: it tells why no server runs.
	MCP        string `json:"mcp,omitempty"`
	Session    int    `json:"session"`
	Production bool   `json:"production"`
	Writes     bool   `json:"writes"`
}

func (t *toolset) listSessions(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	snapshot, err := t.manager.Snapshot(ctx)
	if err != nil {
		return refusal("%v", err), nil, nil
	}
	settings := t.settings()
	out := struct {
		Sessions []sessionInfo `json:"sessions"`
	}{Sessions: []sessionInfo{}}
	list := snapshot.Sessions
	sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for idx := range list {
		s := &list[idx]
		info := sessionInfo{
			Session: int(s.ID), Type: s.ResourceType, Target: s.Target, Profile: s.Profile, Region: s.Region,
			State: stateName(s.State), OpenedBy: openedBy(s), Production: sessions.EnvironmentOf(settings.Environments, s).Production,
			Writes: s.MCPWrites(), MCP: s.Details.MCP, Tools: "none",
		}
		if access, accessErr := t.manager.MCPServer(s.ID); accessErr == nil {
			info.Tools = string(kindOf(access.ResourceType))
			info.About = t.relay.instructions(s.ID, access.Server)
		}
		out.Sessions = append(out.Sessions, info)
	}
	return jsonResult(out)
}

func openedBy(s *sessions.Session) string {
	if s.OpenedBy == "" {
		return "you"
	}
	return s.OpenedBy
}

func stateName(state sessions.State) string {
	switch state {
	case sessions.StateStarting:
		return "starting"
	case sessions.StateRunning:
		return "running"
	case sessions.StateStopping:
		return "stopping"
	default:
		return "ended"
	}
}
