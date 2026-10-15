package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// reasonNoPassword refuses a password sign-in without a password in the keychain.
const reasonNoPassword = "no saved password for this connection: ask the user to open it once from the airlock dashboard with Remember"

// defaultStartTimeout bounds how long open_session waits for the session to run.
const defaultStartTimeout = 3 * time.Minute

var openableTypes = []string{"rds", "eks", "elasticache", sessions.DynamoDBResourceType, sessions.S3ResourceType, sessions.DirectResourceType, sessions.DockerResourceType, sessions.DockerCacheResourceType}

type openInput struct {
	Type    string `json:"type" jsonschema:"rds, eks, elasticache, dynamodb, s3, direct, docker or docker-cache"`
	Name    string `json:"name" jsonschema:"the target name, as list_targets gives it"`
	Profile string `json:"profile,omitempty" jsonschema:"AWS profile; needed when the target is not a saved connection, or is saved in several profiles"`
	Region  string `json:"region,omitempty" jsonschema:"AWS region; defaults to the saved connection's, else the profile's"`
	Reason  string `json:"reason,omitempty" jsonschema:"why the session is needed, shown to the user"`
	Writes  bool   `json:"writes,omitempty" jsonschema:"ask for the tools modifying data (always approved by the user)"`
}

type openOutput struct {
	Tools   string `json:"tools"`
	MCP     string `json:"mcp,omitempty"`
	Session int    `json:"session"`
	Reused  bool   `json:"reused"`
	Writes  bool   `json:"writes"`
}

// target is a resolved open_session target.
type target struct {
	typ     string
	name    string
	profile string
	region  string
	prefs   jumphost.ConnectionPrefs
	env     environment.Env
}

func (t *toolset) openSession(ctx context.Context, req *mcp.CallToolRequest, in openInput) (*mcp.CallToolResult, any, error) { //nolint:gocritic // signature fixed by mcp.ToolHandlerFor.
	agent := agentName(req)
	settings := t.settings()
	// The agent's text reaches the dashboard, the session log and daemon.log: no control sequence.
	in.Name, in.Profile, in.Region = cleanText(in.Name, maxName), cleanText(in.Profile, maxName), cleanText(in.Region, maxName)
	in.Reason = cleanText(in.Reason, maxReason)
	audit := &mcpserve.ToolEvent{Time: t.now(), Server: t.name, Tool: "open_session", Target: in.Name, Profile: in.Profile, Region: in.Region, Access: accessLabel(in.Writes)}
	refuse := func(format string, args ...any) (*mcp.CallToolResult, any, error) {
		text := fmt.Sprintf(format, args...)
		if audit.Decision == "approved" {
			audit.Decision = "approved, then refused: " + text
		} else {
			audit.Decision = "refused: " + text
		}
		t.record(audit)
		return refusal("%s", text), nil, nil
	}
	discovery, err := t.discovery()
	if err != nil {
		return refuse("airlock cannot open targets: %v", err)
	}
	tg, err := t.resolve(ctx, discovery, &settings, &in)
	if err != nil {
		return refuse("%v", err)
	}
	audit.Target, audit.Profile, audit.Region = tg.name, tg.profile, tg.region
	if in.Writes && !settings.AgentWrites {
		return refuse("%s", reasonWritesOff)
	}
	if running := t.running(ctx, &tg); running != nil {
		if in.Writes && !running.MCPWrites() {
			return refuse("session %d runs %s read-only: close it (close_session, or ask the user) and open it again with writes", running.ID, tg.name)
		}
		audit.Decision = "reused"
		t.record(audit)
		return jsonResult(openOutput{Session: int(running.ID), Reused: true, Writes: running.MCPWrites(), Tools: toolsOf(running), MCP: running.Details.MCP})
	}

	var grant sessions.Grant
	switch decision := Decide(in.Writes, &settings, tg.env.Production, t.manager.Views()); decision.Verdict {
	case Refuse:
		return refuse("%s", decision.Reason)
	case Prompt:
		grant, err = t.manager.RequestApproval(ctx, sessions.Approval{
			Agent: agent, Reason: in.Reason, Type: tg.typ, Target: tg.name, Profile: tg.profile, Region: tg.region,
			Environment: tg.env.Name, Production: tg.env.Production, Writes: in.Writes,
		})
		switch {
		case errors.Is(err, sessions.ErrNoView):
			return refuse("%s", reasonNoView)
		case errors.Is(err, sessions.ErrApprovalDenied), errors.Is(err, sessions.ErrApprovalExpired):
			return refuse("%v", err)
		case err != nil:
			return refuse("the request was withdrawn: %v", err)
		}
		audit.Decision = "approved"
	default:
		audit.Decision = "auto"
	}

	options, err := t.options(ctx, discovery, &tg, agent, in.Writes)
	if err != nil {
		releaseGrant(grant)
		return refuse("%v", err)
	}
	options.Unattended = grant.Interactive == nil
	id, err := t.manager.Open(ctx, sessions.OpenRequest{Options: options, Interactive: grant.Interactive})
	releaseGrant(grant)
	if err != nil {
		return refuse("%s", openFailure(err, &tg))
	}
	t.relay.touch(id)
	how := "without asking (agentAutoConnect)"
	if audit.Decision == "approved" {
		how = "approved in a dashboard"
	}
	line := fmt.Sprintf("Opened by %s through the MCP gateway, %s", agent, how)
	if in.Reason != "" {
		line += ": " + in.Reason
	}
	_ = t.manager.Log(id, line) //nolint:errcheck // the session may already be gone.
	session, err := t.waitReady(ctx, id)
	if err != nil {
		return refuse("%v", err)
	}
	t.record(audit)
	return jsonResult(openOutput{Session: int(id), Writes: session.MCPWrites(), Tools: toolsOf(session), MCP: session.Details.MCP})
}

func releaseGrant(grant sessions.Grant) {
	if grant.Done != nil {
		grant.Done()
	}
}

func accessLabel(writes bool) string {
	if writes {
		return "writes"
	}
	return "read-only"
}

func toolsOf(s *sessions.Session) string {
	if s.MCP == nil {
		return "none"
	}
	if k := kindOf(s.ResourceType); k != "" {
		return string(k)
	}
	return "none"
}

// resolveDocker finds the running container of tg: no profile, no environment, no saved choices.
// A password the container does not give is refused here: no dashboard would ask it.
func (t *toolset) resolveDocker(ctx context.Context, tg *target, asked string) (target, error) {
	if t.containers == nil {
		return target{}, errors.New("docker databases and caches are unavailable: the docker CLI was not found")
	}
	c, found, err := t.containers.Get(ctx, tg.name)
	switch {
	case err != nil:
		return target{}, err
	case !found:
		return target{}, fmt.Errorf("no running database or cache container named %q; call list_targets", asked)
	case c.Problem != "":
		return target{}, fmt.Errorf("%s: %s", c.Name, c.Problem)
	case dockerType(&c) != tg.typ:
		return target{}, fmt.Errorf("%s is a %s container: use type %s", c.Name, c.Engine, dockerType(&c))
	case c.Password == "" && !c.Trust:
		return target{}, fmt.Errorf("the password of %s is unknown: ask the user to set %s on the container, or to open it from the airlock dashboard", c.Name, c.PasswordHint())
	}
	tg.profile, tg.region = "", ""
	return *tg, nil
}

// resolve finds the target of in: a direct database, or an AWS target from the saved connections,
// else from the profile in gives.
func (t *toolset) resolve(ctx context.Context, discovery Discovery, settings *Settings, in *openInput) (target, error) {
	tg := target{typ: strings.ToLower(strings.TrimSpace(in.Type)), name: strings.TrimSpace(in.Name),
		profile: strings.TrimSpace(in.Profile), region: strings.TrimSpace(in.Region)}
	if !slices.Contains(openableTypes, tg.typ) {
		return target{}, fmt.Errorf("unknown type %q (valid: %s)", in.Type, strings.Join(openableTypes, ", "))
	}
	if settings.disabled(tg.typ) {
		return target{}, fmt.Errorf("%s targets are disabled in airlock's config (disabledModules)", tg.typ)
	}
	if tg.name == "" {
		return target{}, errors.New("name is required: a target from list_targets")
	}
	if tg.typ == sessions.DockerResourceType || tg.typ == sessions.DockerCacheResourceType {
		return t.resolveDocker(ctx, &tg, in.Name)
	}
	saved, err := discovery.ListSavedConnections(ctx, tg.profile, tg.region)
	if err != nil {
		return target{}, fmt.Errorf("airlock cannot read the saved connections: %w", err)
	}
	if tg.typ == sessions.DirectResourceType {
		if t.databases == nil {
			return target{}, errors.New("the direct databases are unavailable: ask the user to check saved-connections.json")
		}
		db, found, getErr := t.databases.Get(tg.name)
		if getErr != nil {
			return target{}, getErr
		}
		if !found {
			return target{}, fmt.Errorf("no direct database named %q; call list_targets", in.Name)
		}
		tg.name = orName(db.Name, tg.name)
		tg.profile, tg.region, tg.env = "", "", settings.Environments.Lookup(db.Environment)
		for idx := range saved {
			if saved[idx].Ref.Type == tg.typ && strings.EqualFold(saved[idx].Ref.Name, tg.name) {
				tg.prefs = saved[idx].Prefs
			}
		}
		return tg, nil
	}
	var matches []jumphost.SavedConnection
	for idx := range saved {
		if conn := saved[idx]; conn.Ref.Type == tg.typ && strings.EqualFold(conn.Ref.Name, tg.name) && !conn.ProfileMissing {
			matches = append(matches, conn)
		}
	}
	switch len(matches) {
	case 0:
		if tg.profile == "" {
			return target{}, fmt.Errorf("%s %s is not a saved connection: pass its profile (see list_targets)", tg.typ, tg.name)
		}
	case 1:
		tg.name, tg.profile, tg.region, tg.prefs = matches[0].Ref.Name, matches[0].Prefs.Profile, matches[0].Ref.Region, matches[0].Prefs
	default:
		profiles := make([]string, 0, len(matches))
		for idx := range matches {
			profiles = append(profiles, matches[idx].Prefs.Profile)
		}
		slices.Sort(profiles)
		return target{}, fmt.Errorf("%s %s is saved in several profiles (%s): pass profile", tg.typ, tg.name, strings.Join(profiles, ", "))
	}
	tg.env = settings.Environments.Match(tg.profile)
	return tg, nil
}

func orName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

// running returns the running session of tg; nil when none.
func (t *toolset) running(ctx context.Context, tg *target) *sessions.Session {
	snapshot, err := t.manager.Snapshot(ctx)
	if err != nil {
		return nil
	}
	for idx := range snapshot.Sessions {
		s := &snapshot.Sessions[idx]
		if !s.Active() || s.ResourceType != tg.typ || !strings.EqualFold(s.Target, tg.name) {
			continue
		}
		if tg.typ == sessions.DirectResourceType || ((tg.profile == "" || s.Profile == tg.profile) && (tg.region == "" || s.Region == tg.region)) {
			return s
		}
	}
	return nil
}

// options are the options opening tg for agent, from its saved choices: user, sign-in method,
// secret, keychain password and local port, as a reconnection from the recent list.
func (t *toolset) options(ctx context.Context, discovery Discovery, tg *target, agent string, writes bool) (jumphost.Options, error) {
	o := jumphost.Options{
		ResourceType: tg.typ, Name: tg.name, AwsProfile: tg.profile, AwsRegion: tg.region,
		DBUser: tg.prefs.DBUser, DBSecretARN: tg.prefs.DBSecretARN, MCPAllowWrites: writes,
		SkipIDEDataSource: true, SkipMCPRegistration: true, OpenedBy: agent,
	}
	if tg.prefs.SignIn != "" {
		o.SignIn = tg.prefs.SignIn
	}
	if port := tg.prefs.LocalPort; port > 0 && discovery.CheckLocalPort(port) == nil {
		o.LocalPortNumber = port
	}
	if tg.typ != sessions.DirectResourceType && tg.prefs.SignInMethod() == jumphost.SignInPassword {
		password, ok := discovery.LoadKeychainPassword(ctx, o)
		switch {
		case !ok && tg.prefs.PasswordInKeychain:
			// The keychain entry is found through the AWS account: an expired sign-in hides it.
			return jumphost.Options{}, fmt.Errorf("AWS sign-in required (or the OS keychain did not answer): ask the user to run `aws sso login --profile %s`, then retry", tg.profile)
		case !ok:
			return jumphost.Options{}, errors.New(reasonNoPassword)
		}
		o.DBPassword = password
	}
	return o, nil
}

// openFailure explains a failed opening to the agent.
func openFailure(err error, tg *target) string {
	switch {
	case errors.Is(err, jumphost.ErrSignInRequired):
		return fmt.Sprintf("AWS sign-in required: ask the user to run `aws sso login --profile %s`, then retry", tg.profile)
	case errors.Is(err, jumphost.ErrPasswordRequired):
		return reasonNoPassword
	default:
		return "could not open " + tg.name + ": " + err.Error()
	}
}

// waitReady waits for session id to run with its sign-in settled; a sign-in choice nobody can make
// here stops it.
func (t *toolset) waitReady(ctx context.Context, id sessions.ID) (*sessions.Session, error) {
	timeout := t.startTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshot, events, err := t.manager.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	list := snapshot.Sessions
	for {
		if s := findSession(list, id); s != nil {
			done, readyErr := readiness(s)
			if readyErr != nil && s.Active() {
				_ = t.manager.Stop(context.WithoutCancel(ctx), id) //nolint:errcheck // reported through readyErr.
			}
			if done {
				return s, readyErr
			}
		}
		select {
		case ev, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("session %d is still starting; call list_sessions later", id)
			}
			if ev.Kind == sessions.EventResync {
				if snapshot, err = t.manager.Snapshot(ctx); err == nil {
					list = snapshot.Sessions
				}
				continue
			}
			list = sessions.Apply(list, &ev)
		case <-ctx.Done():
			return nil, fmt.Errorf("session %d is still starting; call list_sessions later", id)
		}
	}
}

func findSession(list []sessions.Session, id sessions.ID) *sessions.Session {
	for idx := range list {
		if list[idx].ID == id {
			return &list[idx]
		}
	}
	return nil
}

// readiness reports whether s is done starting, and why it failed.
func readiness(s *sessions.Session) (bool, error) {
	switch {
	case !s.Active():
		return true, fmt.Errorf("the session ended: %s", orName(s.Err, "stopped"))
	case !s.Ready:
		return false, nil
	case !s.HasSignIn:
		return true, nil
	}
	switch s.SignIn.State {
	case sessions.SignInChoosing:
		return true, errors.New("this target needs a choice of credentials: ask the user to open it once from the airlock dashboard")
	case sessions.SignInIdle, sessions.SignInListing, sessions.SignInChecking:
		return false, nil
	case sessions.SignInFailed:
		return true, fmt.Errorf("could not sign in to %s: %s", s.Target, orName(s.SignIn.Err, "the test query failed"))
	default:
		return true, nil
	}
}

func (t *toolset) closeSession(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_, id, err := splitSession(req.Params.Arguments)
	if err != nil {
		return refusal("%v", err), nil
	}
	audit := &mcpserve.ToolEvent{Time: t.now(), Server: t.name, Tool: "close_session"}
	snapshot, err := t.manager.Snapshot(ctx)
	if err != nil {
		return refusal("%v", err), nil
	}
	s := findSession(snapshot.Sessions, id)
	switch {
	case s == nil:
		return refusal("no session %d; call list_sessions", id), nil
	case s.OpenedBy == "":
		return refusal("session %d was opened by the user: only the user closes it", id), nil
	}
	audit.Target, audit.Profile, audit.Region = s.Target, s.Profile, s.Region
	if err = t.manager.Stop(ctx, s.ID); err != nil {
		return refusal("%v", err), nil
	}
	audit.Decision = "closed"
	t.record(audit)
	t.relay.forget(s.ID)
	res, _, err := jsonResult(map[string]any{"session": int(id), "closed": true})
	return res, err
}
