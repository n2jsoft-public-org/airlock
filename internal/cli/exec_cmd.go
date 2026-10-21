package cli

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/connections"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// execReadyTimeout bounds the wait for the tunnel to listen and the sign-in to work; the AWS
// sign-in before it is not counted.
const execReadyTimeout = 2 * time.Minute

// execResourceTypes are the targets airlock exec opens: those a program connects to.
var execResourceTypes = []string{"rds", "eks", "elasticache", directResourceType, dockerResourceType}

// ExitCodeError ends airlock with Code and no message: the exit status of the command airlock
// exec ran.
type ExitCodeError struct{ Code int }

func (e *ExitCodeError) Error() string { return "exit status " + strconv.Itoa(e.Code) }

// newExecCmd runs a command with a session open and its connection settings in the environment.
func newExecCmd() *cobra.Command {
	options := jumphost.Options{}
	var stack bool
	cmd := &cobra.Command{
		Use:   "exec <rds|eks|elasticache|direct|docker> <name> [local-port] -- <command> [args...]",
		Short: i18n.T("cli.exec.short"),
		Long:  i18n.T("cli.exec.long"),
		Example: `  airlock exec rds my-db -- npm run migrate
  airlock exec eks my-cluster -- kubectl get pods
  airlock exec direct legacy-billing -- ./report.sh
  airlock exec --stack dev -- npm test`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stack {
				return runExecStack(cmd, args)
			}
			return runExec(cmd, args, new(options))
		},
	}
	cmd.Flags().BoolVar(&stack, "stack", false, i18n.T("cli.exec.flag.stack"))
	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", i18n.T("cli.root.flag.profile"))
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", i18n.T("cli.root.flag.aws-profile"))
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", i18n.T("cli.root.flag.region"))
	cmd.Flags().StringVar(&options.DBUser, "db-user", "", i18n.T("cli.root.flag.db-user"))
	cmd.Flags().StringVar(&options.DBSecretARN, "db-secret", "", i18n.T("cli.root.flag.db-secret"))
	registerCompletions(cmd)
	cmd.ValidArgsFunction = completeExecArgs
	return cmd
}

// completeExecArgs completes the target like the connect commands, then the command with files.
func completeExecArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch {
	case cmd.ArgsLenAtDash() >= 0:
		return nil, cobra.ShellCompDirectiveDefault
	case cmd.Flags().Changed("stack"):
		return completeStacks(cmd, args, toComplete)
	case len(args) == 0:
		types, directive := completeArgs(cmd, args, toComplete)
		return slices.DeleteFunc(types, func(t string) bool {
			name, _, _ := strings.Cut(t, "\t")
			return !slices.Contains(execResourceTypes, name)
		}), directive
	default:
		return completeArgs(cmd, args, toComplete)
	}
}

// runExec opens the target of args without dashboard, MCP server nor IDE data source, runs the
// command after -- with the connection settings in its environment, then closes the session.
func runExec(cmd *cobra.Command, args []string, options *jumphost.Options) error {
	target, command, err := splitExecArgs(args, cmd.ArgsLenAtDash())
	if err != nil {
		return err
	}
	options.ResourceType = strings.ToLower(strings.TrimSpace(target[0]))
	if !slices.Contains(execResourceTypes, options.ResourceType) {
		return i18n.NewError("cli.exec.unsupported_type", target[0])
	}
	return execWith(cmd, command, []jumphost.Options{*options}, nil, func(*jumphost.Options) []string { return target })
}

// runExecStack opens every target of a stack of the project's .airlock.json that a program
// connects to, then runs the command after -- with the settings of each (see stackExecVars).
func runExecStack(cmd *cobra.Command, args []string) error {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash == len(args) {
		return errors.New(i18n.T("cli.exec.stack_command_required"))
	}
	if dash > 1 {
		return errors.New(i18n.T("cli.exec.stack_one_name"))
	}
	for _, flag := range directRefusedFlags {
		if cmd.Flags().Changed(flag) {
			return i18n.NewError("cli.exec.stack_refused_flag", flag)
		}
	}
	file, err := loadStackFile()
	if err != nil {
		return err
	}
	name, list, err := file.Stack(firstArg(args[:dash]))
	if err != nil {
		return err
	}
	targets := slices.DeleteFunc(stackOptions(list), func(options jumphost.Options) bool { //nolint:gocritic // a few targets.
		skip := !slices.Contains(execResourceTypes, options.ResourceType)
		if skip {
			cmd.PrintErr(i18n.Format("cli.exec.stack_skipped", options.ResourceType+" "+options.Name))
		}
		return skip
	})
	if len(targets) == 0 {
		return i18n.NewError("cli.exec.stack_no_tunnel", name)
	}
	// Like airlock up, the targets are listed first and production ones confirmed: without a
	// terminal to confirm them, they are refused.
	confirm := func(envs environment.Set, databases jumphost.DirectDatabases) error {
		if !confirmStack(cmd, file.Path, name, targets, envs, databases, isInteractiveInput(cmd.InOrStdin())) {
			return errors.New(i18n.T("cli.up.cancelled"))
		}
		return nil
	}
	// The stack's port and credentials are those the flags would give.
	return execWith(cmd, args[dash:], targets, confirm, func(options *jumphost.Options) []string {
		target := []string{options.ResourceType, options.Name}
		if options.LocalPortNumber > 0 {
			target = append(target, strconv.Itoa(options.LocalPortNumber))
		}
		return target
	})
}

// execWith opens targets one after the other without dashboard, MCP server nor IDE data source,
// runs command with their connection settings in its environment, then closes them. args gives
// the type, name and local port of a target, as typed before --. confirm, when set, may stop the
// opening first.
func execWith(cmd *cobra.Command, command []string, targets []jumphost.Options, confirm func(environment.Set, jumphost.DirectDatabases) error, args func(*jumphost.Options) []string) error {
	cfg, sources, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	// Without a terminal nobody signs in nor types a password: the keychain or --db-secret must do.
	interactive := isInteractiveInput(cmd.InOrStdin())
	runner, runnerOptions := execRunner(cmd, &cfg, sources.org.Org)
	if confirm != nil {
		if refused := confirm(cfg.EnvironmentSet(), runnerOptions.Databases); refused != nil {
			return refused
		}
	}

	// Until the command runs, Ctrl+C and the like stop the opening and close what it started.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	opened := make([]*sessions.Standalone, 0, len(targets))
	defer func() {
		for _, s := range opened {
			s.Close()
		}
	}()
	vars := make([][]string, len(targets))
	for idx := range targets {
		options := &targets[idx]
		options.SkipMCP, options.SkipIDEDataSource, options.Unattended = true, true, !interactive
		s, openErr := openExecTarget(ctx, cmd, runner, &runnerOptions, args(options), options, interactive)
		if openErr != nil && len(targets) > 1 {
			return i18n.NewError("cli.up.target_failed", options.ResourceType+" "+options.Name, openErr)
		}
		if openErr != nil {
			return openErr
		}
		opened = append(opened, s)
		kubeconfig := s.Session.Details.Kubeconfig
		if options.ResourceType == "eks" && kubeconfig == "" {
			return i18n.NewError("cli.exec.no_kubeconfig", s.Session.Target)
		}
		vars[idx] = execVars(&s.Client, kubeconfig)
	}
	if len(targets) > 1 {
		if vars, err = stackExecVars(targets, vars); err != nil {
			return err
		}
	}
	var env []string
	for idx, s := range opened {
		names := make([]string, len(vars[idx]))
		for i, v := range vars[idx] {
			names[i], _, _ = strings.Cut(v, "=")
		}
		env = append(env, vars[idx]...)
		cmd.PrintErr(i18n.Format("cli.exec.ready", s.Session.Target, s.Client.Address.String(), strings.Join(names, " ")))
	}

	code, err := runCommand(command, append(os.Environ(), env...), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), interactive)
	if err != nil {
		return err
	}
	if code != 0 {
		return &ExitCodeError{Code: code}
	}
	return nil
}

// openExecTarget checks the target of options, typed as target, completes it with its saved
// choices and opens it standalone.
func openExecTarget(ctx context.Context, cmd *cobra.Command, runner *jumphost.Runner, runnerOptions *jumphost.RunnerOptions, target []string, options *jumphost.Options, interactive bool) (*sessions.Standalone, error) {
	var err error
	switch options.ResourceType {
	case directResourceType:
		options.Name, err = checkDirectTarget(cmd, target, runnerOptions.Databases)
	case dockerResourceType:
		options.Name, err = checkDockerTarget(cmd, target, runnerOptions.Containers, false)
	default:
		err = execAWSTarget(ctx, cmd.Flags(), runner, target, options, interactive)
	}
	if err != nil {
		return nil, err
	}
	opened, err := sessions.OpenStandalone(ctx, runner, sessions.AWSLauncher, options, execReadyTimeout)
	switch {
	case err != nil && options.ResourceType == directResourceType:
		return nil, directRunError(options.Name, err)
	case err != nil && options.ResourceType == dockerResourceType:
		return nil, dockerRunError(err)
	}
	return opened, err
}

// stackExecVars are the settings of the command airlock exec --stack runs, per target: each
// variable of the target's vars under the prefix of its name in upper case, other characters
// than letters and digits becoming _ (orders-db: ORDERS_DB_DATABASE_URL); when two targets get
// the same prefix, under their type and name (RDS_ORDERS_, ELASTICACHE_ORDERS_). A variable set
// by one target only is also set without prefix, as airlock exec sets it for one target.
func stackExecVars(targets []jumphost.Options, vars [][]string) ([][]string, error) {
	prefixes := make([]string, len(targets))
	count := map[string]int{}
	for idx := range targets {
		prefixes[idx] = envPrefix(targets[idx].Name)
		count[prefixes[idx]]++
	}
	seen := map[string]bool{}
	for idx := range targets {
		if count[prefixes[idx]] > 1 {
			prefixes[idx] = envPrefix(targets[idx].ResourceType + "_" + targets[idx].Name)
		}
		if seen[prefixes[idx]] {
			return nil, i18n.NewError("cli.exec.stack_prefix_clash", strings.TrimSuffix(prefixes[idx], "_"))
		}
		seen[prefixes[idx]] = true
	}
	setBy := map[string]int{}
	for _, list := range vars {
		for _, v := range list {
			key, _, _ := strings.Cut(v, "=")
			setBy[key]++
		}
	}
	out := make([][]string, len(vars))
	for idx, list := range vars {
		for _, v := range list {
			out[idx] = append(out[idx], prefixes[idx]+v)
		}
		for _, v := range list {
			if key, _, _ := strings.Cut(v, "="); setBy[key] == 1 {
				out[idx] = append(out[idx], v)
			}
		}
	}
	return out, nil
}

// envPrefix is name as the prefix of environment variables: upper case, other characters than
// letters and digits becoming _, a leading digit preceded by _, then _.
func envPrefix(name string) string {
	prefix := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
	if prefix == "" || prefix[0] >= '0' && prefix[0] <= '9' {
		prefix = "_" + prefix
	}
	return prefix + "_"
}

// splitExecArgs splits args at -- into the target (type, name, local port) and the command.
func splitExecArgs(args []string, dash int) (target, command []string, err error) {
	if dash < 0 || dash == len(args) {
		return nil, nil, errors.New(i18n.T("cli.exec.command_required"))
	}
	if dash < 2 || dash > 3 {
		return nil, nil, errors.New(i18n.T("cli.exec.target_required"))
	}
	return args[:dash], args[dash:], nil
}

// execRunner is the runner of airlock exec: its progress on stderr, keeping stdout for the command,
// and the saved connections, keychain passwords and kubeconfigs of the connect commands.
func execRunner(cmd *cobra.Command, cfg *config.Config, org *config.Organization) (*jumphost.Runner, jumphost.RunnerOptions) {
	kubeconfigDir, err := appdir.KubeconfigDir()
	if err != nil {
		cmd.PrintErr(i18n.Format("cli.root.warning_eks_sessions_will", err))
	}
	var prefsPath string
	var databaseStore *connections.Databases
	if saved, openErr := openSavedConnections(cmd.ErrOrStderr()); openErr != nil {
		cmd.PrintErr(i18n.Format("cli.root.warning_saved_connections_are", openErr))
	} else {
		prefsPath, databaseStore = saved.Path(), saved.Databases()
	}
	options := sharedRunnerOptions(cfg, org, nil, nil, "", kubeconfigDir, databaseStore)
	options.In, options.Out, options.Err = cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.ErrOrStderr()
	options.PrefsPath, options.Keychain = prefsPath, newKeychain()
	runner := jumphost.NewRunner(&options)
	return runner, options
}

// execAWSTarget fills options with the name and local port of target, then with the choices saved
// for it that the flags leave out, as the dashboard's recent connections do: profile, region,
// local port, user, sign-in method, secret, and the password remembered in the keychain.
func execAWSTarget(ctx context.Context, flags *pflag.FlagSet, runner *jumphost.Runner, target []string, options *jumphost.Options, interactive bool) error {
	options.Name = strings.TrimSpace(target[1])
	if len(target) > 2 {
		port, err := strconv.Atoi(target[2])
		if err != nil {
			return i18n.NewError("cli.root.invalid_local_port", target[2], err)
		}
		if port <= 0 {
			return i18n.NewError("cli.root.invalid_local_port_must", target[2])
		}
		options.LocalPortNumber = port
	}
	saved, err := runner.ListSavedConnections(ctx, options.AwsProfile, options.AwsRegion)
	if err != nil {
		return nil //nolint:nilerr // saved choices are a convenience: the flags alone still open the target
	}
	var matches []jumphost.SavedConnection
	for idx := range saved {
		if conn := saved[idx]; conn.Ref.Type == options.ResourceType && strings.EqualFold(conn.Ref.Name, options.Name) && !conn.ProfileMissing {
			matches = append(matches, conn)
		}
	}
	switch len(matches) {
	case 0:
		return nil
	case 1:
	default:
		places := make([]string, len(matches))
		for idx := range matches {
			places[idx] = matches[idx].Prefs.Profile + "/" + matches[idx].Ref.Region
		}
		return i18n.NewError("cli.exec.saved_in_several", options.ResourceType, options.Name, strings.Join(places, ", "))
	}
	conn := matches[0]
	options.Name = conn.Ref.Name
	if options.AwsProfile == "" {
		options.AwsProfile = conn.Prefs.Profile
	}
	if options.AwsRegion == "" {
		options.AwsRegion = conn.Ref.Region
	}
	if port := conn.Prefs.LocalPort; options.LocalPortNumber == 0 && port > 0 && runner.CheckLocalPort(port) == nil {
		options.LocalPortNumber = port
	}
	// The user or secret given, by a flag or the stack file, beats the saved ones.
	if flags.Changed("db-user") || flags.Changed("db-secret") || options.DBUser != "" || options.DBSecretARN != "" {
		return nil
	}
	options.DBUser, options.SignIn = conn.Prefs.DBUser, conn.Prefs.SignIn
	switch conn.Prefs.SignInMethod() {
	case jumphost.SignInSecret:
		options.DBSecretARN = conn.Prefs.DBSecretARN
	case jumphost.SignInPassword:
		// The keychain entry is found through the AWS account: the sign-in must work first.
		if interactive {
			err = runner.SignIn(ctx, options.AwsProfile, options.AwsRegion)
		} else {
			err = runner.CheckIdentity(ctx, options.AwsProfile, options.AwsRegion)
		}
		if err != nil {
			return err
		}
		password, ok := runner.LoadKeychainPassword(ctx, *options)
		if !ok {
			return i18n.NewError("cli.exec.no_saved_password", options.Name)
		}
		options.DBPassword = password
	}
	return nil
}

// execVars are the KEY=value settings of the command airlock exec runs: where and as whom to
// connect, in airlock's names, the engine's native ones and a URL, and KUBECONFIG for EKS.
func execVars(client *jumphost.Client, kubeconfig string) []string {
	addr := client.Address
	host, port := addr.Host, strconv.Itoa(addr.Port)
	var vars []string
	set := func(key, value string) {
		if value != "" {
			vars = append(vars, key+"="+value)
		}
	}
	set("AIRLOCK_HOST", host)
	set("AIRLOCK_PORT", port)
	set("AIRLOCK_USER", client.User)
	set("AIRLOCK_PASSWORD", client.Password)
	set("AIRLOCK_DATABASE", client.Database)
	set("KUBECONFIG", kubeconfig)
	var user *url.Userinfo
	switch {
	case client.User != "" && client.Password != "":
		user = url.UserPassword(client.User, client.Password)
	case client.User != "":
		user = url.User(client.User)
	}
	switch client.Engine {
	case string(dbmcp.EnginePostgres):
		sslMode := map[dbmcp.TLSMode]string{dbmcp.TLSDisable: "disable", dbmcp.TLSVerify: "verify-full"}[addr.TLS]
		if sslMode == "" {
			sslMode = "require" // the tunnel's certificate names the RDS host, not localhost
		}
		set("PGHOST", host)
		set("PGPORT", port)
		set("PGUSER", client.User)
		set("PGPASSWORD", client.Password)
		set("PGDATABASE", client.Database)
		set("PGSSLMODE", sslMode)
		u := url.URL{Scheme: "postgres", User: user, Host: addr.String(), Path: "/" + client.Database, RawQuery: "sslmode=" + sslMode}
		set("DATABASE_URL", u.String())
	case string(dbmcp.EngineSQLServer):
		set("SQLCMDSERVER", host+","+port)
		set("SQLCMDUSER", client.User)
		set("SQLCMDPASSWORD", client.Password)
		set("SQLCMDDBNAME", client.Database)
		query := url.Values{}
		if client.Database != "" {
			query.Set("database", client.Database)
		}
		switch addr.TLS {
		case dbmcp.TLSDisable:
			query.Set("encrypt", "disable")
		case dbmcp.TLSVerify:
			query.Set("encrypt", "true")
		default:
			query.Set("encrypt", "true")
			query.Set("TrustServerCertificate", "true")
		}
		u := url.URL{Scheme: "sqlserver", User: user, Host: addr.String(), RawQuery: query.Encode()}
		set("DATABASE_URL", u.String())
	case string(dbmcp.EngineMySQL), string(dbmcp.EngineMariaDB):
		set("MYSQL_HOST", host)
		set("MYSQL_TCP_PORT", port)
		set("MYSQL_PWD", client.Password)
		u := url.URL{Scheme: "mysql", User: user, Host: addr.String(), Path: "/" + client.Database}
		set("DATABASE_URL", u.String())
	case jumphost.ClientEngineRedis:
		u := url.URL{Scheme: "redis", User: user, Host: addr.String()}
		if addr.TLS != dbmcp.TLSDisable {
			u.Scheme = "rediss"
		}
		set("REDIS_URL", u.String())
		set("AIRLOCK_TLS_SERVER_NAME", client.ServerName)
	}
	return vars
}

// runCommand runs command with env and airlock's standard streams, and returns its exit status.
// The signals airlock gets are passed on, except Ctrl+C and hangups from a terminal: the command,
// in the same foreground process group, already got them.
func runCommand(command, env []string, in io.Reader, out, errOut io.Writer, terminal bool) (int, error) {
	child := exec.Command(command[0], command[1:]...) // #nosec G204 G702 -- the command the user asked airlock to run.
	child.Env, child.Stdin, child.Stdout, child.Stderr = env, in, out, errOut
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return 0, i18n.NewError("cli.exec.cannot_start", command[0], err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				if terminal && sig != syscall.SIGTERM {
					continue
				}
				_ = child.Process.Signal(sig) //nolint:errcheck // Windows signals nothing; the command may have ended.
			case <-done:
				return
			}
		}
	}()
	err := child.Wait()
	close(done)
	return exitStatus(err)
}

// exitStatus is the exit status of a command that ended with err: 128 plus the signal that
// killed it, as shells report it.
func exitStatus(err error) (int, error) {
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return 0, err
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return exitErr.ExitCode(), nil
}
