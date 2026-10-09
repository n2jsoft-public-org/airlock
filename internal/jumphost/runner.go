package jumphost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"

	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/dbuser"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/ideadatasource"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/kubeconfig"
	"github.com/n2jsoft/airlock/internal/kubemcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/platform"
	"golang.org/x/term"
)

const (
	resourceEKS         = "eks"
	resourceRDS         = "rds"
	resourceElastiCache = "elasticache"
	defaultAwsProfile   = "default"
	defaultEKSLocalPort = 9444
	defaultRedisPort    = 6379
	// defaultJumphostTagKey and defaultJumphostTagValue find the jumphost when no tag is configured.
	defaultJumphostTagKey   = "Usage"
	defaultJumphostTagValue = "jumphost"
)

// supportedResourceType reports whether resourceType (lower case) is one airlock connects to.
func supportedResourceType(resourceType string) bool {
	return resourceType == resourceEKS || resourceType == resourceRDS || resourceType == resourceElastiCache
}

type Options struct {
	ResourceType string
	Name         string
	AwsProfile   string
	AwsRegion    string
	// DBUser is the database user signing in with AWS IAM on RDS (defaults to the engine's user of
	// RunnerOptions.DefaultDBUser).
	DBUser string
	// DBPassword signs DBUser in with a password instead of an AWS IAM token. It is only kept in
	// memory (the MCP server uses it; the IDE asks for it) and is never written to disk.
	DBPassword string
	// SignIn is the method chosen in the wizard (SignIn*); empty derives it from DBSecretARN and DBPassword.
	SignIn string
	// DBSecretARN names the Secrets Manager secret holding the password (ARN or name, from the
	// wizard or --db-secret).
	DBSecretARN string
	// ProjectDir is where the IDE data source and the MCP client files of this opening are written;
	// empty uses RunnerOptions.ProjectDir, else the working directory.
	ProjectDir string `json:"projectDir,omitempty"`
	// OpenedBy names the AI agent that opened the session through the MCP gateway; empty for the user.
	OpenedBy        string `json:"openedBy,omitempty"`
	LocalPortNumber int
	// SkipIDEDataSource disables registering the connection in ./.idea/dataSources.xml.
	SkipIDEDataSource bool
	// SkipMCP disables the MCP server giving AI assistants access to the tunneled database, cluster or cache.
	SkipMCP bool
	// MCPAllowWrites exposes the MCP tools modifying data (databases and caches; EKS is always read-only). PostgreSQL writes as DBUser; SQL Server, without
	// AWS IAM sign-in, uses the master user secret.
	MCPAllowWrites bool
	// RememberPassword keeps DBPassword in the OS keychain for the next connections.
	RememberPassword bool
	// Unattended never signs in interactively, as nobody may be there to answer (automatic
	// reconnections): missing or expired credentials fail with ErrSignInRequired.
	Unattended bool
	// SkipMCPRegistration serves the MCP server without writing it in the AI client files: the
	// gateway relays it.
	SkipMCPRegistration bool `json:"skipMCPRegistration,omitempty"`
}

// ErrSignInRequired reports AWS credentials that only an interactive sign-in can renew.
var ErrSignInRequired = errors.New("AWS sign-in required")

// SignInMethod returns how the connection signs in to an RDS database.
func (o *Options) SignInMethod() string {
	switch {
	case o.SignIn != "":
		return o.SignIn
	case strings.TrimSpace(o.DBSecretARN) != "":
		return SignInSecret
	case o.DBPassword != "":
		return SignInPassword
	default:
		return SignInIAM
	}
}

type ResourceSummary struct {
	Name        string
	Description string
	// Type is the resource type (rds, eks, elasticache) the summary was listed as.
	Type string
	// Engine is the RDS engine (postgres, sqlserver-se, ...) or the ElastiCache engine (redis, valkey, memcached); empty for EKS.
	Engine string
	// CacheAuth is how an ElastiCache target signs clients in (CacheAuth*); empty for other types.
	CacheAuth string
	// IAMAuth reports whether the RDS instance accepts AWS IAM authentication.
	IAMAuth bool
}

type AWSProfileSummary struct {
	Name        string
	Description string
}

type RunnerOptions struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Keychain remembers the passwords the user asks to keep; nil disables it. It only works
	// together with PrefsPath, which records which targets have one.
	Keychain keychain.Store
	// Databases finds the direct databases; nil disables them.
	Databases DirectDatabases
	// Containers finds the database containers of the local Docker; nil disables them.
	Containers DockerContainers
	// Environments resolves the environment of direct databases.
	Environments environment.Set
	// Registry tracks ports and targets of parallel sessions; share it between runners of one process.
	Registry *SessionRegistry
	// MCPHost serves the MCP servers of database sessions; share it between runners of one process.
	MCPHost *mcpserve.Host
	// Interactive runs a command needing the user's terminal (aws sso login), purpose saying why;
	// nil runs it on In, Out and Err.
	Interactive InteractiveFunc
	// JumphostTag finds the SSM jumphost instance (Usage=jumphost when zero).
	JumphostTag awstag.Tag
	// ReadOnlySecretTag marks the read-only secrets of SQL Server instances; zero disables them.
	ReadOnlySecretTag awstag.Tag
	// RandomLocalPort binds tunnels to a free port chosen by the OS instead of the target's port.
	RandomLocalPort bool
	// MCPLogDir receives the audit log of the SQL run through MCP servers; empty disables it.
	MCPLogDir string
	// ProjectDir is where the IntelliJ .idea directory is looked up (defaults to the working directory).
	ProjectDir string
	// PrefsPath is the saved-connections.json remembering the Connect step choices per target; empty disables it.
	PrefsPath string
	// DefaultRegion is used after AWS_REGION and the profile's region; empty means none.
	DefaultRegion string
	// DefaultDBUser are the users per engine signing in on RDS when none is chosen; with none for the
	// engine, one is required.
	DefaultDBUser dbuser.Defaults
	// MCPServerPrefix starts MCP server names (mcpserve.DefaultServerPrefix when empty).
	MCPServerPrefix string
	// KubeconfigDir receives the kubeconfig of each EKS session; empty disables them.
	KubeconfigDir string
}

type SessionDetails struct {
	ConnectedServer string
	RestartCommand  string
	IDEDataSource   string
	// Auth describes how the database is signed in to, e.g. "AWS IAM token · app_ro" (empty when not applicable).
	Auth string
	// MCP describes the session's MCP server (empty when none runs).
	MCP string
	// Kubeconfig is the kubeconfig written for an EKS session (empty when none).
	Kubeconfig string
	// TokenCommand prints a temporary AWS IAM database token (empty without AWS IAM sign-in).
	TokenCommand string
}

// Session is a prepared port-forwarding session: its local port and target are reserved
// in the registry until Release is called.
type Session struct {
	mcp mcpserve.Session
	// SignIn resolves and checks the database credentials once the tunnel is up; nil when airlock
	// cannot query the session's database.
	SignIn  SignInChecker
	release func() string
	// ReadCredentials reads the database credentials of the session from Secrets Manager, again on
	// each call: the picked secret, or the read-only secrets of each database; nil when the session
	// signs in otherwise.
	ReadCredentials func(ctx context.Context) ([]DBCredentials, error)
	Details         SessionDetails
	releaseMsg      string
	Label           string
	// ResourceType (eks, rds, elasticache), Target, Profile and Region describe the connection for display.
	ResourceType string
	Target       string
	Profile      string
	Region       string
	AwsArgs      []string
	CommandArgs  []string
	// Intro describes the connection (display name, connection hints, IDE data source).
	Intro       []string
	LocalPort   int
	releaseOnce sync.Once
	// Environment is the environment of a direct database's definition, and Production whether it
	// is the production environment (AWS sessions are matched by profile in the dashboard).
	Environment string
	Production  bool
}

// MCPServer is the session's running MCP server, nil when none runs.
func (s *Session) MCPServer() mcpserve.Session {
	return s.mcp
}

// SetMCPServer makes server the session's MCP server: for the fakes of the runner (sessionstest).
func (s *Session) SetMCPServer(server mcpserve.Session) {
	s.mcp = server
}

// Release frees the session's port and target and removes its IDE data source.
// It returns a message describing the IDE cleanup (empty when nothing was done). Safe to call repeatedly.
func (s *Session) Release() string {
	s.releaseOnce.Do(func() {
		if s.release != nil {
			s.releaseMsg = s.release()
		}
	})
	return s.releaseMsg
}

type Runner struct {
	in       io.Reader
	out      io.Writer
	err      io.Writer
	registry *SessionRegistry
	mcpHost  *mcpserve.Host
	// prefs is nil when connection preferences are not remembered.
	prefs *PrefsStore
	// keychain is nil when passwords cannot be remembered; keychainOK caches its availability.
	keychain keychain.Store
	// accounts caches the AWS account ID of each profile, learned from the identity check;
	// accountsMu guards it.
	accounts map[string]string
	// databases finds the direct databases; nil when they are disabled.
	databases DirectDatabases
	// containers finds the database containers of the local Docker; nil when unavailable.
	containers DockerContainers
	// environments resolves the environment of direct databases.
	environments environment.Set
	interactive  InteractiveFunc
	projectDir   string
	mcpLogDir    string
	// defaultRegion and defaultDBUser are used when none is given; empty when not configured.
	defaultRegion string
	defaultDBUser dbuser.Defaults
	// jumphostTag finds the SSM jumphost instance; readOnlySecretTag the read-only SQL Server secrets.
	jumphostTag       awstag.Tag
	readOnlySecretTag awstag.Tag
	mcpServerPrefix   string
	randomLocalPort   bool
	// kubeconfigDir receives the kubeconfig of EKS sessions; empty disables them.
	kubeconfigDir string
	accountsMu    sync.Mutex
	// loginMu serializes the SSO sign-ins of expired identities.
	loginMu      sync.Mutex
	keychainOnce sync.Once
	keychainOK   bool
}

// attachMCP records the MCP server of session and its display line; a server that did not start
// (nil, typed or not) only leaves its display, so MCPServer stays a nil interface.
func (r *Runner) attachMCP(session *Session, server mcpserve.Session, display string) {
	if display != "" {
		session.Details.MCP = display
		session.Intro = append(session.Intro, "MCP "+display)
	}
	if v := reflect.ValueOf(server); server == nil || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return
	}
	session.mcp = server
}

// preparedTarget is what the prepare step of a resource type resolved: the target name, the
// session's display name and intro lines, the SSM port-forwarding parameters and the IDE target.
type preparedTarget struct {
	ssmParameters map[string][]string
	name          string
	display       string
	info          []string
	target        ideTarget
}

// ideTarget carries what is needed to register the forwarded connection in the IDE.
type ideTarget struct {
	Engine   string
	Database string
	// RDSHost and RDSPort are the real endpoint, used to sign AWS IAM auth tokens.
	RDSHost string
	// InstanceARN and MasterSecretARN locate the Secrets Manager credentials of the instance.
	InstanceARN     string
	MasterSecretARN string
	// DBUser is the database user, signing in with AWS IAM when IAMAuth is true, else with DBPassword.
	DBUser string
	// DBPassword is the password entered by the user; empty when the session signs in with AWS IAM.
	DBPassword string
	// DBSecretARN is the secret holding the password; empty unless the user picked one.
	DBSecretARN string
	// KeychainAccount is the keychain entry DBPassword was remembered in; empty when not remembered.
	KeychainAccount string
	// ClusterCA is the base64 certificate authority of an EKS cluster, for its kubeconfig.
	ClusterCA string
	// CacheEndpoint is the real cache host, the TLS server name of an ElastiCache session.
	CacheEndpoint string
	// CacheAuth is how the cache signs clients in (CacheAuth*); empty for other types.
	CacheAuth string
	// CacheReplicationGroup is the replication group of a cache node reached by its node id, which
	// IAM tokens are signed for; empty otherwise.
	CacheReplicationGroup string
	RDSPort               int
	// CacheTLS reports in-transit encryption; CacheServerless a serverless cache; CacheClusterMode a
	// cluster-mode-enabled replication group, whose other shards the tunnel cannot reach.
	CacheTLS         bool
	CacheServerless  bool
	CacheClusterMode bool
	// IAMAuth reports whether the session signs in with AWS IAM: the instance accepts it (not SQL
	// Server) and the user did not choose a password instead.
	IAMAuth bool
}

type awsConfig struct {
	Profile string
	Region  string
	Args    []string
}

func NewRunner(opts *RunnerOptions) *Runner {
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	err := opts.Err
	if err == nil {
		err = os.Stderr
	}
	registry := opts.Registry
	if registry == nil {
		registry = NewSessionRegistry()
	}
	mcpHost := opts.MCPHost
	if mcpHost == nil {
		mcpHost = mcpserve.NewHost()
	}

	jumphostTag := opts.JumphostTag
	if jumphostTag.IsZero() {
		jumphostTag = awstag.Tag{Key: defaultJumphostTagKey, Value: defaultJumphostTagValue}
	}
	runner := &Runner{
		in: in, out: out, err: err, registry: registry, mcpHost: mcpHost,
		projectDir: opts.ProjectDir, mcpLogDir: opts.MCPLogDir, accounts: map[string]string{},
		defaultRegion: strings.TrimSpace(opts.DefaultRegion), defaultDBUser: opts.DefaultDBUser,
		jumphostTag: jumphostTag, readOnlySecretTag: opts.ReadOnlySecretTag,
		mcpServerPrefix: opts.MCPServerPrefix, kubeconfigDir: opts.KubeconfigDir,
		randomLocalPort: opts.RandomLocalPort,
		databases:       opts.Databases,
		containers:      opts.Containers,
		environments:    opts.Environments,
		interactive:     opts.Interactive,
	}
	if opts.PrefsPath != "" {
		runner.prefs = NewPrefsStore(opts.PrefsPath)
		runner.keychain = opts.Keychain
	}
	return runner
}

// Run prepares a single session and streams it in the current terminal until it ends.
func (r *Runner) Run(ctx context.Context, options *Options) error {
	session, err := r.Prepare(ctx, *options)
	if err != nil {
		return err
	}
	release := func() {
		if msg := session.Release(); msg != "" {
			fprintf(r.out, "%s\n", msg)
		}
	}
	stopHangupWatch := RunOnHangup(release)
	defer func() {
		stopHangupWatch()
		release()
	}()

	if session.ResourceType == resourceDirect || session.ResourceType == resourceDocker || session.ResourceType == resourceDockerCache {
		return r.serveDirect(ctx, session)
	}

	fprintf(r.out, "Starting SSM session...\n")
	for _, line := range session.Intro {
		fprintf(r.out, "> %s\n", line)
	}
	fprintf(r.out, "Ctrl+C to cancel.\n")

	return r.runAWSStreaming(ctx, session.AwsArgs, session.CommandArgs...)
}

// Prepare resolves the target, reserves a free local port and registers the IDE data source.
// The caller starts the returned session (aws CommandArgs + AwsArgs; none for a direct database) and must Release it once finished.
func (r *Runner) Prepare(ctx context.Context, options Options) (*Session, error) { //nolint:gocritic // signature fixed by sessions.Runner.
	// A direct database is reached without AWS: no CLI, identity, jumphost nor local port.
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceDirect) {
		return r.prepareDirect(ctx, &options)
	}
	if t := strings.ToLower(strings.TrimSpace(options.ResourceType)); t == resourceDocker || t == resourceDockerCache {
		return r.prepareDocker(ctx, &options)
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, i18n.NewError("jumphost.runner.aws_cli_is_not")
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType != resourceEKS && resourceType != resourceRDS && resourceType != resourceElastiCache {
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, &options)
	if err != nil {
		return nil, err
	}

	jumphostID, err := r.getJumphostID(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var prepared preparedTarget
	switch resourceType {
	case resourceRDS:
		prepared, err = r.prepareRDS(ctx, cfg, &options)
	case resourceEKS:
		prepared, err = r.prepareEKS(ctx, cfg, &options)
	default: // resourceElastiCache, the last of the types checked above.
		prepared, err = r.prepareElastiCache(ctx, cfg, &options)
	}
	if err != nil {
		return nil, err
	}
	resolvedTargetName, sessionDisplayName, sessionInfo, ssmParameters, target := prepared.name, prepared.display, prepared.info, prepared.ssmParameters, prepared.target

	// A remembered password the database rejects is forgotten, so the next connection asks again.
	if (resourceType == resourceRDS || resourceType == resourceElastiCache) && target.DBPassword != "" && options.RememberPassword && r.KeychainAvailable() {
		if ref, ok := r.prefsTarget(ctx, &options); ok {
			target.KeychainAccount = keychainAccount(ref, target.DBUser) // the default user for an empty cache user
		}
	}

	// The prepare step reserved the port under this label; release it if anything below fails.
	label := sessionLabel(resourceType, resolvedTargetName, cfg)
	ready := false
	defer func() {
		if !ready {
			r.registry.release(label)
		}
	}()

	localPort := firstValue(ssmParameters, "localPortNumber")
	remotePort := firstValue(ssmParameters, "portNumber")
	host := strings.TrimSpace(firstValue(ssmParameters, "host"))
	connectedServer := host
	if connectedServer != "" && strings.TrimSpace(remotePort) != "" {
		connectedServer = fmt.Sprintf("%s:%s", connectedServer, strings.TrimSpace(remotePort))
	}

	parameters, err := json.Marshal(ssmParameters)
	if err != nil {
		return nil, i18n.NewError("jumphost.runner.failed_to_serialize_ssm", err)
	}

	session := &Session{
		Label:        label,
		ResourceType: resourceType,
		Target:       resolvedTargetName,
		Profile:      cfg.Profile,
		Region:       cfg.Region,
		Details: SessionDetails{
			ConnectedServer: connectedServer,
			RestartCommand:  buildRestartCommand(resourceType, resolvedTargetName, localPort, restartDBUser(&target), restartDBSecret(&target), cfg),
			Auth:            describeAuth(&target),
			TokenCommand:    tokenCommand(&target, cfg),
		},
		AwsArgs: append([]string{}, cfg.Args...),
		CommandArgs: []string{
			"ssm", "start-session",
			"--target", jumphostID,
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
			"--parameters", string(parameters),
		},
		Intro: append([]string{sessionDisplayName}, sessionInfo...),
	}
	if session.LocalPort, err = strconv.Atoi(localPort); err != nil {
		return nil, i18n.NewError("jumphost.runner.invalid_local_port", localPort, err)
	}
	if resourceType == resourceRDS {
		session.ReadCredentials = r.secretCredentialsReader(r.awsRunner(cfg), resolvedTargetName, &target)
		if session.Details.Auth == "" && session.ReadCredentials != nil {
			session.Details.Auth = "Secrets Manager · read-only secrets " + r.readOnlySecretTag.String()
		}
	}

	var removeDataSource func() string
	if !options.SkipIDEDataSource {
		session.Details.IDEDataSource, removeDataSource = r.registerIDEDataSource(&options, resourceType, resolvedTargetName, localPort, &target, cfg)
		if session.Details.IDEDataSource != "" {
			session.Intro = append(session.Intro, session.Details.IDEDataSource)
		}
	}
	var removeKubeconfig func() string
	if resourceType == resourceEKS {
		var warning string
		session.Details.Kubeconfig, removeKubeconfig, warning = r.writeKubeconfig(resolvedTargetName, host, target.ClusterCA, session.LocalPort, cfg)
		session.Intro = append(session.Intro, eksSessionInfo(session.LocalPort, host, session.Details.Kubeconfig)...)
		if warning != "" {
			session.Intro = append(session.Intro, "Warning: "+warning)
		}
	}
	if resourceType == resourceEKS && !options.SkipMCP {
		handle, display := r.startKubeMCP(&options, resolvedTargetName, host, target.ClusterCA, session.LocalPort, cfg)
		r.attachMCP(session, handle, display)
	}
	if resourceType == resourceRDS && !options.SkipMCP {
		handle, display := r.startMCP(&options, resolvedTargetName, session.LocalPort, &target, cfg, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	if resourceType == resourceElastiCache && !options.SkipMCP {
		handle, display := r.startCacheMCP(&options, resolvedTargetName, session.LocalPort, &target, cfg, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	if (resourceType == resourceRDS && signInApplies(&target, options.MCPAllowWrites)) ||
		(resourceType == resourceElastiCache && cacheSignInApplies(&target)) {
		signIn := &sessionSignIn{
			runner: r, session: session, cfg: cfg, target: target,
			resourceType: resourceType, instance: resolvedTargetName, localPort: localPort,
			aws: r.awsRunner(cfg),
		}
		if session.Details.IDEDataSource != "" {
			signIn.updateIDE = func(user string) {
				updated := target
				updated.DBUser = user
				// Same key: the entry is updated; the first remover still removes it.
				r.registerIDEDataSource(&options, resourceType, resolvedTargetName, localPort, &updated, cfg)
			}
		}
		session.SignIn = signIn
	}
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp, removeDataSource, removeKubeconfig)

	ready = true
	return session, nil
}

// ListAWSProfiles returns available AWS CLI profiles.
func (r *Runner) ListAWSProfiles(ctx context.Context) ([]string, error) {
	summaries, err := r.ListAWSProfileSummaries(ctx)
	if err != nil {
		return nil, err
	}

	profiles := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		name := strings.TrimSpace(summary.Name)
		if name == "" {
			continue
		}
		profiles = append(profiles, name)
	}

	return profiles, nil
}

// ListAWSProfileNames returns the AWS CLI profile names, sorted, with a single aws call: fast
// enough for shell completion, unlike ListAWSProfileSummaries.
func (r *Runner) ListAWSProfileNames(ctx context.Context) ([]string, error) {
	return r.listProfileNames(ctx)
}

// listProfileNames returns the sorted AWS CLI profile names, without the default fallback.
func (r *Runner) listProfileNames(ctx context.Context) ([]string, error) {
	rawProfiles, err := r.runAWSCapture(ctx, nil, "configure", "list-profiles")
	if err != nil {
		return nil, err
	}
	profileSet := map[string]struct{}{}
	for _, line := range strings.Split(rawProfiles, "\n") {
		if profile := strings.TrimSpace(line); profile != "" {
			profileSet[profile] = struct{}{}
		}
	}
	profiles := make([]string, 0, len(profileSet))
	for profile := range profileSet {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return profiles, nil
}

// ListAWSProfileSummaries returns available AWS profiles with display details.
func (r *Runner) ListAWSProfileSummaries(ctx context.Context) ([]AWSProfileSummary, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, i18n.NewError("jumphost.runner.aws_cli_is_not")
	}

	profiles, err := r.listProfileNames(ctx)
	if err != nil {
		return nil, err
	}

	if len(profiles) == 0 {
		profiles = []string{defaultAwsProfile}
	}

	summaries := make([]AWSProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		description := "region n/a"
		region, err := r.runAWSCapture(ctx, nil, "configure", "get", "region", "--profile", profile)
		if err == nil {
			trimmedRegion := strings.TrimSpace(region)
			if trimmedRegion != "" {
				description = fmt.Sprintf("region %s", trimmedRegion)
			}
		}

		summaries = append(summaries, AWSProfileSummary{
			Name:        profile,
			Description: description,
		})
	}

	return summaries, nil
}

// ListResourceNames returns available resource names for the given type.
func (r *Runner) ListResourceNames(ctx context.Context, options *Options) ([]string, error) {
	resources, err := r.ListResourceSummaries(ctx, options)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resources))
	for _, resource := range resources {
		name := strings.TrimSpace(resource.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}

	return names, nil
}

// requireAWSCLI returns an error if the AWS CLI is not installed.
func requireAWSCLI() error {
	if _, err := exec.LookPath("aws"); err != nil {
		return i18n.NewError("jumphost.runner.aws_cli_is_not")
	}
	return nil
}

// listResources lists the targets of resourceType with the resolved cfg.
func (r *Runner) listResources(ctx context.Context, cfg awsConfig, resourceType string) ([]ResourceSummary, error) {
	switch resourceType {
	case resourceRDS:
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[*].{name:DBInstanceIdentifier,engine:Engine,engineVersion:EngineVersion,host:Endpoint.Address,port:Endpoint.Port,size:AllocatedStorage,status:DBInstanceStatus,iamAuth:IAMDatabaseAuthenticationEnabled}",
			"--output", "json",
		)
		if err != nil {
			return nil, err
		}

		var instances []struct {
			Name          string `json:"name"`
			Engine        string `json:"engine"`
			EngineVersion string `json:"engineVersion"`
			Host          string `json:"host"`
			Status        string `json:"status"`
			Port          int    `json:"port"`
			SizeGiB       int    `json:"size"`
			IAMAuth       bool   `json:"iamAuth"`
		}
		if err := json.Unmarshal([]byte(listOutput), &instances); err != nil {
			return nil, i18n.NewError("jumphost.runner.failed_to_parse_rds", err)
		}

		resources := make([]ResourceSummary, 0, len(instances))
		for _, instance := range instances {
			name := strings.TrimSpace(instance.Name)
			if name == "" {
				continue
			}

			resources = append(resources, ResourceSummary{
				Name:        name,
				Description: formatRDSResourceDescription(instance.Engine, instance.EngineVersion, instance.Host, instance.Port, instance.SizeGiB, instance.Status),
				IAMAuth:     instance.IAMAuth,
				Engine:      instance.Engine,
			})
		}

		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})

		return resources, nil
	case resourceEKS:
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"eks", "list-clusters",
			"--query", "clusters[*].{name:@}",
			"--output", "json",
		)
		if err != nil {
			return nil, err
		}

		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(listOutput), &clusters); err != nil {
			return nil, i18n.NewError("jumphost.runner.failed_to_parse_eks", err)
		}

		resources := make([]ResourceSummary, 0, len(clusters))
		for _, cluster := range clusters {
			name := strings.TrimSpace(cluster.Name)
			if name == "" {
				continue
			}
			resources = append(resources, ResourceSummary{Name: name})
		}
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})
		return resources, nil
	case resourceElastiCache:
		resources, err := r.listElastiCacheResources(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return resources, nil
	default:
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type", resourceType)
	}
}

// ListResourceSummaries returns available resources with display details for the given type.
func (r *Runner) ListResourceSummaries(ctx context.Context, options *Options) ([]ResourceSummary, error) {
	if err := requireAWSCLI(); err != nil {
		return nil, err
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if !supportedResourceType(resourceType) {
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}

	resources, err := r.listResources(ctx, cfg, resourceType)
	if err != nil {
		return nil, err
	}
	return withType(resources, resourceType), nil
}

func formatElastiCacheResourceDescription(host string, port int, status string) string {
	parts := make([]string, 0, 3)

	host = strings.TrimSpace(host)
	if host != "" {
		if port > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", host, port))
		} else {
			parts = append(parts, host)
		}
	}

	status = strings.TrimSpace(status)
	if status != "" {
		parts = append(parts, status)
	}

	return strings.Join(parts, " | ")
}

func formatRDSResourceDescription(engine, engineVersion, host string, port, sizeGiB int, status string) string {
	parts := make([]string, 0, 4)

	engine = strings.TrimSpace(engine)
	engineVersion = strings.TrimSpace(engineVersion)
	if engine != "" && engineVersion != "" {
		parts = append(parts, fmt.Sprintf("%s %s", engine, engineVersion))
	} else if engine != "" {
		parts = append(parts, engine)
	}

	host = strings.TrimSpace(host)
	if host != "" {
		if port > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", host, port))
		} else {
			parts = append(parts, host)
		}
	}

	if sizeGiB > 0 {
		parts = append(parts, fmt.Sprintf("%d GiB", sizeGiB))
	} else {
		parts = append(parts, "size n/a")
	}

	status = strings.TrimSpace(status)
	if status != "" {
		parts = append(parts, status)
	}

	return strings.Join(parts, " | ")
}

// ResolveDefaultLocalPort computes the recommended local port for a selected target:
// the remote port when it is free locally, otherwise the next free port above it; with
// randomLocalPort, a free port chosen by the OS.
func (r *Runner) ResolveDefaultLocalPort(ctx context.Context, options Options) (int, error) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	if r.randomLocalPort {
		return r.registry.SuggestPort(0)
	}
	port, err := r.resolvePreferredLocalPort(ctx, &options)
	if err != nil {
		return 0, err
	}
	return r.registry.SuggestPort(port)
}

// LoadConnectionPrefs returns the Connect step choices saved for the target of options, if any.
func (r *Runner) LoadConnectionPrefs(ctx context.Context, options Options) (ConnectionPrefs, bool) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	target, ok := r.prefsTarget(ctx, &options)
	if !ok {
		return ConnectionPrefs{}, false
	}
	return r.prefs.Load(target)
}

// SaveConnectionPrefs remembers the choices of options for its target: profile and local port,
// plus user, sign-in method, secret and MCP write access for RDS, ElastiCache and direct databases. The password itself is never
// written to the file: with RememberPassword it goes to the OS keychain. Keychain failures are
// returned wrapped in ErrKeychain, after the choices are saved.
func (r *Runner) SaveConnectionPrefs(ctx context.Context, options Options) error { //nolint:gocritic // signature fixed by sessions.Runner and ui.JumphostDiscovery.
	target, ok := r.prefsTarget(ctx, &options)
	if !ok {
		return nil
	}
	prefs := &ConnectionPrefs{
		Profile:      resolveProfileName(options.AwsProfile),
		ResourceType: target.Type,
		LocalPort:    options.LocalPortNumber,
	}
	if target.Type == resourceDirect {
		// A direct database has neither AWS profile nor local port.
		prefs.Profile, prefs.LocalPort = "", 0
	}
	var keychainErr error
	if target.Type == resourceRDS || target.Type == resourceElastiCache || target.Type == resourceDirect {
		prefs.DBUser = strings.TrimSpace(options.DBUser)
		prefs.SignIn = options.SignInMethod()
		if target.Type == resourceDirect {
			// A direct database always signs in with its password.
			prefs.SignIn = SignInPassword
		}
		if target.Type == resourceElastiCache && prefs.SignIn == SignInPassword {
			// The keychain entry and the session sign the empty user in as the default user.
			prefs.DBUser = keychainUser(target.Type, prefs.DBUser)
		}
		if prefs.SignIn == SignInSecret {
			prefs.DBSecretARN = strings.TrimSpace(options.DBSecretARN)
		}
		prefs.MCPAllowWrites = options.MCPAllowWrites
		var previous *ConnectionPrefs
		if saved, found := r.prefs.Load(target); found {
			previous = &saved
		}
		if previous != nil && keepPreviousSignIn(previous, &options) {
			prefs.SignIn, prefs.DBSecretARN, prefs.PasswordInKeychain = previous.SignIn, previous.DBSecretARN, previous.PasswordInKeychain
			if method := previous.SignInMethod(); method == SignInPassword || method == SignInSecret {
				prefs.DBUser = previous.DBUser
			}
		} else {
			keychainErr = r.syncKeychain(target, previous, prefs, &options)
		}
	}
	return errors.Join(r.prefs.Save(target, prefs), keychainErr)
}

// keepPreviousSignIn reports whether the saved sign-in choices outlive a connection that did not
// choose one: the wizard offered neither a password nor a secret (e.g. --no-mcp) and leaves
// options.SignIn empty. A remembered password or secret, with its user, is then kept.
func keepPreviousSignIn(previous *ConnectionPrefs, options *Options) bool {
	return previous != nil && options.SignIn == "" && options.DBPassword == "" && strings.TrimSpace(options.DBSecretARN) == ""
}

// prefsTarget identifies the target keying the preferences of options; false when none apply
// (unsupported type, disabled, or the AWS account is unknown).
func (r *Runner) prefsTarget(ctx context.Context, options *Options) (TargetRef, bool) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType == resourceDirect {
		// A direct database belongs to no AWS account: no lookup is needed.
		if r.prefs == nil || strings.TrimSpace(options.Name) == "" {
			return TargetRef{}, false
		}
		return DirectTargetRef(options.Name), true
	}
	if r.prefs == nil || !supportedResourceType(resourceType) || strings.TrimSpace(options.Name) == "" {
		return TargetRef{}, false
	}
	profile := resolveProfileName(options.AwsProfile)
	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return TargetRef{}, false
	}
	account := r.accountFor(ctx, profile)
	if account == "" {
		return TargetRef{}, false
	}
	return TargetRef{Account: account, Region: region, Type: resourceType, Name: strings.TrimSpace(options.Name)}, true
}

// accountFor returns the AWS account ID of profile: the one seen by the identity check, which runs
// before targets are listed, else asked to STS. It is empty when unknown.
func (r *Runner) accountFor(ctx context.Context, profile string) string {
	r.accountsMu.Lock()
	account, ok := r.accounts[profile]
	r.accountsMu.Unlock()
	if ok {
		return account
	}
	account, err := r.runAWSCapture(ctx, []string{"--profile", profile}, "sts", "get-caller-identity", "--query", "Account", "--output", "text")
	if err != nil {
		return ""
	}
	r.rememberAccount(profile, account)
	return strings.TrimSpace(account)
}

// rememberAccount caches the account of profile, from an account ID or a caller identity ARN
// (arn:aws:sts::<account>:assumed-role/...).
func (r *Runner) rememberAccount(profile, identity string) {
	account := strings.TrimSpace(identity)
	if parts := strings.Split(account, ":"); len(parts) > 4 {
		account = parts[4]
	}
	if account == "" {
		return
	}
	r.accountsMu.Lock()
	defer r.accountsMu.Unlock()
	r.accounts[profile] = account
}

// resolveProfileName is profile, else AWS_PROFILE, else the default profile.
func resolveProfileName(profile string) string {
	if profile = strings.TrimSpace(profile); profile != "" {
		return profile
	}
	if profile = strings.TrimSpace(os.Getenv("AWS_PROFILE")); profile != "" {
		return profile
	}
	return defaultAwsProfile
}

// CheckLocalPort returns an error explaining why port cannot be used for a new session.
func (r *Runner) CheckLocalPort(port int) error {
	return r.registry.CheckPort(port)
}

func (r *Runner) resolvePreferredLocalPort(ctx context.Context, options *Options) (int, error) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	switch resourceType {
	case resourceEKS:
		return defaultEKSLocalPort, nil
	case resourceElastiCache:
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return 0, i18n.NewError("jumphost.runner.elasticache_cluster_name_is")
		}

		cfg, err := r.resolveAWSConfig(ctx, options)
		if err != nil {
			return 0, err
		}

		// Try replication group first.
		rawPort, err := r.runAWSCapture(ctx, cfg.Args,
			"elasticache", "describe-replication-groups",
			"--replication-group-id", name,
			"--query", "ReplicationGroups[0].NodeGroups[0].PrimaryEndpoint.Port",
			"--output", "text",
		)
		if err == nil {
			rawPort = strings.TrimSpace(rawPort)
			if rawPort != "" && !strings.EqualFold(rawPort, "none") {
				port := 0
				if _, scanErr := fmt.Sscanf(rawPort, "%d", &port); scanErr == nil && port > 0 {
					return port, nil
				}
			}
		}

		// Try standalone cache cluster.
		rawPort, err = r.runAWSCapture(ctx, cfg.Args,
			"elasticache", "describe-cache-clusters",
			"--cache-cluster-id", name,
			"--show-cache-node-info",
			"--query", "CacheClusters[0].CacheNodes[0].Endpoint.Port",
			"--output", "text",
		)
		if err == nil {
			rawPort = strings.TrimSpace(rawPort)
			if rawPort != "" && !strings.EqualFold(rawPort, "none") {
				port := 0
				if _, scanErr := fmt.Sscanf(rawPort, "%d", &port); scanErr == nil && port > 0 {
					return port, nil
				}
			}
		}

		// Try serverless cache.
		rawPort, err = r.runAWSCapture(ctx, cfg.Args,
			"elasticache", "describe-serverless-caches",
			"--serverless-cache-name", name,
			"--query", "ServerlessCaches[0].Endpoint.Port",
			"--output", "text",
		)
		if err == nil {
			rawPort = strings.TrimSpace(rawPort)
			if rawPort != "" && !strings.EqualFold(rawPort, "none") {
				port := 0
				if _, scanErr := fmt.Sscanf(rawPort, "%d", &port); scanErr == nil && port > 0 {
					return port, nil
				}
			}
		}

		return defaultRedisPort, nil
	case resourceRDS:
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return 0, i18n.NewError("jumphost.runner.rds_instance_name_is")
		}

		cfg, err := r.resolveAWSConfig(ctx, options)
		if err != nil {
			return 0, err
		}

		rawInstance, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[0]",
			"--db-instance-identifier", name,
			"--output", "json",
		)
		if err != nil {
			return 0, err
		}

		var instance struct {
			Endpoint struct {
				Port int `json:"Port"`
			} `json:"Endpoint"`
		}
		if err := json.Unmarshal([]byte(rawInstance), &instance); err != nil {
			return 0, i18n.NewError("jumphost.runner.failed_to_parse_rds_2", err)
		}
		if instance.Endpoint.Port == 0 {
			return 0, i18n.NewError("jumphost.runner.rds_instance_endpoint_port")
		}

		return instance.Endpoint.Port, nil
	default:
		return 0, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}
}

func (r *Runner) resolveAWSConfig(ctx context.Context, options *Options) (awsConfig, error) {
	profile := resolveProfileName(options.AwsProfile)

	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return awsConfig{}, i18n.NewError("jumphost.runner.no_aws_region_for",
			profile, profile)
	}

	cfg := awsConfig{
		Profile: profile,
		Region:  region,
		Args:    []string{"--profile", profile, "--region", region},
	}

	fprintf(r.out, "Using AWS profile/region... %s/%s\n", cfg.Profile, cfg.Region)
	if err := r.ensureIdentity(ctx, cfg, !options.Unattended); err != nil {
		return awsConfig{}, err
	}

	return cfg, nil
}

// ResolveRegion returns the region used when none is given explicitly: AWS_REGION, then the region
// configured for profile, then the configured default region; "" when none.
func (r *Runner) ResolveRegion(ctx context.Context, profile string) string {
	if region := strings.TrimSpace(os.Getenv("AWS_REGION")); region != "" {
		return region
	}

	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if profile == "" {
		profile = defaultAwsProfile
	}

	configuredRegion, err := r.runAWSCapture(ctx, nil, "configure", "get", "region", "--profile", profile)
	if region := strings.TrimSpace(configuredRegion); err == nil && region != "" {
		return region
	}
	return r.defaultRegion
}

// settingHint tells how to fix a setting of the config file: look at it, change it, or override it.
func settingHint(key, example, flag string) string {
	return fmt.Sprintf("check it with `airlock config show`, change it with `airlock config set %s %s`, or pass %s", key, example, flag)
}

// jumphostNotFoundError explains that no running instance carries the jumphost tag, which may be
// the wrong profile or region, or a tag that does not match the account's conventions.
func jumphostNotFoundError(tag awstag.Tag, profile, region string) error {
	return i18n.NewError("jumphost.runner.no_running_jumphost_instance",
		tag, region, profile, settingHint("jumphostTag", "Key=Value", "--jumphost-tag"))
}

// ssoLogin signs profile in again with AWS IAM Identity Center, on the user's terminal.
func (r *Runner) ssoLogin(ctx context.Context, profile string) error {
	interactive := interactiveFrom(ctx)
	if interactive == nil {
		interactive = r.interactive
	}
	if interactive == nil {
		return r.runAWSStreaming(ctx, nil, "sso", "login", "--profile", profile)
	}
	purpose := fmt.Sprintf("The AWS credentials of profile %s are missing or expired: signing in again.", profile)
	return interactive(ctx, purpose, "aws", "sso", "login", "--profile", profile)
}

// ensureIdentity checks the AWS identity of cfg, signing in again when it is missing or expired,
// unless interactive is false: then it fails with ErrSignInRequired.
func (r *Runner) ensureIdentity(ctx context.Context, cfg awsConfig, interactive bool) error {
	fprintf(r.out, "Using AWS identity... ")
	identity, err := r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err == nil {
		fprintf(r.out, "%s\n", identity)
		r.rememberAccount(cfg.Profile, identity)
		return nil
	}

	// One sign-in at a time: checks failing together would each open the browser. A check that
	// waited finds the identity signed in by the one before it.
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if identity, err = r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text"); err == nil {
		fprintf(r.out, "%s\n", identity)
		r.rememberAccount(cfg.Profile, identity)
		return nil
	}

	if !interactive {
		return i18n.NewError("jumphost.identity.aws_credentials_of_profile", ErrSignInRequired, cfg.Profile)
	}
	fprintf(r.out, "Failed. Reauthorize...\n")
	// A profile without source_profile signs in itself.
	loginProfile := cfg.Profile
	if parentProfile, lookupErr := r.runAWSCapture(ctx, nil, "configure", "get", "source_profile", "--profile", cfg.Profile); lookupErr == nil && strings.TrimSpace(parentProfile) != "" {
		loginProfile = strings.TrimSpace(parentProfile)
	}
	if loginProfile == "" {
		loginProfile = defaultAwsProfile
	}

	if authErr := r.ssoLogin(ctx, loginProfile); authErr != nil {
		return i18n.NewError("jumphost.runner.failed_to_authorize_please")
	}

	fprintf(r.out, "Using AWS identity... ")
	identity, err = r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err != nil {
		return i18n.NewError("jumphost.runner.failed_to_retrieve_current")
	}

	fprintf(r.out, "%s\n", identity)
	r.rememberAccount(cfg.Profile, identity)
	return nil
}

func (r *Runner) getJumphostID(ctx context.Context, cfg awsConfig) (string, error) {
	fprintf(r.out, "Using jumphost... ")
	jumphostID, err := r.runAWSCapture(ctx, cfg.Args,
		"ec2", "describe-instances",
		"--filters", fmt.Sprintf("Name=tag:%s,Values=%s", r.jumphostTag.Key, r.jumphostTag.Value), "Name=instance-state-name,Values=running",
		"--output", "text",
		"--query", "Reservations[0].Instances[0].InstanceId",
		"--no-cli-pager",
	)
	if err != nil {
		return "", err
	}
	if jumphostID == "" || strings.EqualFold(jumphostID, "none") {
		return "", jumphostNotFoundError(r.jumphostTag, cfg.Profile, cfg.Region)
	}

	fprintf(r.out, "%s\n", jumphostID)
	return jumphostID, nil
}

func (r *Runner) prepareRDS(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[*].{name:DBInstanceIdentifier,address:Endpoint.Address,port:Endpoint.Port}",
			"--output", "json",
		)
		if err != nil {
			return preparedTarget{}, err
		}

		var instances []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Port    int    `json:"port"`
		}
		if err := json.Unmarshal([]byte(listOutput), &instances); err != nil {
			return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_parse_rds", err)
		}
		if len(instances) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_rds_instance_found")
		}
		if len(instances) == 1 {
			name = instances[0].Name
		} else {
			names := make([]string, 0, len(instances))
			for _, instance := range instances {
				names = append(names, instance.Name)
			}
			sort.Strings(names)
			selectedName, err := r.selectName("RDS instances", names, "Please specify the RDS instance name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.rds_instance_name_is")
			}
			name = selectedName
		}
	}

	rawInstance, err := r.runAWSCapture(ctx, cfg.Args,
		"rds", "describe-db-instances",
		"--query", "DBInstances[0]",
		"--db-instance-identifier", name,
		"--output", "json",
	)
	if err != nil {
		return preparedTarget{}, err
	}

	var instance struct {
		Engine               string `json:"Engine"`
		DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
		DBInstanceArn        string `json:"DBInstanceArn"`
		DBName               string `json:"DBName"`
		MasterUsername       string `json:"MasterUsername"`
		MasterUserSecret     struct {
			SecretArn string `json:"SecretArn"`
		} `json:"MasterUserSecret"`
		Endpoint struct {
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Endpoint"`
		IAMDatabaseAuthenticationEnabled bool `json:"IAMDatabaseAuthenticationEnabled"`
	}
	if err = json.Unmarshal([]byte(rawInstance), &instance); err != nil {
		return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_parse_rds_2", err)
	}
	if instance.Endpoint.Address == "" || instance.Endpoint.Port == 0 {
		return preparedTarget{}, i18n.NewError("jumphost.runner.rds_instance_endpoint_is")
	}

	secretID := strings.TrimSpace(options.DBSecretARN)
	if secretID == "" && options.DBPassword != "" && strings.TrimSpace(options.DBUser) == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.database_user_is_required")
	}
	iamAuth := instance.IAMDatabaseAuthenticationEnabled && options.DBPassword == "" && secretID == "" && options.SignIn != SignInAuto
	if iamAuth && r.ResolveDBUser(instance.Engine, options.DBUser) == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.database_user_is_required_2",
			instance.DBInstanceIdentifier)
	}
	var secretUser string
	if secretID != "" {
		aws := func(ctx context.Context, args ...string) (string, error) {
			return r.runAWSCapture(ctx, cfg.Args, args...)
		}
		if secretUser, err = resolveSecretSignIn(ctx, aws, secretID, options.DBUser); err != nil {
			return preparedTarget{}, err
		}
	}

	localPort, err := r.reservePort(sessionLabel(resourceRDS, instance.DBInstanceIdentifier, cfg), options.LocalPortNumber, instance.Endpoint.Port)
	if err != nil {
		return preparedTarget{}, err
	}

	displayName := fmt.Sprintf(
		"RDS %s %s:%d -> localhost:%d",
		instance.Engine,
		instance.DBInstanceIdentifier,
		instance.Endpoint.Port,
		localPort,
	)

	awsArgsHint := strings.Join(cfg.Args, " ")
	var sessionInfo []string
	var dbUser string
	switch {
	case secretID != "":
		dbUser = secretUser
		sessionInfo = rdsSecretSessionInfo(localPort, dbUser, secretID, awsArgsHint)
	case options.DBPassword != "":
		dbUser = strings.TrimSpace(options.DBUser)
		sessionInfo = rdsPasswordSessionInfo(localPort, dbUser)
	case iamAuth:
		dbUser = r.ResolveDBUser(instance.Engine, options.DBUser)
		sessionInfo = rdsIAMSessionInfo(localPort, instance.Endpoint.Address, instance.Endpoint.Port, dbUser, cfg, awsArgsHint)
	default:
		sessionInfo = rdsSecretsSessionInfo(localPort, instance.DBInstanceArn, instance.MasterUserSecret.SecretArn, instance.MasterUsername, r.readOnlySecretTag, awsArgsHint)
	}

	params := map[string][]string{
		"host":            {instance.Endpoint.Address},
		"portNumber":      {fmt.Sprintf("%d", instance.Endpoint.Port)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	password := options.DBPassword
	if secretID != "" {
		password = ""
	}

	return preparedTarget{name: instance.DBInstanceIdentifier, display: displayName, info: sessionInfo, ssmParameters: params, target: ideTarget{
		Engine:          instance.Engine,
		Database:        instance.DBName,
		RDSHost:         instance.Endpoint.Address,
		RDSPort:         instance.Endpoint.Port,
		InstanceARN:     instance.DBInstanceArn,
		MasterSecretARN: instance.MasterUserSecret.SecretArn,
		IAMAuth:         iamAuth,
		DBUser:          dbUser,
		DBPassword:      password,
		DBSecretARN:     secretID,
	}}, nil
}

// rdsSecretSessionInfo explains how to sign in with the password of the picked secret.
func rdsSecretSessionInfo(localPort int, user, secretID, awsArgsHint string) []string {
	return []string{
		"Signs in with the password of a Secrets Manager secret:",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + user,
		"  password             = read it with:",
		fmt.Sprintf("  aws secretsmanager get-secret-value --secret-id %s --query SecretString --output text %s", secretID, awsArgsHint),
	}
}

// rdsIAMSessionInfo explains how to sign in with AWS IAM authentication.
func rdsIAMSessionInfo(localPort int, rdsHost string, rdsPort int, iamUser string, cfg awsConfig, awsArgsHint string) []string {
	return []string{
		"For tools which support AWS IAM authentication, use:",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + iamUser,
		fmt.Sprintf("  region               = %s", cfg.Region),
		fmt.Sprintf("  credential/profile   = %s", cfg.Profile),
		fmt.Sprintf("  rds host:port        = %s:%d", rdsHost, rdsPort),
		"",
		"Otherwise, generate a temporary token (valid for 15 minutes) with:",
		fmt.Sprintf("  aws rds generate-db-auth-token --hostname %s --port %d --username %s %s", rdsHost, rdsPort, iamUser, awsArgsHint),
	}
}

// rdsPasswordSessionInfo explains how to sign in with the password entered in place of AWS IAM.
func rdsPasswordSessionInfo(localPort int, user string) []string {
	return []string{
		"Signs in with a password (AWS IAM passwordless sign-in turned off):",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = " + user,
		"  password             = the one entered before connecting (kept in memory only; the IDE asks for it)",
	}
}

// rdsSecretsSessionInfo explains where the credentials of an instance without IAM authentication (SQL Server) live.
func rdsSecretsSessionInfo(localPort int, instanceARN, masterSecretARN, masterUsername string, readOnlyTag awstag.Tag, awsArgsHint string) []string {
	info := []string{
		"IAM authentication is not enabled for this instance: use Secrets Manager.",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user / password      = read them from a secret below (the JSON holds both)",
	}
	if !readOnlyTag.IsZero() {
		info = append(info,
			"",
			"Read-only credentials, one secret per database:",
			fmt.Sprintf("  aws secretsmanager list-secrets --filters Key=tag-key,Values=DBInstanceArn Key=tag-value,Values=%s --query %q --output text %s",
				instanceARN, dbmcp.ReadOnlySecretsQuery(readOnlyTag, instanceARN), awsArgsHint),
			"  aws secretsmanager get-secret-value --secret-id <arn> --query SecretString --output text "+awsArgsHint,
		)
	}
	if masterSecretARN == "" {
		return append(info, "", "No master user secret found for this instance.")
	}
	return append(info,
		"",
		fmt.Sprintf("Master credentials (%s), administrators only - avoid unless strictly necessary:", masterUsername),
		fmt.Sprintf("  aws secretsmanager get-secret-value --secret-id %q --query SecretString --output text %s", masterSecretARN, awsArgsHint),
	)
}

func (r *Runner) prepareEKS(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"eks", "list-clusters",
			"--query", "clusters[*].{name:@}",
			"--output", "json",
		)
		if err != nil {
			return preparedTarget{}, err
		}

		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(listOutput), &clusters); err != nil {
			return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_parse_eks", err)
		}
		if len(clusters) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_eks_cluster_found")
		}
		if len(clusters) == 1 {
			name = clusters[0].Name
		} else {
			names := make([]string, 0, len(clusters))
			for _, cluster := range clusters {
				names = append(names, cluster.Name)
			}
			sort.Strings(names)
			selectedName, err := r.selectName("EKS clusters", names, "Please specify the EKS cluster name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.eks_cluster_name_is")
			}
			name = selectedName
		}
	}

	rawCluster, err := r.runAWSCapture(ctx, cfg.Args,
		"eks", "describe-cluster",
		"--name", name,
		"--query", "cluster",
		"--output", "json",
	)
	if err != nil {
		return preparedTarget{}, err
	}

	var cluster struct {
		Name                 string `json:"name"`
		Endpoint             string `json:"endpoint"`
		CertificateAuthority struct {
			Data string `json:"data"`
		} `json:"certificateAuthority"`
	}
	if err = json.Unmarshal([]byte(rawCluster), &cluster); err != nil {
		return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_parse_eks_2", err)
	}
	if cluster.Endpoint == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.eks_cluster_endpoint_is")
	}

	localPort, err := r.reservePort(sessionLabel(resourceEKS, cluster.Name, cfg), options.LocalPortNumber, defaultEKSLocalPort)
	if err != nil {
		return preparedTarget{}, err
	}

	displayName := fmt.Sprintf("EKS %s (%s) -> localhost:%d", cluster.Name, cluster.Endpoint, localPort)
	params := map[string][]string{
		"host":            {strings.TrimPrefix(cluster.Endpoint, "https://")},
		"portNumber":      {"443"},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return preparedTarget{name: cluster.Name, display: displayName, ssmParameters: params, target: ideTarget{ClusterCA: strings.TrimSpace(cluster.CertificateAuthority.Data)}}, nil
}

// writeKubeconfig writes the kubeconfig pointing Kubernetes clients at the tunnel of an EKS
// cluster, signing in with aws eks get-token. It writes none without a kubeconfig directory, or
// without the certificate authority kubectl needs to trust the cluster. A failure only warns.
func (r *Runner) writeKubeconfig(clusterName, endpointHost, caData string, localPort int, cfg awsConfig) (path string, remove func() string, warning string) {
	if r.kubeconfigDir == "" || caData == "" {
		return "", nil, ""
	}
	path, removeFile, err := kubeconfig.Write(r.kubeconfigDir, &kubeconfig.Cluster{
		Name:                     strings.Join([]string{"airlock", cfg.Profile, cfg.Region, clusterName}, "-"),
		Server:                   fmt.Sprintf("https://localhost:%d", localPort),
		TLSServerName:            endpointHost,
		CertificateAuthorityData: caData,
		Command:                  "aws",
		Args:                     append([]string{"eks", "get-token", "--cluster-name", clusterName, "--output", "json"}, cfg.Args...),
	})
	if err != nil {
		return "", nil, "no kubeconfig for this session: " + err.Error()
	}
	return path, func() string {
		if err := removeFile(); err != nil {
			return fmt.Sprintf("Warning: failed to remove kubeconfig %s: %v", path, err)
		}
		return "kubeconfig " + path + " removed"
	}, ""
}

// eksSessionInfo explains how Kubernetes clients reach the tunnel: the session kubeconfig when
// one was written, else the kubectl flags overriding the user's own context of the cluster.
func eksSessionInfo(localPort int, endpointHost, kubeconfigPath string) []string {
	override := fmt.Sprintf("  kubectl --server https://localhost:%d --tls-server-name %s get namespaces", localPort, endpointHost)
	if kubeconfigPath == "" {
		return []string{"For kubectl, select your context of this cluster and point it at the tunnel:", override}
	}
	return []string{
		"For kubectl, k9s and other Kubernetes clients, use the kubeconfig of this session (removed when it stops):",
		"  export KUBECONFIG=" + kubeconfigPath,
		"  kubectl get namespaces",
		"",
		"Or keep your own context of this cluster and point it at the tunnel:",
		override,
	}
}

func (r *Runner) prepareElastiCache(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		resources, err := r.listElastiCacheResources(ctx, cfg)
		if err != nil {
			return preparedTarget{}, err
		}
		if len(resources) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_elasticache_cluster_found")
		}
		if len(resources) == 1 {
			name = resources[0].Name
		} else {
			names := make([]string, 0, len(resources))
			for _, r := range resources {
				names = append(names, r.Name)
			}
			selectedName, err := r.selectName("ElastiCache clusters", names, "Please specify the cluster name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.elasticache_cluster_name_is")
			}
			name = selectedName
		}
	}

	// Try replication group first.
	rawGroup, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-replication-groups",
		"--replication-group-id", name,
		"--query", "ReplicationGroups[0]",
		"--output", "json",
	)
	if err == nil && strings.TrimSpace(rawGroup) != "" && !strings.EqualFold(strings.TrimSpace(rawGroup), "null") {
		var group struct {
			ReplicationGroupId string   `json:"ReplicationGroupId"`
			Engine             string   `json:"Engine"`
			Status             string   `json:"Status"`
			UserGroupIDs       []string `json:"UserGroupIds"`
			NodeGroups         []struct {
				PrimaryEndpoint struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				} `json:"PrimaryEndpoint"`
			} `json:"NodeGroups"`
			ConfigurationEndpoint struct {
				Address string `json:"Address"`
				Port    int    `json:"Port"`
			} `json:"ConfigurationEndpoint"`
			TransitEncryptionEnabled bool `json:"TransitEncryptionEnabled"`
			AuthTokenEnabled         bool `json:"AuthTokenEnabled"`
			ClusterEnabled           bool `json:"ClusterEnabled"`
		}
		if jsonErr := json.Unmarshal([]byte(rawGroup), &group); jsonErr == nil {
			// Cluster-mode groups have no primary endpoint: their configuration endpoint reaches one shard.
			endpoint := group.ConfigurationEndpoint
			if len(group.NodeGroups) > 0 && group.NodeGroups[0].PrimaryEndpoint.Address != "" {
				endpoint = group.NodeGroups[0].PrimaryEndpoint
			}
			if endpoint.Address != "" {
				return r.prepareCacheTarget(ctx, cfg, options, group.ReplicationGroupId, endpoint.Address, endpoint.Port, &ideTarget{
					Engine:           elastiCacheEngine(group.Engine),
					CacheAuth:        cacheAuthMode(group.AuthTokenEnabled, group.UserGroupIDs),
					CacheTLS:         group.TransitEncryptionEnabled,
					CacheClusterMode: group.ClusterEnabled,
				}, func(localPort int) string {
					return fmt.Sprintf("ElastiCache %s %s:%d -> localhost:%d", group.ReplicationGroupId, endpoint.Address, endpoint.Port, localPort)
				})
			}
		}
	}

	// Fall back to standalone cache cluster.
	rawCluster, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-cache-clusters",
		"--cache-cluster-id", name,
		"--show-cache-node-info",
		"--query", "CacheClusters[0]",
		"--output", "json",
	)
	if err == nil {
		var cluster struct {
			CacheClusterId     string `json:"CacheClusterId"`
			ReplicationGroupId string `json:"ReplicationGroupId"`
			Engine             string `json:"Engine"`
			EngineVersion      string `json:"EngineVersion"`
			CacheClusterStatus string `json:"CacheClusterStatus"`
			CacheNodes         []struct {
				Endpoint struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				} `json:"Endpoint"`
			} `json:"CacheNodes"`
			TransitEncryptionEnabled bool `json:"TransitEncryptionEnabled"`
			AuthTokenEnabled         bool `json:"AuthTokenEnabled"`
		}
		if json.Unmarshal([]byte(rawCluster), &cluster) == nil &&
			len(cluster.CacheNodes) > 0 && cluster.CacheNodes[0].Endpoint.Address != "" {
			endpoint := cluster.CacheNodes[0].Endpoint
			target := &ideTarget{
				Engine:    elastiCacheEngine(cluster.Engine),
				CacheAuth: cacheAuthMode(cluster.AuthTokenEnabled, nil),
				CacheTLS:  cluster.TransitEncryptionEnabled,
			}
			if cluster.ReplicationGroupId != "" {
				// A node of a replication group signs in as its group does.
				if groupErr := r.applyNodeReplicationGroup(ctx, cfg, cluster.ReplicationGroupId, target); groupErr != nil {
					return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_describe_replication", cluster.ReplicationGroupId, cluster.CacheClusterId, groupErr)
				}
			}
			return r.prepareCacheTarget(ctx, cfg, options, cluster.CacheClusterId, endpoint.Address, endpoint.Port, target, func(localPort int) string {
				return fmt.Sprintf("ElastiCache %s (%s %s) %s:%d -> localhost:%d",
					cluster.CacheClusterId, cluster.Engine, cluster.EngineVersion, endpoint.Address, endpoint.Port, localPort)
			})
		}
	}

	// Fall back to serverless cache.
	rawServerless, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-serverless-caches",
		"--serverless-cache-name", name,
		"--query", "ServerlessCaches[0]",
		"--output", "json",
	)
	if err != nil {
		return preparedTarget{}, i18n.NewError("jumphost.runner.elasticache_cluster_not_found", name, err)
	}

	var sc struct {
		ServerlessCacheName string `json:"ServerlessCacheName"`
		Engine              string `json:"Engine"`
		EngineVersion       string `json:"MajorEngineVersion"`
		Status              string `json:"Status"`
		UserGroupID         string `json:"UserGroupId"`
		Endpoint            struct {
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Endpoint"`
	}
	if err = json.Unmarshal([]byte(rawServerless), &sc); err != nil {
		return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_parse_serverless", err)
	}
	if sc.Endpoint.Address == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.serverless_cache_endpoint_is", name)
	}

	var userGroups []string
	if sc.UserGroupID != "" {
		userGroups = []string{sc.UserGroupID}
	}
	return r.prepareCacheTarget(ctx, cfg, options, sc.ServerlessCacheName, sc.Endpoint.Address, sc.Endpoint.Port, &ideTarget{
		Engine:          elastiCacheEngine(sc.Engine),
		CacheAuth:       cacheAuthMode(false, userGroups),
		CacheTLS:        true,
		CacheServerless: true,
	}, func(localPort int) string {
		return fmt.Sprintf("ElastiCache serverless %s (%s %s) %s:%d -> localhost:%d",
			sc.ServerlessCacheName, sc.Engine, sc.EngineVersion, sc.Endpoint.Address, sc.Endpoint.Port, localPort)
	})
}

// registerIDEDataSource adds the forwarded connection to the IntelliJ project in the working directory.
// It returns a display message and a function removing the entry again and describing the outcome
// (nil when nothing was registered).
// Failures are reported as a warning and never abort the session.
func (r *Runner) registerIDEDataSource(options *Options, resourceType, targetName, localPort string, target *ideTarget, cfg awsConfig) (message string, remove func() string) {
	if strings.TrimSpace(target.Engine) == "" {
		return "", nil
	}

	port, err := strconv.Atoi(strings.TrimSpace(localPort))
	if err != nil || port <= 0 {
		return "", nil
	}

	dataSource := &ideadatasource.DataSource{
		Key:        strings.Join([]string{resourceType, cfg.Profile, cfg.Region, targetName}, "|"),
		Name:       fmt.Sprintf("airlock %s %s", cfg.Profile, targetName),
		Engine:     target.Engine,
		Host:       "localhost",
		Port:       port,
		Database:   target.Database,
		Properties: awsIAMProperties(target, cfg),
	}
	dataSource.User = target.DBUser
	if target.IAMAuth {
		dataSource.AuthProvider = awsIAMAuthProvider
	}
	return r.registerDataSource(options, dataSource, fmt.Sprintf("localhost:%d", port))
}

// registerDataSource adds dataSource to the IntelliJ project in the working directory; endpoint is
// where it connects, for display. It returns a display message and a function removing the entry
// again (nil when nothing was registered). Failures are reported as a warning and never abort the
// session.
func (r *Runner) registerDataSource(options *Options, dataSource *ideadatasource.DataSource, endpoint string) (message string, remove func() string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return "", nil
	}

	result, err := ideadatasource.Register(projectDir, dataSource)
	if err != nil {
		fprintf(r.err, "Warning: failed to register IntelliJ data source: %v\n", err)
		return "", nil
	}
	if !result.Registered {
		return "", nil
	}

	key := dataSource.Key
	remove = func() string {
		unregistered, err := ideadatasource.Unregister(projectDir, key)
		switch {
		case err != nil:
			return fmt.Sprintf("Warning: failed to remove IntelliJ data source %q: %v", result.Name, err)
		case unregistered.Removed:
			return fmt.Sprintf("IntelliJ data source %q removed from %s", result.Name, relativePaths(projectDir, result.Paths))
		case unregistered.ActiveSessions > 0:
			return fmt.Sprintf("IntelliJ data source %q kept: still used by %d other session(s)", result.Name, unregistered.ActiveSessions)
		default:
			return ""
		}
	}

	return fmt.Sprintf("IntelliJ data source %q -> %s (%s)", result.Name, endpoint, relativePaths(projectDir, result.Paths)), remove
}

// awsIAMAuthProvider is the IntelliJ authentication method "AWS IAM".
const awsIAMAuthProvider = "aws.rds.iam"

// ResolveDBUser returns the database user of an RDS instance of engine (an AWS RDS engine name):
// user, else the configured default user of the engine; "" when neither is set.
func (r *Runner) ResolveDBUser(engine, user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return r.defaultDBUser.For(engine)
}

// awsIAMProperties prefills the AWS Toolkit settings used by IntelliJ's "AWS IAM" authentication.
func awsIAMProperties(target *ideTarget, cfg awsConfig) map[string]string {
	if !target.IAMAuth {
		return nil
	}
	return map[string]string{
		"AWS.RdsSigningHost": target.RDSHost,
		"AWS.RdsSigningPort": strconv.Itoa(target.RDSPort),
		"AWS.RegionId":       cfg.Region,
		"AWS.CredentialId":   "profile:" + cfg.Profile,
	}
}

// sessionRelease frees the port and target, stops the MCP server, then runs the cleanups (IDE data
// source, kubeconfig); nil cleanups are skipped.
// It returns the cleanup messages joined with "; ".
func sessionRelease(releasePort func(), mcpServer mcpserve.Session, cleanups ...func() string) func() string {
	return func() string {
		releasePort()
		var messages []string
		if mcpServer != nil {
			messages = append(messages, mcpServer.Close())
		}
		for _, cleanup := range cleanups {
			if cleanup == nil {
				continue
			}
			if msg := cleanup(); msg != "" {
				messages = append(messages, msg)
			}
		}
		return strings.Join(messages, "; ")
	}
}

// projectDirFor is where the opening of options registers its IDE data source and MCP servers: its
// own project directory, else the runner's, else the working directory.
func (r *Runner) projectDirFor(options *Options) (string, error) {
	if options != nil && options.ProjectDir != "" {
		return options.ProjectDir, nil
	}
	if r.projectDir != "" {
		return r.projectDir, nil
	}
	return os.Getwd()
}

// kubemcpStart starts an EKS session's MCP server; a package variable so tests can force a failure.
var kubemcpStart = kubemcp.Start

// startKubeMCP serves the read-only MCP server of an EKS session and returns its display line:
// empty when no server applies (no certificate authority), "not started: …" on failure.
func (r *Runner) startKubeMCP(options *Options, clusterName, endpointHost, caData string, localPort int, cfg awsConfig) (handle *mcpserve.Handle, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = kubemcpStart(r.mcpHost, &kubemcp.SessionConfig{
		AWS: r.awsRunner(cfg), ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
		Target: clusterName, EndpointHost: endpointHost, ClusterCA: caData, LocalPort: localPort,
		ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
		SkipRegistration: options.SkipMCPRegistration,
	})
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}

// dbmcpStart starts a session's MCP server; a package variable so tests can force a failure.
var dbmcpStart = dbmcp.Start

// startMCP serves the MCP server of a database session and returns the display line describing
// it (empty when the engine is unsupported, so the session shows no MCP line at all). A failure
// to start is reported through the returned display rather than r.err, which is discarded in TUI
// mode.
func (r *Runner) startMCP(options *Options, targetName string, localPort int, target *ideTarget, cfg awsConfig, allowWrites bool) (handle *dbmcp.Handle, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = dbmcpStart(r.mcpHost, &dbmcp.SessionConfig{
		AWS: func(ctx context.Context, args ...string) (string, error) {
			return r.runAWSCapture(ctx, cfg.Args, args...)
		},
		ProjectDir:         projectDir,
		Profile:            cfg.Profile,
		Region:             cfg.Region,
		Target:             targetName,
		AWSEngine:          target.Engine,
		RDSHost:            target.RDSHost,
		RDSPort:            target.RDSPort,
		DBName:             target.Database,
		InstanceARN:        target.InstanceARN,
		MasterSecretARN:    target.MasterSecretARN,
		DBUser:             target.DBUser,
		DBPassword:         target.DBPassword,
		DBSecretARN:        target.DBSecretARN,
		OnPasswordRejected: r.forgetKeychainFunc(target.KeychainAccount, target.DBPassword),
		IAMAuth:            target.IAMAuth,
		LocalPort:          localPort,
		AllowWrites:        allowWrites,
		LogDir:             r.mcpLogDir,
		ServerPrefix:       r.mcpServerPrefix,
		ReadOnlySecretTag:  r.readOnlySecretTag,
		SkipRegistration:   options.SkipMCPRegistration,
	})
	if errors.Is(err, dbmcp.ErrReadOnlySecretTagUnset) {
		return nil, "not started: set the tag of the read-only SQL Server secrets with `airlock config set readOnlySecretTag Key=Value` (or enable writes)"
	}
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}

func relativePaths(base string, paths []string) string {
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, err := filepath.Rel(base, path); err == nil {
			path = rel
		}
		relative = append(relative, path)
	}
	return strings.Join(relative, ", ")
}

// RunOnHangup runs fn when the terminal is closed (SIGHUP), then lets the signal terminate the process.
// SIGINT and SIGTERM are already turned into a graceful exit by the session TUI.
// The returned function stops watching.
func RunOnHangup(fn func()) (stop func()) {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, syscall.SIGHUP)

	go func() {
		select {
		case <-signals:
			fn()
			signal.Reset(syscall.SIGHUP)
			if err := raiseSignal(syscall.SIGHUP); err != nil {
				os.Exit(1)
			}
		case <-done:
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

// raiseSignal sends sig to the current process. It fails on Windows, which only delivers Kill.
func raiseSignal(sig os.Signal) error {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return self.Signal(sig)
}

// elastiCacheEngine defaults to redis when AWS does not report the engine.
func elastiCacheEngine(engine string) string {
	if strings.TrimSpace(engine) == "" {
		return "redis"
	}
	return engine
}

// sessionLabel identifies a target across profiles and regions, e.g. "RDS my-db (prod/eu-west-3)".
func sessionLabel(resourceType, targetName string, cfg awsConfig) string {
	return fmt.Sprintf("%s %s (%s/%s)", strings.ToUpper(resourceType), targetName, cfg.Profile, cfg.Region)
}

// reservePort reserves the requested local port, or the first free one from the first positive default.
func (r *Runner) reservePort(label string, requested int, defaults ...int) (int, error) {
	preferred := 0 // 0 lets the OS choose.
	for _, port := range defaults {
		if port > 0 && !r.randomLocalPort {
			preferred = port
			break
		}
	}

	port, err := r.registry.reserve(label, requested, preferred)
	if err != nil {
		return 0, err
	}
	if requested <= 0 && preferred > 0 && port != preferred {
		fprintf(r.out, "Local port %d is busy, using %d instead\n", preferred, port)
	}
	return port, nil
}

func firstValue(values map[string][]string, key string) string {
	if values == nil {
		return ""
	}

	list, ok := values[key]
	if !ok || len(list) == 0 {
		return ""
	}

	return strings.TrimSpace(list[0])
}

// describeAuth tells how the session signs in to the database; empty when it does not (not RDS, or
// credentials read from Secrets Manager).
// tokenCommand prints a temporary AWS IAM token for the database user of target, valid 15
// minutes; "" when the session does not sign in with AWS IAM.
func tokenCommand(target *ideTarget, cfg awsConfig) string {
	if !target.IAMAuth || target.RDSHost == "" {
		return ""
	}
	args := []string{"aws", "rds", "generate-db-auth-token", "--hostname", target.RDSHost, "--port", strconv.Itoa(target.RDSPort), "--username", target.DBUser}
	return strings.Join(append(args, cfg.Args...), " ")
}

func describeAuth(target *ideTarget) string {
	switch {
	case target.DBSecretARN != "":
		return "Secrets Manager · " + SecretDisplayName(target.DBSecretARN) + " · " + target.DBUser
	case target.IAMAuth:
		return "AWS IAM token (generated by airlock) · " + target.DBUser
	case target.DBPassword != "":
		return "password · " + target.DBUser
	default:
		return ""
	}
}

// restartDBUser is the --db-user of the restart command: AWS IAM and Secrets Manager sessions can
// be restarted unattended, a password session is restarted as an IAM one.
func restartDBUser(target *ideTarget) string {
	if !target.IAMAuth && target.DBSecretARN == "" {
		return ""
	}
	return target.DBUser
}

// restartDBSecret is the --db-secret of the restart command; empty unless the session reads a secret.
func restartDBSecret(target *ideTarget) string {
	return target.DBSecretARN
}

func buildRestartCommand(resourceType, targetName, localPort, dbUser, dbSecret string, cfg awsConfig) string {
	parts := []string{"airlock", resourceType}

	targetName = strings.TrimSpace(targetName)
	if targetName != "" {
		parts = append(parts, targetName)
	}

	localPort = strings.TrimSpace(localPort)
	if localPort != "" {
		parts = append(parts, localPort)
	}

	if cfg.Profile != "" {
		parts = append(parts, "--profile", cfg.Profile)
	}
	if cfg.Region != "" {
		parts = append(parts, "--region", cfg.Region)
	}
	if dbUser != "" {
		parts = append(parts, "--db-user", dbUser)
	}
	if dbSecret != "" {
		parts = append(parts, "--db-secret", dbSecret)
	}

	return strings.Join(parts, " ")
}

func (r *Runner) listElastiCacheResources(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	var resources []ResourceSummary

	// Replication groups (Redis/Valkey with replication).
	rgOutput, rgErr := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-replication-groups",
		"--query", "ReplicationGroups[*].{name:ReplicationGroupId,status:Status,engine:Engine,authToken:AuthTokenEnabled,userGroups:UserGroupIds,"+
			"host:NodeGroups[0].PrimaryEndpoint.Address,port:NodeGroups[0].PrimaryEndpoint.Port,cfgHost:ConfigurationEndpoint.Address,cfgPort:ConfigurationEndpoint.Port}",
		"--output", "json",
	)
	if rgErr == nil {
		var groups []struct {
			Name       string   `json:"name"`
			Status     string   `json:"status"`
			Engine     string   `json:"engine"`
			Host       string   `json:"host"`
			CfgHost    string   `json:"cfgHost"`
			UserGroups []string `json:"userGroups"`
			Port       int      `json:"port"`
			CfgPort    int      `json:"cfgPort"`
			AuthToken  bool     `json:"authToken"`
		}
		if json.Unmarshal([]byte(rgOutput), &groups) == nil {
			for i := range groups {
				g := &groups[i]
				name := strings.TrimSpace(g.Name)
				if name == "" {
					continue
				}
				host, port := g.Host, g.Port
				if host == "" {
					host, port = g.CfgHost, g.CfgPort
				}
				resources = append(resources, ResourceSummary{
					Name:        name,
					Description: formatElastiCacheResourceDescription(host, port, g.Status),
					Engine:      elastiCacheEngine(g.Engine),
					CacheAuth:   cacheAuthMode(g.AuthToken, g.UserGroups),
				})
			}
		}
	}

	// Standalone cache clusters (no replication group — includes single-node Valkey/Redis).
	clOutput, clErr := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-cache-clusters",
		"--show-cache-node-info",
		"--query", "CacheClusters[?!ReplicationGroupId].{name:CacheClusterId,status:CacheClusterStatus,engine:Engine,authToken:AuthTokenEnabled,host:CacheNodes[0].Endpoint.Address,port:CacheNodes[0].Endpoint.Port}",
		"--output", "json",
	)
	if clErr == nil {
		var clusters []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Engine    string `json:"engine"`
			Host      string `json:"host"`
			Port      int    `json:"port"`
			AuthToken bool   `json:"authToken"`
		}
		if json.Unmarshal([]byte(clOutput), &clusters) == nil {
			for _, c := range clusters {
				name := strings.TrimSpace(c.Name)
				if name == "" {
					continue
				}
				resources = append(resources, ResourceSummary{
					Name:        name,
					Description: formatElastiCacheResourceDescription(c.Host, c.Port, c.Status),
					Engine:      elastiCacheEngine(c.Engine),
					CacheAuth:   cacheAuthMode(c.AuthToken, nil),
				})
			}
		}
	}

	// Serverless caches (Valkey/Redis serverless — different API entirely).
	scOutput, scErr := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-serverless-caches",
		"--query", "ServerlessCaches[*].{name:ServerlessCacheName,status:Status,engine:Engine,userGroup:UserGroupId,host:Endpoint.Address,port:Endpoint.Port}",
		"--output", "json",
	)
	if scErr == nil {
		var serverless []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Engine    string `json:"engine"`
			Host      string `json:"host"`
			UserGroup string `json:"userGroup"`
			Port      int    `json:"port"`
		}
		if json.Unmarshal([]byte(scOutput), &serverless) == nil {
			for _, s := range serverless {
				name := strings.TrimSpace(s.Name)
				if name == "" {
					continue
				}
				var userGroups []string
				if s.UserGroup != "" {
					userGroups = []string{s.UserGroup}
				}
				resources = append(resources, ResourceSummary{
					Name: name, Description: formatElastiCacheResourceDescription(s.Host, s.Port, s.Status),
					Engine: elastiCacheEngine(s.Engine), CacheAuth: cacheAuthMode(false, userGroups),
				})
			}
		}
	}
	// One listing may fail on its own (a region without serverless caches); all failing means
	// the caches could not be listed at all.
	if rgErr != nil && clErr != nil && scErr != nil {
		return nil, i18n.NewError("jumphost.runner.failed_to_list_elasticache", errors.Join(rgErr, clErr, scErr))
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})

	return resources, nil
}

func (r *Runner) selectName(title string, names []string, prompt string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}

	if !isInteractive(r.in) {
		return "", i18n.NewError("jumphost.runner.name_is_required_available", title, strings.Join(names, ", "))
	}

	tw := tabwriter.NewWriter(r.out, 0, 0, 2, ' ', 0)
	fprintf(tw, "%s\n", title)
	fprintf(tw, "NAME\n")
	for _, name := range names {
		fprintf(tw, "%s\n", name)
	}
	_ = tw.Flush() //nolint:errcheck // best-effort terminal output

	fprintf(r.out, "%s: ", prompt)
	reader := bufio.NewReader(r.in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", i18n.NewError("jumphost.runner.failed_to_read_input", err)
	}
	return strings.TrimSpace(line), nil
}

// awsRunner runs AWS CLI commands with the profile and region of cfg.
func (r *Runner) awsRunner(cfg awsConfig) dbmcp.AWSRunner {
	args := append([]string{}, cfg.Args...)
	return func(ctx context.Context, commandArgs ...string) (string, error) {
		return r.runAWSCapture(ctx, args, commandArgs...)
	}
}

func (r *Runner) runAWSCapture(ctx context.Context, awsArgs []string, commandArgs ...string) (string, error) {
	args := append([]string{}, commandArgs...)
	args = append(args, awsArgs...)

	cmd := exec.CommandContext(ctx, "aws", args...) // #nosec G204 G702 -- the aws CLI with airlock's own arguments, no shell.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return "", i18n.NewError("jumphost.runner.aws_failed_output", strings.Join(commandArgs, " "), errText)
		}
		return "", i18n.NewError("jumphost.runner.aws_failed", strings.Join(commandArgs, " "), err)
	}

	return output, nil
}

func (r *Runner) runAWSStreaming(ctx context.Context, awsArgs []string, commandArgs ...string) error {
	args := append([]string{}, commandArgs...)
	args = append(args, awsArgs...)

	// Use exec.Command (not CommandContext) and start aws in its own process group,
	// so that it and its child process session-manager-plugin are both stopped
	// when the context is cancelled.
	cmd := exec.Command("aws", args...) // #nosec G204 G702 -- the aws CLI with airlock's own arguments, no shell.
	platform.StartInOwnGroup(cmd)
	cmd.Stdin = r.in
	cmd.Stdout = r.out
	cmd.Stderr = r.err

	if err := cmd.Start(); err != nil {
		return i18n.NewError("jumphost.runner.aws_failed_to_start", strings.Join(commandArgs, " "), err)
	}

	// Kill the entire process group when the context is cancelled.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			platform.TerminateGroup(cmd)
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	if err != nil {
		return i18n.NewError("jumphost.runner.aws_failed", strings.Join(commandArgs, " "), err)
	}

	return nil
}

func isInteractive(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // best-effort terminal output
}
