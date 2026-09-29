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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"

	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/ideadatasource"
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

type Options struct {
	ResourceType    string
	Name            string
	LocalPortNumber int
	AwsProfile      string
	AwsRegion       string
	// SkipIDEDataSource disables registering the connection in ./.idea/dataSources.xml.
	SkipIDEDataSource bool
	// SkipMCP disables the MCP server giving AI assistants access to tunneled databases.
	SkipMCP bool
	// MCPAllowWrites exposes the MCP execute tool. PostgreSQL writes as DBUser; SQL Server, without
	// AWS IAM sign-in, uses the master user secret.
	MCPAllowWrites bool
	// DBUser is the database user signing in with AWS IAM on RDS (defaults to RunnerOptions.DefaultDBUser).
	DBUser string
	// DBPassword signs DBUser in with a password instead of an AWS IAM token. It is only kept in
	// memory (the MCP server uses it; the IDE asks for it) and is never written to disk.
	DBPassword string
}

type ResourceSummary struct {
	Name        string
	Description string
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
	// Registry tracks ports and targets of parallel sessions; share it between runners of one process.
	Registry *SessionRegistry
	// MCPHost serves the MCP servers of database sessions; share it between runners of one process.
	MCPHost *dbmcp.Host
	// MCPLogDir receives the audit log of the SQL run through MCP servers; empty disables it.
	MCPLogDir string
	// ProjectDir is where the IntelliJ .idea directory is looked up (defaults to the working directory).
	ProjectDir string
	// PrefsPath is the file remembering the Connect step choices per region and database; empty disables it.
	PrefsPath string
	// DefaultRegion is used after AWS_REGION and the profile's region; empty means none.
	DefaultRegion string
	// DefaultDBUser signs in with AWS IAM on RDS when no user is chosen; empty means one is required.
	DefaultDBUser string
	// JumphostTag finds the SSM jumphost instance (Usage=jumphost when zero).
	JumphostTag awstag.Tag
	// ReadOnlySecretTag marks the read-only secrets of SQL Server instances; zero disables them.
	ReadOnlySecretTag awstag.Tag
	// MCPServerPrefix starts MCP server names (dbmcp.DefaultServerPrefix when empty).
	MCPServerPrefix string
	// ConfigPath is the settings file named in hints about missing settings.
	ConfigPath string
}

type SessionDetails struct {
	ConnectedServer string
	RestartCommand  string
	IDEDataSource   string
	// Auth describes how the database is signed in to, e.g. "AWS IAM token · app_ro" (empty when not applicable).
	Auth string
	// MCP describes the session's MCP server (empty when none runs).
	MCP string
}

// Session is a prepared port-forwarding session: its local port and target are reserved
// in the registry until Release is called.
type Session struct {
	release    func() string
	mcp        *dbmcp.Handle
	releaseMsg string
	Label      string
	// ResourceType (eks, rds, elasticache), Target, Profile and Region describe the connection for display.
	ResourceType string
	Target       string
	Profile      string
	Region       string
	Details      SessionDetails
	AwsArgs      []string
	CommandArgs  []string
	// Intro describes the connection (display name, connection hints, IDE data source).
	Intro       []string
	LocalPort   int
	releaseOnce sync.Once
}

// MCPServer is the session's running MCP server, nil when none runs.
func (s *Session) MCPServer() *dbmcp.Handle {
	return s.mcp
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
	mcpHost  *dbmcp.Host
	// prefs is nil when connection preferences are not remembered.
	prefs *PrefsStore
	// accounts caches the AWS account ID of each profile, learned from the identity check.
	accounts   map[string]string
	accountsMu sync.Mutex
	projectDir string
	mcpLogDir  string
	// defaultRegion and defaultDBUser are used when none is given; empty when not configured.
	defaultRegion string
	defaultDBUser string
	// jumphostTag finds the SSM jumphost instance; readOnlySecretTag the read-only SQL Server secrets.
	jumphostTag       awstag.Tag
	readOnlySecretTag awstag.Tag
	mcpServerPrefix   string
	// configPath is named in hints about missing settings.
	configPath string
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
	RDSPort    int
	// IAMAuth reports whether the session signs in with AWS IAM: the instance accepts it (not SQL
	// Server) and the user did not choose a password instead.
	IAMAuth bool
}

type awsConfig struct {
	Profile string
	Region  string
	Args    []string
}

func NewRunner(opts RunnerOptions) *Runner {
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
		mcpHost = dbmcp.NewHost()
	}

	jumphostTag := opts.JumphostTag
	if jumphostTag.IsZero() {
		jumphostTag = awstag.Tag{Key: defaultJumphostTagKey, Value: defaultJumphostTagValue}
	}
	runner := &Runner{
		in: in, out: out, err: err, registry: registry, mcpHost: mcpHost,
		projectDir: opts.ProjectDir, mcpLogDir: opts.MCPLogDir, accounts: map[string]string{},
		defaultRegion: strings.TrimSpace(opts.DefaultRegion), defaultDBUser: strings.TrimSpace(opts.DefaultDBUser),
		jumphostTag: jumphostTag, readOnlySecretTag: opts.ReadOnlySecretTag,
		mcpServerPrefix: opts.MCPServerPrefix, configPath: opts.ConfigPath,
	}
	if opts.PrefsPath != "" {
		runner.prefs = NewPrefsStore(opts.PrefsPath)
	}
	return runner
}

// Run prepares a single session and streams it in the current terminal until it ends.
func (r *Runner) Run(ctx context.Context, options Options) error {
	session, err := r.Prepare(ctx, options)
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

	fprintf(r.out, "Starting SSM session...\n")
	for _, line := range session.Intro {
		fprintf(r.out, "> %s\n", line)
	}
	fprintf(r.out, "Ctrl+C to cancel.\n")

	return r.runAWSStreaming(ctx, session.AwsArgs, session.CommandArgs...)
}

// Prepare resolves the target, reserves a free local port and registers the IDE data source.
// The caller starts the returned session (aws CommandArgs + AwsArgs) and must Release it once finished.
func (r *Runner) Prepare(ctx context.Context, options Options) (*Session, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, errors.New("AWS CLI is not installed. Please install it from https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html")
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType != resourceEKS && resourceType != resourceRDS && resourceType != resourceElastiCache {
		return nil, fmt.Errorf("unsupported resource type %q (supported: eks, rds, elasticache)", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}

	jumphostID, err := r.getJumphostID(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var sessionDisplayName string
	var sessionInfo []string
	var ssmParameters map[string][]string
	var target ideTarget
	var resolvedTargetName string

	switch resourceType {
	case resourceRDS:
		resolvedTargetName, sessionDisplayName, sessionInfo, ssmParameters, target, err = r.prepareRDS(ctx, cfg, options)
	case resourceEKS:
		resolvedTargetName, sessionDisplayName, sessionInfo, ssmParameters, target, err = r.prepareEKS(ctx, cfg, options)
	case resourceElastiCache:
		resolvedTargetName, sessionDisplayName, sessionInfo, ssmParameters, target, err = r.prepareElastiCache(ctx, cfg, options)
	}
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("failed to serialize SSM parameters: %w", err)
	}

	session := &Session{
		Label:        label,
		ResourceType: resourceType,
		Target:       resolvedTargetName,
		Profile:      cfg.Profile,
		Region:       cfg.Region,
		Details: SessionDetails{
			ConnectedServer: connectedServer,
			RestartCommand:  buildRestartCommand(resourceType, resolvedTargetName, localPort, restartDBUser(&target), cfg),
			Auth:            describeAuth(&target),
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
		return nil, fmt.Errorf("invalid local port %q: %w", localPort, err)
	}

	var removeDataSource func() string
	if !options.SkipIDEDataSource {
		session.Details.IDEDataSource, removeDataSource = r.registerIDEDataSource(resourceType, resolvedTargetName, localPort, target, cfg)
		if session.Details.IDEDataSource != "" {
			session.Intro = append(session.Intro, session.Details.IDEDataSource)
		}
	}
	var mcpServer *dbmcp.Handle
	if resourceType == resourceRDS && !options.SkipMCP {
		var mcpDisplay string
		mcpServer, mcpDisplay = r.startMCP(resolvedTargetName, session.LocalPort, &target, cfg, options.MCPAllowWrites)
		if mcpDisplay != "" {
			session.Details.MCP = mcpDisplay
			session.Intro = append(session.Intro, "MCP "+mcpDisplay)
		}
		session.mcp = mcpServer
	}
	session.release = sessionRelease(func() { r.registry.release(label) }, mcpServer, removeDataSource)

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

// ListAWSProfileSummaries returns available AWS profiles with display details.
func (r *Runner) ListAWSProfileSummaries(ctx context.Context) ([]AWSProfileSummary, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, errors.New("AWS CLI is not installed. Please install it from https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html")
	}

	rawProfiles, err := r.runAWSCapture(ctx, nil, "configure", "list-profiles")
	if err != nil {
		return nil, err
	}

	profileSet := map[string]struct{}{}
	for _, line := range strings.Split(rawProfiles, "\n") {
		profile := strings.TrimSpace(line)
		if profile == "" {
			continue
		}
		profileSet[profile] = struct{}{}
	}

	if len(profileSet) == 0 {
		profileSet[defaultAwsProfile] = struct{}{}
	}

	profiles := make([]string, 0, len(profileSet))
	for profile := range profileSet {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)

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
func (r *Runner) ListResourceNames(ctx context.Context, options Options) ([]string, error) {
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

// ListResourceSummaries returns available resources with display details for the given type.
func (r *Runner) ListResourceSummaries(ctx context.Context, options Options) ([]ResourceSummary, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, errors.New("AWS CLI is not installed. Please install it from https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html")
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType != resourceEKS && resourceType != resourceRDS && resourceType != resourceElastiCache {
		return nil, fmt.Errorf("unsupported resource type %q (supported: eks, rds, elasticache)", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}

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
			Port          int    `json:"port"`
			SizeGiB       int    `json:"size"`
			Status        string `json:"status"`
			IAMAuth       bool   `json:"iamAuth"`
		}
		if err := json.Unmarshal([]byte(listOutput), &instances); err != nil {
			return nil, fmt.Errorf("failed to parse RDS instances: %w", err)
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
			return nil, fmt.Errorf("failed to parse EKS clusters: %w", err)
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
		return nil, fmt.Errorf("unsupported resource type %q", options.ResourceType)
	}
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

func formatRDSResourceDescription(engine string, engineVersion string, host string, port int, sizeGiB int, status string) string {
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
// the remote port when it is free locally, otherwise the next free port above it.
func (r *Runner) ResolveDefaultLocalPort(ctx context.Context, options Options) (int, error) {
	port, err := r.resolvePreferredLocalPort(ctx, options)
	if err != nil {
		return 0, err
	}
	return r.registry.SuggestPort(port)
}

// LoadConnectionPrefs returns the Connect step choices saved for the RDS database of options, if any.
func (r *Runner) LoadConnectionPrefs(ctx context.Context, options Options) (ConnectionPrefs, bool) {
	db, ok := r.prefsDatabase(ctx, options)
	if !ok {
		return ConnectionPrefs{}, false
	}
	return r.prefs.Load(db)
}

// SaveConnectionPrefs remembers the Connect step choices of options for its RDS database: user,
// sign-in method, local port and MCP write access. The password itself is never saved.
func (r *Runner) SaveConnectionPrefs(ctx context.Context, options Options) error {
	db, ok := r.prefsDatabase(ctx, options)
	if !ok {
		return nil
	}
	signIn := SignInIAM
	if options.DBPassword != "" {
		signIn = SignInPassword
	}
	return r.prefs.Save(db, ConnectionPrefs{
		DBUser:         strings.TrimSpace(options.DBUser),
		SignIn:         signIn,
		LocalPort:      options.LocalPortNumber,
		MCPAllowWrites: options.MCPAllowWrites,
	})
}

// prefsDatabase identifies the database keying the preferences of options; false when none apply
// (not RDS, disabled, or the AWS account is unknown).
func (r *Runner) prefsDatabase(ctx context.Context, options Options) (DatabaseRef, bool) {
	if r.prefs == nil || strings.ToLower(strings.TrimSpace(options.ResourceType)) != resourceRDS || strings.TrimSpace(options.Name) == "" {
		return DatabaseRef{}, false
	}
	profile := resolveProfileName(options.AwsProfile)
	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return DatabaseRef{}, false
	}
	account := r.accountFor(ctx, profile)
	if account == "" {
		return DatabaseRef{}, false
	}
	return DatabaseRef{Account: account, Region: region, Name: strings.TrimSpace(options.Name)}, true
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

func (r *Runner) resolvePreferredLocalPort(ctx context.Context, options Options) (int, error) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	switch resourceType {
	case resourceEKS:
		return defaultEKSLocalPort, nil
	case resourceElastiCache:
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return 0, errors.New("ElastiCache cluster name is required")
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
			return 0, errors.New("RDS instance name is required")
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
			return 0, fmt.Errorf("failed to parse RDS instance details: %w", err)
		}
		if instance.Endpoint.Port == 0 {
			return 0, errors.New("RDS instance endpoint port is missing")
		}

		return instance.Endpoint.Port, nil
	default:
		return 0, fmt.Errorf("unsupported resource type %q (supported: eks, rds, elasticache)", options.ResourceType)
	}
}

func (r *Runner) resolveAWSConfig(ctx context.Context, options Options) (awsConfig, error) {
	profile := resolveProfileName(options.AwsProfile)

	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return awsConfig{}, fmt.Errorf(
			"no AWS region for profile %q: pass --region, set AWS_REGION, configure the profile's region (aws configure set region <region> --profile %s), or set \"defaultRegion\" in %s",
			profile, profile, r.configHint())
	}

	cfg := awsConfig{
		Profile: profile,
		Region:  region,
		Args:    []string{"--profile", profile, "--region", region},
	}

	fprintf(r.out, "Using AWS profile/region... %s/%s\n", cfg.Profile, cfg.Region)
	if err := r.ensureIdentity(ctx, cfg); err != nil {
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

// configHint names the settings file in messages about missing settings.
func (r *Runner) configHint() string {
	if r.configPath != "" {
		return r.configPath
	}
	return "the airlock config file"
}

func (r *Runner) ensureIdentity(ctx context.Context, cfg awsConfig) error {
	fprintf(r.out, "Using AWS identity... ")
	identity, err := r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err == nil {
		fprintf(r.out, "%s\n", identity)
		r.rememberAccount(cfg.Profile, identity)
		return nil
	}

	fprintf(r.out, "Failed. Reauthorize...\n")
	parentProfile, _ := r.runAWSCapture(ctx, nil, "configure", "get", "source_profile", "--profile", cfg.Profile)
	loginProfile := strings.TrimSpace(parentProfile)
	if loginProfile == "" {
		loginProfile = cfg.Profile
	}
	if loginProfile == "" {
		loginProfile = defaultAwsProfile
	}

	if authErr := r.runAWSStreaming(ctx, nil, "sso", "login", "--profile", loginProfile); authErr != nil {
		return errors.New("failed to authorize. Please check your AWS CLI configuration")
	}

	fprintf(r.out, "Using AWS identity... ")
	identity, err = r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err != nil {
		return errors.New("failed to retrieve current identity. Please check your AWS CLI configuration")
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
		return "", fmt.Errorf("no running jumphost instance tagged %s found in %s. Are you using the correct profile/region?", r.jumphostTag, cfg.Region)
	}

	fprintf(r.out, "%s\n", jumphostID)
	return jumphostID, nil
}

func (r *Runner) prepareRDS(ctx context.Context, cfg awsConfig, options Options) (string, string, []string, map[string][]string, ideTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[*].{name:DBInstanceIdentifier,address:Endpoint.Address,port:Endpoint.Port}",
			"--output", "json",
		)
		if err != nil {
			return "", "", nil, nil, ideTarget{}, err
		}

		var instances []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Port    int    `json:"port"`
		}
		if err := json.Unmarshal([]byte(listOutput), &instances); err != nil {
			return "", "", nil, nil, ideTarget{}, fmt.Errorf("failed to parse RDS instances: %w", err)
		}
		if len(instances) == 0 {
			return "", "", nil, nil, ideTarget{}, errors.New("no RDS instance found")
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
				return "", "", nil, nil, ideTarget{}, err
			}
			if selectedName == "" {
				return "", "", nil, nil, ideTarget{}, errors.New("RDS instance name is required")
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
		return "", "", nil, nil, ideTarget{}, err
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
		return "", "", nil, nil, ideTarget{}, fmt.Errorf("failed to parse RDS instance details: %w", err)
	}
	if instance.Endpoint.Address == "" || instance.Endpoint.Port == 0 {
		return "", "", nil, nil, ideTarget{}, errors.New("RDS instance endpoint is missing")
	}

	if options.DBPassword != "" && strings.TrimSpace(options.DBUser) == "" {
		return "", "", nil, nil, ideTarget{}, errors.New("a database user is required to sign in with a password")
	}
	iamAuth := instance.IAMDatabaseAuthenticationEnabled && options.DBPassword == ""
	if iamAuth && r.ResolveDBUser(options.DBUser) == "" {
		return "", "", nil, nil, ideTarget{}, fmt.Errorf(
			"a database user is required to sign in to %s with AWS IAM: pass --db-user, or set \"defaultDbUser\" in %s",
			instance.DBInstanceIdentifier, r.configHint())
	}

	localPort, err := r.reservePort(sessionLabel(resourceRDS, instance.DBInstanceIdentifier, cfg), options.LocalPortNumber, instance.Endpoint.Port)
	if err != nil {
		return "", "", nil, nil, ideTarget{}, err
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
	case options.DBPassword != "":
		dbUser = strings.TrimSpace(options.DBUser)
		sessionInfo = rdsPasswordSessionInfo(localPort, dbUser)
	case iamAuth:
		dbUser = r.ResolveDBUser(options.DBUser)
		sessionInfo = rdsIAMSessionInfo(localPort, instance.Endpoint.Address, instance.Endpoint.Port, dbUser, cfg, awsArgsHint)
	default:
		sessionInfo = rdsSecretsSessionInfo(localPort, instance.DBInstanceArn, instance.MasterUserSecret.SecretArn, instance.MasterUsername, r.readOnlySecretTag, awsArgsHint)
	}

	params := map[string][]string{
		"host":            {instance.Endpoint.Address},
		"portNumber":      {fmt.Sprintf("%d", instance.Endpoint.Port)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return instance.DBInstanceIdentifier, displayName, sessionInfo, params, ideTarget{
		Engine:          instance.Engine,
		Database:        instance.DBName,
		RDSHost:         instance.Endpoint.Address,
		RDSPort:         instance.Endpoint.Port,
		InstanceARN:     instance.DBInstanceArn,
		MasterSecretARN: instance.MasterUserSecret.SecretArn,
		IAMAuth:         iamAuth,
		DBUser:          dbUser,
		DBPassword:      options.DBPassword,
	}, nil
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

func (r *Runner) prepareEKS(ctx context.Context, cfg awsConfig, options Options) (string, string, []string, map[string][]string, ideTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"eks", "list-clusters",
			"--query", "clusters[*].{name:@}",
			"--output", "json",
		)
		if err != nil {
			return "", "", nil, nil, ideTarget{}, err
		}

		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(listOutput), &clusters); err != nil {
			return "", "", nil, nil, ideTarget{}, fmt.Errorf("failed to parse EKS clusters: %w", err)
		}
		if len(clusters) == 0 {
			return "", "", nil, nil, ideTarget{}, errors.New("no EKS cluster found")
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
				return "", "", nil, nil, ideTarget{}, err
			}
			if selectedName == "" {
				return "", "", nil, nil, ideTarget{}, errors.New("EKS cluster name is required")
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
		return "", "", nil, nil, ideTarget{}, err
	}

	var cluster struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	if err = json.Unmarshal([]byte(rawCluster), &cluster); err != nil {
		return "", "", nil, nil, ideTarget{}, fmt.Errorf("failed to parse EKS cluster details: %w", err)
	}
	if cluster.Endpoint == "" {
		return "", "", nil, nil, ideTarget{}, errors.New("EKS cluster endpoint is missing")
	}

	localPort, err := r.reservePort(sessionLabel(resourceEKS, cluster.Name, cfg), options.LocalPortNumber, defaultEKSLocalPort)
	if err != nil {
		return "", "", nil, nil, ideTarget{}, err
	}

	displayName := fmt.Sprintf("EKS %s (%s) -> localhost:%d", cluster.Name, cluster.Endpoint, localPort)
	params := map[string][]string{
		"host":            {strings.TrimPrefix(cluster.Endpoint, "https://")},
		"portNumber":      {"443"},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return cluster.Name, displayName, nil, params, ideTarget{}, nil
}

func (r *Runner) prepareElastiCache(ctx context.Context, cfg awsConfig, options Options) (string, string, []string, map[string][]string, ideTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		resources, err := r.listElastiCacheResources(ctx, cfg)
		if err != nil {
			return "", "", nil, nil, ideTarget{}, err
		}
		if len(resources) == 0 {
			return "", "", nil, nil, ideTarget{}, errors.New("no ElastiCache cluster found")
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
				return "", "", nil, nil, ideTarget{}, err
			}
			if selectedName == "" {
				return "", "", nil, nil, ideTarget{}, errors.New("ElastiCache cluster name is required")
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
			ReplicationGroupId string `json:"ReplicationGroupId"`
			Engine             string `json:"Engine"`
			Status             string `json:"Status"`
			NodeGroups         []struct {
				PrimaryEndpoint struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				} `json:"PrimaryEndpoint"`
			} `json:"NodeGroups"`
		}
		if jsonErr := json.Unmarshal([]byte(rawGroup), &group); jsonErr == nil &&
			len(group.NodeGroups) > 0 && group.NodeGroups[0].PrimaryEndpoint.Address != "" {
			endpoint := group.NodeGroups[0].PrimaryEndpoint
			localPort, reserveErr := r.reservePort(sessionLabel(resourceElastiCache, group.ReplicationGroupId, cfg), options.LocalPortNumber, endpoint.Port, defaultRedisPort)
			if reserveErr != nil {
				return "", "", nil, nil, ideTarget{}, reserveErr
			}
			displayName := fmt.Sprintf("ElastiCache %s %s:%d -> localhost:%d",
				group.ReplicationGroupId, endpoint.Address, endpoint.Port, localPort)
			sessionInfo := []string{
				fmt.Sprintf("host:port  = localhost:%d", localPort),
				fmt.Sprintf("remote     = %s:%d", endpoint.Address, endpoint.Port),
			}
			params := map[string][]string{
				"host":            {endpoint.Address},
				"portNumber":      {fmt.Sprintf("%d", endpoint.Port)},
				"localPortNumber": {fmt.Sprintf("%d", localPort)},
			}
			return group.ReplicationGroupId, displayName, sessionInfo, params, ideTarget{Engine: elastiCacheEngine(group.Engine)}, nil
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
			Engine             string `json:"Engine"`
			EngineVersion      string `json:"EngineVersion"`
			CacheClusterStatus string `json:"CacheClusterStatus"`
			CacheNodes         []struct {
				Endpoint struct {
					Address string `json:"Address"`
					Port    int    `json:"Port"`
				} `json:"Endpoint"`
			} `json:"CacheNodes"`
		}
		if json.Unmarshal([]byte(rawCluster), &cluster) == nil &&
			len(cluster.CacheNodes) > 0 && cluster.CacheNodes[0].Endpoint.Address != "" {
			endpoint := cluster.CacheNodes[0].Endpoint
			localPort, reserveErr := r.reservePort(sessionLabel(resourceElastiCache, cluster.CacheClusterId, cfg), options.LocalPortNumber, endpoint.Port, defaultRedisPort)
			if reserveErr != nil {
				return "", "", nil, nil, ideTarget{}, reserveErr
			}
			displayName := fmt.Sprintf("ElastiCache %s (%s %s) %s:%d -> localhost:%d",
				cluster.CacheClusterId, cluster.Engine, cluster.EngineVersion,
				endpoint.Address, endpoint.Port, localPort)
			sessionInfo := []string{
				fmt.Sprintf("host:port  = localhost:%d", localPort),
				fmt.Sprintf("remote     = %s:%d", endpoint.Address, endpoint.Port),
			}
			params := map[string][]string{
				"host":            {endpoint.Address},
				"portNumber":      {fmt.Sprintf("%d", endpoint.Port)},
				"localPortNumber": {fmt.Sprintf("%d", localPort)},
			}
			return cluster.CacheClusterId, displayName, sessionInfo, params, ideTarget{Engine: elastiCacheEngine(cluster.Engine)}, nil
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
		return "", "", nil, nil, ideTarget{}, fmt.Errorf("ElastiCache cluster %q not found: %w", name, err)
	}

	var sc struct {
		ServerlessCacheName string `json:"ServerlessCacheName"`
		Engine              string `json:"Engine"`
		EngineVersion       string `json:"MajorEngineVersion"`
		Status              string `json:"Status"`
		Endpoint            struct {
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Endpoint"`
	}
	if err = json.Unmarshal([]byte(rawServerless), &sc); err != nil {
		return "", "", nil, nil, ideTarget{}, fmt.Errorf("failed to parse serverless cache details: %w", err)
	}
	if sc.Endpoint.Address == "" {
		return "", "", nil, nil, ideTarget{}, fmt.Errorf("serverless cache %q endpoint is missing", name)
	}

	localPort, err := r.reservePort(sessionLabel(resourceElastiCache, sc.ServerlessCacheName, cfg), options.LocalPortNumber, sc.Endpoint.Port, defaultRedisPort)
	if err != nil {
		return "", "", nil, nil, ideTarget{}, err
	}

	displayName := fmt.Sprintf("ElastiCache serverless %s (%s %s) %s:%d -> localhost:%d",
		sc.ServerlessCacheName, sc.Engine, sc.EngineVersion,
		sc.Endpoint.Address, sc.Endpoint.Port, localPort)
	sessionInfo := []string{
		fmt.Sprintf("host:port  = localhost:%d", localPort),
		fmt.Sprintf("remote     = %s:%d", sc.Endpoint.Address, sc.Endpoint.Port),
	}
	params := map[string][]string{
		"host":            {sc.Endpoint.Address},
		"portNumber":      {fmt.Sprintf("%d", sc.Endpoint.Port)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return sc.ServerlessCacheName, displayName, sessionInfo, params, ideTarget{Engine: elastiCacheEngine(sc.Engine)}, nil
}

// registerIDEDataSource adds the forwarded connection to the IntelliJ project in the working directory.
// It returns a display message and a function removing the entry again and describing the outcome
// (nil when nothing was registered).
// Failures are reported as a warning and never abort the session.
func (r *Runner) registerIDEDataSource(resourceType, targetName, localPort string, target ideTarget, cfg awsConfig) (message string, remove func() string) {
	if strings.TrimSpace(target.Engine) == "" {
		return "", nil
	}

	port, err := strconv.Atoi(strings.TrimSpace(localPort))
	if err != nil || port <= 0 {
		return "", nil
	}

	projectDir := r.projectDir
	if projectDir == "" {
		projectDir, err = os.Getwd()
		if err != nil {
			return "", nil
		}
	}

	key := strings.Join([]string{resourceType, cfg.Profile, cfg.Region, targetName}, "|")
	dataSource := &ideadatasource.DataSource{
		Key:        key,
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
	result, err := ideadatasource.Register(projectDir, dataSource)
	if err != nil {
		fprintf(r.err, "Warning: failed to register IntelliJ data source: %v\n", err)
		return "", nil
	}
	if !result.Registered {
		return "", nil
	}

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

	return fmt.Sprintf("IntelliJ data source %q -> localhost:%d (%s)", result.Name, port, relativePaths(projectDir, result.Paths)), remove
}

// awsIAMAuthProvider is the IntelliJ authentication method "AWS IAM".
const awsIAMAuthProvider = "aws.rds.iam"

// ResolveDBUser returns the database user signing in with AWS IAM: user, else the configured
// default user; "" when neither is set.
func (r *Runner) ResolveDBUser(user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return r.defaultDBUser
}

// awsIAMProperties prefills the AWS Toolkit settings used by IntelliJ's "AWS IAM" authentication.
func awsIAMProperties(target ideTarget, cfg awsConfig) map[string]string {
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

// sessionRelease frees the port and target, then stops the MCP server and removes the IDE data source.
// It returns the cleanup messages joined with "; ".
func sessionRelease(releasePort func(), mcpServer *dbmcp.Handle, removeDataSource func() string) func() string {
	return func() string {
		releasePort()
		var messages []string
		if mcpServer != nil {
			messages = append(messages, mcpServer.Close())
		}
		if removeDataSource != nil {
			if msg := removeDataSource(); msg != "" {
				messages = append(messages, msg)
			}
		}
		return strings.Join(messages, "; ")
	}
}

// dbmcpStart starts a session's MCP server; a package variable so tests can force a failure.
var dbmcpStart = dbmcp.Start

// startMCP serves the MCP server of a database session and returns the display line describing
// it (empty when the engine is unsupported, so the session shows no MCP line at all). A failure
// to start is reported through the returned display rather than r.err, which is discarded in TUI
// mode.
func (r *Runner) startMCP(targetName string, localPort int, target *ideTarget, cfg awsConfig, allowWrites bool) (handle *dbmcp.Handle, display string) {
	projectDir := r.projectDir
	if projectDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Sprintf("not started: %v", err)
		}
		projectDir = wd
	}
	handle, err := dbmcpStart(r.mcpHost, &dbmcp.SessionConfig{
		AWS: func(ctx context.Context, args ...string) (string, error) {
			return r.runAWSCapture(ctx, cfg.Args, args...)
		},
		ProjectDir:        projectDir,
		Profile:           cfg.Profile,
		Region:            cfg.Region,
		Target:            targetName,
		AWSEngine:         target.Engine,
		RDSHost:           target.RDSHost,
		RDSPort:           target.RDSPort,
		DBName:            target.Database,
		InstanceARN:       target.InstanceARN,
		MasterSecretARN:   target.MasterSecretARN,
		DBUser:            target.DBUser,
		DBPassword:        target.DBPassword,
		IAMAuth:           target.IAMAuth,
		LocalPort:         localPort,
		AllowWrites:       allowWrites,
		LogDir:            r.mcpLogDir,
		ServerPrefix:      r.mcpServerPrefix,
		ReadOnlySecretTag: r.readOnlySecretTag,
	})
	if errors.Is(err, dbmcp.ErrReadOnlySecretTagUnset) {
		return nil, fmt.Sprintf("not started: set \"readOnlySecretTag\" in %s for read-only SQL Server access (or enable writes)", r.configHint())
	}
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	return handle, handle.Display
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
			if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
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
	preferred := 0
	for _, port := range defaults {
		if port > 0 {
			preferred = port
			break
		}
	}

	port, err := r.registry.reserve(label, requested, preferred)
	if err != nil {
		return 0, err
	}
	if requested <= 0 && port != preferred {
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
func describeAuth(target *ideTarget) string {
	switch {
	case target.IAMAuth:
		return "AWS IAM token (generated by airlock) · " + target.DBUser
	case target.DBPassword != "":
		return "password · " + target.DBUser
	default:
		return ""
	}
}

// restartDBUser is the --db-user of the restart command: only AWS IAM sign-in can be restarted
// unattended, a password session is restarted as an IAM one.
func restartDBUser(target *ideTarget) string {
	if !target.IAMAuth {
		return ""
	}
	return target.DBUser
}

func buildRestartCommand(resourceType string, targetName string, localPort string, dbUser string, cfg awsConfig) string {
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

	return strings.Join(parts, " ")
}

func (r *Runner) listElastiCacheResources(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	var resources []ResourceSummary

	// Replication groups (Redis/Valkey with replication).
	rgOutput, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-replication-groups",
		"--query", "ReplicationGroups[*].{name:ReplicationGroupId,status:Status,host:NodeGroups[0].PrimaryEndpoint.Address,port:NodeGroups[0].PrimaryEndpoint.Port}",
		"--output", "json",
	)
	if err == nil {
		var groups []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Host   string `json:"host"`
			Port   int    `json:"port"`
		}
		if json.Unmarshal([]byte(rgOutput), &groups) == nil {
			for _, g := range groups {
				name := strings.TrimSpace(g.Name)
				if name == "" {
					continue
				}
				resources = append(resources, ResourceSummary{
					Name:        name,
					Description: formatElastiCacheResourceDescription(g.Host, g.Port, g.Status),
				})
			}
		}
	}

	// Standalone cache clusters (no replication group — includes single-node Valkey/Redis).
	clOutput, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-cache-clusters",
		"--show-cache-node-info",
		"--query", "CacheClusters[?!ReplicationGroupId].{name:CacheClusterId,status:CacheClusterStatus,engine:Engine,host:CacheNodes[0].Endpoint.Address,port:CacheNodes[0].Endpoint.Port}",
		"--output", "json",
	)
	if err == nil {
		var clusters []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Engine string `json:"engine"`
			Host   string `json:"host"`
			Port   int    `json:"port"`
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
				})
			}
		}
	}

	// Serverless caches (Valkey/Redis serverless — different API entirely).
	scOutput, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-serverless-caches",
		"--query", "ServerlessCaches[*].{name:ServerlessCacheName,status:Status,engine:Engine,host:Endpoint.Address,port:Endpoint.Port}",
		"--output", "json",
	)
	if err == nil {
		var serverless []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Engine string `json:"engine"`
			Host   string `json:"host"`
			Port   int    `json:"port"`
		}
		if json.Unmarshal([]byte(scOutput), &serverless) == nil {
			for _, s := range serverless {
				name := strings.TrimSpace(s.Name)
				if name == "" {
					continue
				}
				resources = append(resources, ResourceSummary{
					Name:        name,
					Description: formatElastiCacheResourceDescription(s.Host, s.Port, s.Status),
				})
			}
		}
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
		return "", fmt.Errorf("name is required. Available %s: %s", title, strings.Join(names, ", "))
	}

	tw := tabwriter.NewWriter(r.out, 0, 0, 2, ' ', 0)
	fprintf(tw, "%s\n", title)
	fprintf(tw, "NAME\n")
	for _, name := range names {
		fprintf(tw, "%s\n", name)
	}
	_ = tw.Flush()

	fprintf(r.out, "%s: ", prompt)
	reader := bufio.NewReader(r.in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (r *Runner) runAWSCapture(ctx context.Context, awsArgs []string, commandArgs ...string) (string, error) {
	args := append([]string{}, commandArgs...)
	args = append(args, awsArgs...)

	cmd := exec.CommandContext(ctx, "aws", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return "", fmt.Errorf("aws %s failed: %s", strings.Join(commandArgs, " "), errText)
		}
		return "", fmt.Errorf("aws %s failed: %w", strings.Join(commandArgs, " "), err)
	}

	return output, nil
}

func (r *Runner) runAWSStreaming(ctx context.Context, awsArgs []string, commandArgs ...string) error {
	args := append([]string{}, commandArgs...)
	args = append(args, awsArgs...)

	// Use exec.Command (not CommandContext) and set Setpgid so that aws and its
	// child process session-manager-plugin share a process group. This lets us
	// kill both when the context is cancelled.
	cmd := exec.Command("aws", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = r.in
	cmd.Stdout = r.out
	cmd.Stderr = r.err

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("aws %s failed to start: %w", strings.Join(commandArgs, " "), err)
	}

	// Kill the entire process group when the context is cancelled.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	if err != nil {
		return fmt.Errorf("aws %s failed: %w", strings.Join(commandArgs, " "), err)
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
	_, _ = fmt.Fprintf(w, format, args...)
}
