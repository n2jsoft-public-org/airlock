package jumphost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft-public-org/airlock/internal/awsapi"
	"github.com/n2jsoft-public-org/airlock/internal/awstag"
	"github.com/n2jsoft-public-org/airlock/internal/dbuser"
	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/keychain"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
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
	return resourceType == resourceEKS || resourceType == resourceRDS || resourceType == resourceElastiCache || resourceType == resourceDynamoDB || resourceType == resourceS3 || resourceType == resourceLogs || resourceType == resourceForward
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
	OpenedBy string `json:"openedBy,omitempty"`
	// Stack names the .airlock.json stack whose airlock up opened the session: airlock down closes
	// only those.
	Stack           string `json:"stack,omitempty"`
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
	// Interactive runs a command needing the user's terminal (airlock sso-login), purpose saying why;
	// nil runs it on In, Out and Err.
	Interactive InteractiveFunc
	// JumphostTag finds the SSM jumphost instance (Usage=jumphost when zero).
	JumphostTag awstag.Tag
	// ReadOnlySecretTag marks the read-only secrets of SQL Server instances; zero disables them.
	ReadOnlySecretTag awstag.Tag
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
	// DisabledModules are the modules (ModuleOf) whose targets are neither searched nor opened.
	DisabledModules []string
	// RandomLocalPort binds tunnels to a free port chosen by the OS instead of the target's port.
	RandomLocalPort bool
	// WritesLocked keeps every MCP server read-only, whatever the options or the saved choices ask
	// (the organization locks agent.writes off).
	WritesLocked bool
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
	// client returns the Client of the session; nil for a tunnel without credentials (EKS).
	client func(ctx context.Context, c *SignInCandidate) (Client, error)
	// Environment is the environment of a direct database's definition, and Production whether it
	// is the production environment (AWS sessions are matched by profile in the dashboard).
	Environment string
	// Intro describes the connection (display name, connection hints, IDE data source).
	Intro       []string
	LocalPort   int
	releaseOnce sync.Once
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
	// configs hands out the SDK configuration of each profile and region.
	configs *awsapi.Configs
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
	// kubeconfigDir receives the kubeconfig of EKS sessions; empty disables them.
	kubeconfigDir   string
	disabledModules []string
	accountsMu      sync.Mutex
	// loginMu serializes the SSO sign-ins of expired identities.
	loginMu         sync.Mutex
	keychainOnce    sync.Once
	randomLocalPort bool
	// writesLocked forces MCPAllowWrites off and keeps the saved one as it is.
	writesLocked bool
	keychainOK   bool
}

// attachMCP records the MCP server of session (nil when none started, see serveMCP) and its
// display line.
func (r *Runner) attachMCP(session *Session, server mcpserve.Session, display string) {
	if display != "" {
		session.Details.MCP = display
		session.Intro = append(session.Intro, "MCP "+display)
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
	// CacheTLS reports in-transit encryption; CacheServerless a serverless cache.
	CacheTLS        bool
	CacheServerless bool
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
		in: in, out: out, err: err, registry: registry, mcpHost: mcpHost, configs: awsapi.NewConfigs(),
		projectDir: opts.ProjectDir, mcpLogDir: opts.MCPLogDir, accounts: map[string]string{},
		defaultRegion: strings.TrimSpace(opts.DefaultRegion), defaultDBUser: opts.DefaultDBUser,
		jumphostTag: jumphostTag, readOnlySecretTag: opts.ReadOnlySecretTag,
		mcpServerPrefix: opts.MCPServerPrefix, kubeconfigDir: opts.KubeconfigDir,
		randomLocalPort: opts.RandomLocalPort,
		disabledModules: opts.DisabledModules,
		databases:       opts.Databases,
		containers:      opts.Containers,
		environments:    opts.Environments,
		interactive:     opts.Interactive,
		writesLocked:    opts.WritesLocked,
	}
	if opts.PrefsPath != "" {
		runner.prefs = NewPrefsStore(opts.PrefsPath)
		runner.keychain = opts.Keychain
	}
	return runner
}

// Run prepares a single session and streams it in the current terminal until it ends or a signal
// stops airlock: aws runs in a process group of its own, which no signal of the terminal reaches.
func (r *Runner) Run(ctx context.Context, options *Options) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	session, err := r.Prepare(ctx, *options)
	if err != nil {
		return err
	}
	defer func() {
		if msg := session.Release(); msg != "" {
			fprintf(r.out, "%s\n", msg)
		}
	}()

	if session.ResourceType == resourceDirect || session.ResourceType == resourceDocker || session.ResourceType == resourceDockerCache || APIOnly(session.ResourceType) {
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
	// Here rather than in the callers: no path to an MCP server can miss it.
	options.MCPAllowWrites = options.MCPAllowWrites && !r.writesLocked
	if err := r.checkModule(options.ResourceType); err != nil {
		return nil, err
	}
	// A direct database is reached without AWS: no CLI, identity, jumphost nor local port.
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceDirect) {
		return r.prepareDirect(ctx, &options)
	}
	if t := strings.ToLower(strings.TrimSpace(options.ResourceType)); t == resourceDocker || t == resourceDockerCache {
		return r.prepareDocker(ctx, &options)
	}
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceDynamoDB) {
		return r.prepareDynamoDB(ctx, &options)
	}
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceS3) {
		return r.prepareS3(ctx, &options)
	}
	if strings.EqualFold(strings.TrimSpace(options.ResourceType), resourceLogs) {
		return r.prepareLogs(ctx, &options)
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType != resourceEKS && resourceType != resourceRDS && resourceType != resourceElastiCache && resourceType != resourceForward {
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}
	if err := requireTunnelTools(); err != nil {
		return nil, err
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
	case resourceForward:
		prepared, err = r.prepareForward(cfg, &options)
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

	commandArgs, err := ssmForwardArgs(jumphostID, ssmParameters)
	if err != nil {
		return nil, err
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
		AwsArgs:     append([]string{}, cfg.Args...),
		CommandArgs: commandArgs,
		Intro:       append([]string{sessionDisplayName}, sessionInfo...),
	}
	if session.LocalPort, err = strconv.Atoi(localPort); err != nil {
		return nil, i18n.NewError("jumphost.runner.invalid_local_port", localPort, err)
	}
	var awsCfg *aws.Config // RDS only
	if resourceType == resourceRDS {
		c, err := r.sdkConfig(ctx, cfg)
		if err != nil {
			return nil, err
		}
		awsCfg = &c
		session.ReadCredentials = r.secretCredentialsReader(awsCfg, resolvedTargetName, &target)
		if session.Details.Auth == "" && session.ReadCredentials != nil {
			session.Details.Auth = "Secrets Manager · read-only secrets " + r.readOnlySecretTag.String()
		}
		session.client = rdsClient(awsCfg, &target, session.LocalPort)
	}
	if resourceType == resourceElastiCache {
		session.client = r.cacheClient(resolvedTargetName, &target, session.LocalPort, cfg)
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
		handle, display := r.startKubeMCP(ctx, &options, resolvedTargetName, host, target.ClusterCA, session.LocalPort, cfg)
		r.attachMCP(session, handle, display)
	}
	if resourceType == resourceRDS && !options.SkipMCP {
		handle, display := r.startMCP(ctx, &options, resolvedTargetName, session.LocalPort, &target, cfg, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	if resourceType == resourceElastiCache && !options.SkipMCP {
		forward := r.cacheNodeForward(launcherFrom(ctx), jumphostID, cfg.Args, label, net.JoinHostPort(host, strings.TrimSpace(remotePort)), session.LocalPort)
		handle, display := r.startCacheMCP(ctx, &options, resolvedTargetName, session.LocalPort, &target, cfg, forward, options.MCPAllowWrites)
		r.attachMCP(session, handle, display)
	}
	if (resourceType == resourceRDS && signInApplies(&target, options.MCPAllowWrites)) ||
		(resourceType == resourceElastiCache && cacheSignInApplies(&target)) {
		signIn := &sessionSignIn{
			runner: r, session: session, cfg: cfg, target: target,
			resourceType: resourceType, instance: resolvedTargetName, localPort: localPort,
			awsCfg: awsCfg,
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

// ssmForwardArgs are the aws arguments of an SSM port forward through jumphostID; parameters
// holds host, portNumber and localPortNumber.
func ssmForwardArgs(jumphostID string, parameters map[string][]string) ([]string, error) {
	raw, err := json.Marshal(parameters)
	if err != nil {
		return nil, i18n.NewError("jumphost.runner.failed_to_serialize_ssm", err)
	}
	return []string{
		"ssm", "start-session", "--target", jumphostID,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost", "--parameters", string(raw),
	}, nil
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

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // best-effort terminal output
}
