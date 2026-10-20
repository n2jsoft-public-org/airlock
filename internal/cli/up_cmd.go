package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/daemon"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/projectstack"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/ui"
	"github.com/n2jsoft/airlock/internal/version"
	"github.com/spf13/cobra"
)

// newUpCmd opens every target of a stack of the project's .airlock.json as sessions of the
// dashboard (CLI mode) or of the daemon (daemon mode).
func newUpCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "up [stack]",
		Short: i18n.T("cli.up.short"),
		Long:  i18n.T("cli.up.long"),
		Example: `  airlock up
  airlock up staging
  airlock up --list`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeStacks,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadStackFile()
			if err != nil {
				return err
			}
			if list {
				printStacks(cmd.OutOrStdout(), file)
				return nil
			}
			name, targets, err := file.Stack(firstArg(args))
			if err != nil {
				return err
			}
			return runUp(cmd, file.Path, name, stackOptions(targets))
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, i18n.T("cli.up.flag.list"))
	return cmd
}

// newDownCmd closes the daemon sessions of the targets of a stack.
func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "down [stack]",
		Short:             i18n.T("cli.down.short"),
		Long:              i18n.T("cli.down.long"),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeStacks,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadStackFile()
			if err != nil {
				return err
			}
			_, targets, err := file.Stack(firstArg(args))
			if err != nil {
				return err
			}
			cfg, _, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if cfg.Mode != config.ModeDaemon {
				cmd.PrintErr(i18n.T("cli.down.cli_mode"))
				return nil
			}
			paths, err := daemon.DefaultPaths()
			if err != nil {
				return err
			}
			// A daemon of another version still closes its sessions: its client is usable for that.
			client, _ := daemon.Running(cmd.Context(), paths, version.String()) //nolint:errcheck // a version mismatch comes with a usable client.
			if client == nil {
				cmd.PrintErr(i18n.T("cli.down.no_daemon"))
				return nil
			}
			defer func() { _ = client.Close() }() //nolint:errcheck // nothing left to do with it.
			return closeStack(cmd.Context(), cmd.ErrOrStderr(), client, stackOptions(targets))
		},
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// loadStackFile reads the .airlock.json of the working directory or its nearest parent.
func loadStackFile() (*projectstack.File, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // only the git root and the filesystem root stop the search.
	}
	path, err := projectstack.Find(dir, home)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, i18n.NewError("cli.up.no_file", projectstack.FileName, dir)
	}
	return projectstack.Load(path)
}

// completeStacks completes the stack names of the project's .airlock.json.
func completeStacks(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	file, err := loadStackFile()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, name := range file.Names() {
		if name == file.Default {
			name += "\t" + i18n.T("cli.up.default")
		}
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// stackOptions are the openings of targets, as airlock <type> <name> builds them.
func stackOptions(targets []projectstack.Target) []jumphost.Options {
	options := make([]jumphost.Options, len(targets))
	for idx := range targets {
		t := &targets[idx]
		options[idx] = jumphost.Options{
			ResourceType: t.Type, Name: t.Name, AwsProfile: t.Profile, AwsRegion: t.Region, DBUser: t.DBUser,
			DBSecretARN: t.DBSecret, LocalPortNumber: t.Port, MCPAllowWrites: t.MCPAllowWrites,
		}
	}
	return options
}

// describeTarget is the line naming the target of options: type, name, profile and region when
// given, local port when fixed.
func describeTarget(options *jumphost.Options) string {
	text := options.ResourceType + " " + options.Name
	if where := options.AwsProfile + "/" + options.AwsRegion; where != "/" {
		text += " (" + where + ")"
	}
	if options.LocalPortNumber > 0 {
		text += " → localhost:" + strconv.Itoa(options.LocalPortNumber)
	}
	return text
}

// printStacks prints every stack of file and its targets.
func printStacks(out io.Writer, file *projectstack.File) {
	printTo(out, file.Path+"\n")
	for _, name := range file.Names() {
		_, targets, _ := file.Stack(name) //nolint:errcheck // name is one of file's stacks.
		if name == file.Default {
			name += " (" + i18n.T("cli.up.default") + ")"
		}
		printTo(out, name+"\n")
		options := stackOptions(targets)
		for idx := range options {
			printTo(out, "  "+describeTarget(&options[idx])+"\n")
		}
	}
}

// runUp opens the targets of stack, read from path, then shows the dashboard of the mode.
func runUp(cmd *cobra.Command, path, stack string, targets []jumphost.Options) error {
	if !isInteractiveInput(cmd.InOrStdin()) {
		return errors.New(i18n.T("cli.up.needs_terminal"))
	}
	cfg, sources, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	env, err := newDashboardEnv(cmd, &cfg, &sources)
	if err != nil {
		return err
	}
	if !confirmStack(cmd, path, stack, targets, cfg.EnvironmentSet(), env.shared.Databases) {
		return errors.New(i18n.T("cli.up.cancelled"))
	}
	// Until the dashboard runs, Ctrl+C and the like stop the openings.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	open := func(service sessions.Service, base *jumphost.Options) (sessions.ID, error) {
		for idx := range targets {
			targets[idx].ProjectDir = base.ProjectDir
		}
		first, failed, openErr := openStack(ctx, cmd.ErrOrStderr(), service, targets, terminalInteractive(cmd))
		if openErr == nil && failed {
			// The dashboard takes the screen: the failures are read first.
			cmd.PrintErr(i18n.T("cli.up.press_enter"))
			_, _ = bufio.NewReader(cmd.InOrStdin()).ReadString('\n') //nolint:errcheck // any answer, or none, goes on.
		}
		stop()
		return first, openErr
	}
	if shouldUseDaemon(&cfg, true) {
		return runDaemonView(cmd, &jumphost.Options{}, open, env.discovery, &env.dashboard)
	}
	first, err := open(env.manager, &jumphost.Options{ProjectDir: env.dashboard.ProjectDir})
	if err != nil {
		env.manager.Shutdown()
		return err
	}
	return ui.RunJumphostSessions(cmd.Context(), env.manager, env.discovery, first, jumphost.Options{}, cmd.OutOrStdout(), env.dashboard)
}

// confirmStack prints the targets of stack with their environment, and asks before opening
// production ones.
func confirmStack(cmd *cobra.Command, path, stack string, targets []jumphost.Options, envs environment.Set, databases jumphost.DirectDatabases) bool {
	cmd.PrintErr(i18n.Format("cli.up.plan", stack, path))
	production := 0
	for idx := range targets {
		line := describeTarget(&targets[idx])
		if env := targetEnvironment(&targets[idx], envs, databases); !env.IsZero() {
			line += " [" + env.Name + "]"
			if env.Production {
				production++
			}
		}
		cmd.PrintErr("  " + line + "\n")
	}
	return production == 0 || confirm(cmd, i18n.Format("cli.up.confirm_production", production))
}

// targetEnvironment is the environment of the target of options: a direct database's, none for
// a Docker container, else its AWS profile's.
func targetEnvironment(options *jumphost.Options, envs environment.Set, databases jumphost.DirectDatabases) environment.Env {
	switch options.ResourceType {
	case directResourceType:
		if databases == nil {
			return environment.Env{}
		}
		db, found, err := databases.Get(options.Name)
		if err != nil || !found {
			return environment.Env{} // the opening reports it.
		}
		return envs.Lookup(db.Environment)
	case dockerResourceType:
		return environment.Env{}
	default:
		return envs.Match(effectiveProfile(options.AwsProfile))
	}
}

// openStack opens targets in service one after the other, in order: the first opening of a
// profile signs it in, the next ones find the sign-in valid. A target already running is reused;
// one that fails does not stop the others. It returns the first session, whether a target failed,
// and an error when none is open or ctx ended.
func openStack(ctx context.Context, out io.Writer, service sessions.Service, targets []jumphost.Options, interactive jumphost.InteractiveFunc) (sessions.ID, bool, error) {
	snapshot, err := service.Snapshot(ctx)
	if err != nil {
		return 0, false, err
	}
	var first sessions.ID
	var failures []error
	for idx := range targets {
		options := &targets[idx]
		label := options.ResourceType + " " + options.Name
		id := runningSessionOf(snapshot.Sessions, options)
		if id != 0 {
			printTo(out, i18n.Format("cli.up.running", label))
		} else {
			printTo(out, i18n.Format("cli.up.opening", label))
			id, err = service.Open(ctx, sessions.OpenRequest{Options: *options, Interactive: interactive})
			if ctx.Err() != nil {
				return first, true, ctx.Err()
			}
			if err != nil {
				failures = append(failures, i18n.NewError("cli.up.target_failed", label, err))
				printTo(out, i18n.Format("cli.up.failed", i18n.Localize(err)))
				continue
			}
		}
		if first == 0 {
			first = id
		}
	}
	printTo(out, i18n.Format("cli.up.summary", len(targets)-len(failures), len(targets)))
	if first == 0 {
		return 0, true, errors.Join(failures...)
	}
	return first, len(failures) > 0, nil
}

// closeStack stops the running sessions of targets in service, whoever opened them.
func closeStack(ctx context.Context, out io.Writer, service sessions.Service, targets []jumphost.Options) error {
	snapshot, err := service.Snapshot(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for idx := range targets {
		label := targets[idx].ResourceType + " " + targets[idx].Name
		id := runningSessionOf(snapshot.Sessions, &targets[idx])
		if id == 0 {
			printTo(out, i18n.Format("cli.down.not_running", label))
			continue
		}
		if err := service.Stop(ctx, id); err != nil {
			errs = append(errs, i18n.NewError("cli.down.failed", label, err))
			continue
		}
		printTo(out, i18n.Format("cli.down.closed", label))
	}
	return errors.Join(errs...)
}

// printTo writes text to out, the terminal: best effort.
func printTo(out io.Writer, text string) {
	_, _ = io.WriteString(out, text) //nolint:errcheck // best-effort terminal output
}
