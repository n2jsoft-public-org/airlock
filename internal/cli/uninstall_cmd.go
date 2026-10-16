package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"

	"github.com/n2jsoft/airlock/internal/agentskill"
	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/gateway"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/uninstall"
	"github.com/spf13/cobra"
)

// uninstallEnv is what airlock uninstall takes from the machine; tests replace it.
type uninstallEnv struct {
	executable func() (string, error)
	home       func() (string, error)
	getenv     func(string) string
	dataDir    func() (string, error)
	keychain   func() keychain.Store
	userPath   func() (string, error)
	// otherInstances counts the other airlocks running from exe, the daemon of daemonPID aside.
	otherInstances func(exe string, daemonPID int) (int, error)
	// findDaemon describes the daemon of the daemon mode; nil when none runs.
	findDaemon func(ctx context.Context) *runningDaemon
	// stopDaemon stops that daemon and its sessions; false when none ran any more.
	stopDaemon func(ctx context.Context) (bool, error)
	// gatewayEntries returns the files holding the MCP gateway entry and how to remove it.
	gatewayEntries func() (files []string, remove func(path string) error)
	// agentSkills returns the user-level airlock skill directories.
	agentSkills func() []string
	interactive func(io.Reader) bool
	goos        string
}

// runningDaemon is the daemon airlock uninstall stops once the removal is confirmed.
type runningDaemon struct {
	PID      int
	Sessions int
}

func defaultUninstallEnv() *uninstallEnv {
	return &uninstallEnv{
		executable:     selfupdate.Executable,
		home:           os.UserHomeDir,
		getenv:         os.Getenv,
		dataDir:        appdir.ConfigDir,
		keychain:       availableKeychain,
		userPath:       uninstall.UserPath,
		otherInstances: func(exe string, daemonPID int) (int, error) { return uninstall.OtherInstances(exe, daemonPID) },
		findDaemon:     findDaemon,
		stopDaemon:     stopDaemon,
		gatewayEntries: defaultGatewayEntries,
		agentSkills:    defaultAgentSkills,
		interactive:    isInteractiveInput,
		goos:           runtime.GOOS,
	}
}

// availableKeychain returns the OS keychain, or nil when it does not answer (a headless Linux).
func availableKeychain() keychain.Store {
	store := keychain.NewOSStore()
	if !keychain.Available(store) {
		return nil
	}
	return store
}

// newUninstallCmd removes airlock from this computer.
func newUninstallCmd() *cobra.Command {
	return newUninstallCmdWith(defaultUninstallEnv())
}

func newUninstallCmdWith(env *uninstallEnv) *cobra.Command {
	var purge, yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: i18n.T("cli.uninstall.short"),
		Long:  i18n.T("cli.uninstall.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, env, purge, yes)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, i18n.T("cli.uninstall.flag.purge"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("cli.uninstall.flag.yes"))
	return cmd
}

func runUninstall(cmd *cobra.Command, env *uninstallEnv, purge, yes bool) error {
	machine, err := env.machine()
	if err != nil {
		return err
	}
	// The daemon may run from this binary too: it is stopped with the removal, not counted here.
	running := env.findDaemon(cmd.Context())
	daemonPID := 0
	if running != nil {
		daemonPID = running.PID
	}
	others, err := env.otherInstances(machine.Executable, daemonPID)
	if err != nil {
		return err
	}
	if others > 0 {
		return i18n.NewError("cli.uninstall.another_airlock_is_running", machine.Executable)
	}
	plan, err := uninstall.NewPlan(machine, purge)
	if err != nil {
		return err
	}
	printUninstallPlan(cmd, &plan, running)
	if !yes {
		if !env.interactive(cmd.InOrStdin()) {
			return errors.New(i18n.T("cli.uninstall.airlock_uninstall_asks_before"))
		}
		if !confirm(cmd, i18n.T("cli.uninstall.remove_airlock_y")) {
			return errors.New(i18n.T("cli.uninstall.nothing_removed"))
		}
	}

	// Its sessions only end once the removal is confirmed.
	if running != nil {
		stopped, stopErr := env.stopDaemon(cmd.Context())
		if stopErr != nil {
			return i18n.NewError("cli.uninstall.failed_to_stop_airlock", stopErr)
		}
		if stopped {
			outf(cmd, "%s", i18n.T("cli.uninstall.stopped_airlock_daemon_and"))
		}
	}

	pathChanged := false
	err = uninstall.Apply(&plan, machine, func(item uninstall.Item) {
		switch {
		case item.Kind == uninstall.Binary && env.goos == "windows":
			outf(cmd, i18n.T("cli.uninstall.removing_once_this_airlock"), item.Describe())
		case item.Kind == uninstall.InstallDir:
		default:
			outf(cmd, i18n.T("cli.uninstall.removed"), item.Describe())
		}
		pathChanged = pathChanged || item.Kind == uninstall.ProfileEntry || item.Kind == uninstall.UserPathEntry
	})
	if err != nil {
		return err
	}
	outf(cmd, "%s", i18n.T("cli.uninstall.airlock_is_uninstalled"))
	if pathChanged {
		outf(cmd, "%s", i18n.T("cli.uninstall.open_new_terminal_to"))
	}
	outf(cmd, "\n")
	return nil
}

// machine gathers what the uninstall plan reads from this computer.
func (e *uninstallEnv) machine() (*uninstall.Env, error) {
	exe, err := e.executable()
	if err != nil {
		return nil, err
	}
	home, err := e.home()
	if err != nil {
		return nil, i18n.NewError("cli.uninstall.cannot_find_your_home", err)
	}
	dataDir, err := e.dataDir()
	if err != nil {
		return nil, err
	}
	env := &uninstall.Env{
		GOOS:       e.goos,
		Executable: exe,
		Profiles:   uninstall.ShellProfiles(home, e.getenv),
		UserPath:   e.userPath,
		DataDir:    dataDir,
		Keychain:   e.keychain(),
	}
	if e.gatewayEntries != nil {
		env.GatewayEntries, env.RemoveGatewayEntry = e.gatewayEntries()
	}
	if e.agentSkills != nil {
		env.AgentSkills = e.agentSkills()
	}
	return env, nil
}

// defaultGatewayEntries finds the gateway entry config.json names (mcp.serverPrefix, mcp.gateway.port)
// in the user-level file of every AI client.
func defaultGatewayEntries() (files []string, remove func(string) error) {
	cfg := config.Defaults()
	if path, err := appdir.ConfigFile(); err == nil {
		if loaded, loadErr := config.Load(path, organizationFor(context.Background(), path).Org); loadErr == nil {
			cfg = loaded
		}
	}
	entry := mcpserve.ClientEntry{Name: mcpserve.GatewayName(cfg.MCPServerPrefix), URL: gateway.URL(cfg.MCPGatewayPort)}
	files, _ = mcpserve.EntryFiles(aiclient.AllUser(), entry) //nolint:errcheck // a file airlock cannot read holds no entry it can remove.
	return files, func(string) error {
		// One pass removes the entry from every file; the next items find nothing left.
		_, err := mcpserve.UnregisterClients("", aiclient.AllUser(), entry)
		return err
	}
}

func defaultAgentSkills() []string {
	dirs, _ := agentskill.UserInstalled() //nolint:errcheck // without a home there is no skill to remove.
	return dirs
}

func printUninstallPlan(cmd *cobra.Command, plan *uninstall.Plan, running *runningDaemon) {
	outf(cmd, "%s", i18n.T("cli.uninstall.airlock_uninstall_removes"))
	for _, item := range plan.Remove {
		outf(cmd, "  - %s\n", item.Describe())
	}
	if running != nil {
		outf(cmd, i18n.T("cli.uninstall.also_stops_airlock_daemon"), i18n.Plural(running.Sessions, "cli.count.session"))
	}
	if len(plan.Kept) > 0 {
		outf(cmd, "%s", i18n.T("cli.uninstall.it_keeps"))
		for _, kept := range plan.Kept {
			outf(cmd, "  - %s\n", kept)
		}
	}
	outf(cmd, "\n")
}
