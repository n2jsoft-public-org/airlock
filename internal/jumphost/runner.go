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
	"sort"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

const (
	resourceEKS       = "eks"
	resourceRDS       = "rds"
	defaultRegion     = "eu-west-3"
	defaultAwsProfile = "default"
)

type Options struct {
	ResourceType    string
	Name            string
	LocalPortNumber int
	AwsProfile      string
	AwsRegion       string
}

type ResourceSummary struct {
	Name        string
	Description string
}

type AWSProfileSummary struct {
	Name        string
	Description string
}

type RunnerOptions struct {
	In              io.Reader
	Out             io.Writer
	Err             io.Writer
	SessionExecutor SessionExecutor
}

type SessionExecutor func(ctx context.Context, awsArgs []string, commandArgs []string, in io.Reader, out io.Writer, errOut io.Writer) error

type Runner struct {
	in              io.Reader
	out             io.Writer
	err             io.Writer
	sessionExecutor SessionExecutor
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

	return &Runner{in: in, out: out, err: err, sessionExecutor: opts.SessionExecutor}
}

func (r *Runner) Run(ctx context.Context, options Options) error {
	if _, err := exec.LookPath("aws"); err != nil {
		return errors.New("AWS CLI is not installed. Please install it from https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html")
	}

	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType != resourceEKS && resourceType != resourceRDS {
		return fmt.Errorf("unsupported resource type %q (supported: eks, rds)", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return err
	}

	jumphostID, err := r.getJumphostID(ctx, cfg)
	if err != nil {
		return err
	}

	var sessionDisplayName string
	var sessionInfo []string
	var ssmParameters map[string][]string

	switch resourceType {
	case resourceRDS:
		sessionDisplayName, sessionInfo, ssmParameters, err = r.prepareRDS(ctx, cfg, options)
	case resourceEKS:
		sessionDisplayName, sessionInfo, ssmParameters, err = r.prepareEKS(ctx, cfg, options)
	}
	if err != nil {
		return err
	}

	parameters, err := json.Marshal(ssmParameters)
	if err != nil {
		return fmt.Errorf("failed to serialize SSM parameters: %w", err)
	}

	fprintf(r.out, "Starting SSM session...\n")
	fprintf(r.out, "> %s\n", sessionDisplayName)
	for _, line := range sessionInfo {
		fprintf(r.out, "> %s\n", line)
	}
	fprintf(r.out, "Ctrl+C to cancel.\n")

	ssmCommandArgs := []string{
		"ssm", "start-session",
		"--target", jumphostID,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", string(parameters),
	}

	if r.sessionExecutor != nil {
		return r.sessionExecutor(ctx, cfg.Args, ssmCommandArgs, r.in, r.out, r.err)
	}

	return r.runAWSStreaming(ctx, cfg.Args, ssmCommandArgs...)
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
	if resourceType != resourceEKS && resourceType != resourceRDS {
		return nil, fmt.Errorf("unsupported resource type %q (supported: eks, rds)", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}

	switch resourceType {
	case resourceRDS:
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[*].{name:DBInstanceIdentifier,engine:Engine,engineVersion:EngineVersion,host:Endpoint.Address,port:Endpoint.Port,size:AllocatedStorage,status:DBInstanceStatus}",
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
	default:
		return nil, fmt.Errorf("unsupported resource type %q", options.ResourceType)
	}
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

// ResolveDefaultLocalPort computes the recommended local port for a selected target.
func (r *Runner) ResolveDefaultLocalPort(ctx context.Context, options Options) (int, error) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	switch resourceType {
	case resourceEKS:
		return 9444, nil
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
		return 0, fmt.Errorf("unsupported resource type %q (supported: eks, rds)", options.ResourceType)
	}
}

func (r *Runner) resolveAWSConfig(ctx context.Context, options Options) (awsConfig, error) {
	profile := strings.TrimSpace(options.AwsProfile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if profile == "" {
		profile = defaultAwsProfile
	}

	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = strings.TrimSpace(os.Getenv("AWS_REGION"))
	}
	if region == "" {
		configuredRegion, _ := r.runAWSCapture(ctx, nil, "configure", "get", "region", "--profile", profile)
		region = strings.TrimSpace(configuredRegion)
	}
	if region == "" {
		region = defaultRegion
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

func (r *Runner) ensureIdentity(ctx context.Context, cfg awsConfig) error {
	fprintf(r.out, "Using AWS identity... ")
	identity, err := r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err == nil {
		fprintf(r.out, "%s\n", identity)
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
	return nil
}

func (r *Runner) getJumphostID(ctx context.Context, cfg awsConfig) (string, error) {
	fprintf(r.out, "Using jumphost... ")
	jumphostID, err := r.runAWSCapture(ctx, cfg.Args,
		"ec2", "describe-instances",
		"--filters", "Name=tag:Usage,Values=jumphost", "Name=instance-state-name,Values=running",
		"--output", "text",
		"--query", "Reservations[0].Instances[0].InstanceId",
		"--no-cli-pager",
	)
	if err != nil {
		return "", err
	}
	if jumphostID == "" || strings.EqualFold(jumphostID, "none") {
		return "", fmt.Errorf("no jumphost found in %s. Are you using the correct profile/region?", cfg.Region)
	}

	fprintf(r.out, "%s\n", jumphostID)
	return jumphostID, nil
}

func (r *Runner) prepareRDS(ctx context.Context, cfg awsConfig, options Options) (string, []string, map[string][]string, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"rds", "describe-db-instances",
			"--query", "DBInstances[*].{name:DBInstanceIdentifier,address:Endpoint.Address,port:Endpoint.Port}",
			"--output", "json",
		)
		if err != nil {
			return "", nil, nil, err
		}

		var instances []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Port    int    `json:"port"`
		}
		if err := json.Unmarshal([]byte(listOutput), &instances); err != nil {
			return "", nil, nil, fmt.Errorf("failed to parse RDS instances: %w", err)
		}
		if len(instances) == 0 {
			return "", nil, nil, errors.New("no RDS instance found")
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
				return "", nil, nil, err
			}
			if selectedName == "" {
				return "", nil, nil, errors.New("RDS instance name is required")
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
		return "", nil, nil, err
	}

	var instance struct {
		Engine               string `json:"Engine"`
		DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
		Endpoint             struct {
			Address string `json:"Address"`
			Port    int    `json:"Port"`
		} `json:"Endpoint"`
	}
	if err := json.Unmarshal([]byte(rawInstance), &instance); err != nil {
		return "", nil, nil, fmt.Errorf("failed to parse RDS instance details: %w", err)
	}
	if instance.Endpoint.Address == "" || instance.Endpoint.Port == 0 {
		return "", nil, nil, errors.New("RDS instance endpoint is missing")
	}

	localPort := options.LocalPortNumber
	if localPort <= 0 {
		localPort = instance.Endpoint.Port
	}

	displayName := fmt.Sprintf(
		"RDS %s %s:%d -> localhost:%d",
		instance.Engine,
		instance.DBInstanceIdentifier,
		instance.Endpoint.Port,
		localPort,
	)

	awsArgsHint := strings.Join(cfg.Args, " ")
	sessionInfo := []string{
		"For tools which support AWS IAM authentication, use:",
		fmt.Sprintf("  host:port            = localhost:%d", localPort),
		"  user                 = acme_ro",
		fmt.Sprintf("  region               = %s", cfg.Region),
		fmt.Sprintf("  credential/profile   = %s", cfg.Profile),
		fmt.Sprintf("  rds host:port        = %s:%d", instance.Endpoint.Address, instance.Endpoint.Port),
		"",
		"Otherwise, generate a temporary token (valid for 15 minutes) with:",
		fmt.Sprintf("  aws rds generate-db-auth-token --hostname %s --port %d --username acme_ro %s", instance.Endpoint.Address, instance.Endpoint.Port, awsArgsHint),
	}

	params := map[string][]string{
		"host":            {instance.Endpoint.Address},
		"portNumber":      {fmt.Sprintf("%d", instance.Endpoint.Port)},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return displayName, sessionInfo, params, nil
}

func (r *Runner) prepareEKS(ctx context.Context, cfg awsConfig, options Options) (string, []string, map[string][]string, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		listOutput, err := r.runAWSCapture(ctx, cfg.Args,
			"eks", "list-clusters",
			"--query", "clusters[*].{name:@}",
			"--output", "json",
		)
		if err != nil {
			return "", nil, nil, err
		}

		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(listOutput), &clusters); err != nil {
			return "", nil, nil, fmt.Errorf("failed to parse EKS clusters: %w", err)
		}
		if len(clusters) == 0 {
			return "", nil, nil, errors.New("no EKS cluster found")
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
				return "", nil, nil, err
			}
			if selectedName == "" {
				return "", nil, nil, errors.New("EKS cluster name is required")
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
		return "", nil, nil, err
	}

	var cluster struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(rawCluster), &cluster); err != nil {
		return "", nil, nil, fmt.Errorf("failed to parse EKS cluster details: %w", err)
	}
	if cluster.Endpoint == "" {
		return "", nil, nil, errors.New("EKS cluster endpoint is missing")
	}

	localPort := options.LocalPortNumber
	if localPort <= 0 {
		localPort = 9444
	}

	displayName := fmt.Sprintf("EKS %s (%s) -> localhost:%d", cluster.Name, cluster.Endpoint, localPort)
	params := map[string][]string{
		"host":            {strings.TrimPrefix(cluster.Endpoint, "https://")},
		"portNumber":      {"443"},
		"localPortNumber": {fmt.Sprintf("%d", localPort)},
	}

	return displayName, nil, params, nil
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

	cmd := exec.CommandContext(ctx, "aws", args...)
	cmd.Stdin = r.in
	cmd.Stdout = r.out
	cmd.Stderr = r.err

	if err := cmd.Run(); err != nil {
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
