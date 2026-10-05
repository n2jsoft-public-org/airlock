package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/gateway"
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
	interactive    func(io.Reader) bool
	goos           string
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
		Short: "Remove airlock from this computer",
		Long: "Removes the airlock binary and the PATH entry its installer added, after listing them. " +
			"--purge also removes the settings, recent connections, MCP query logs and kubeconfigs, " +
			"and the database passwords saved in the OS keychain.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, env, purge, yes)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also remove settings, recent connections, logs and saved passwords")
	cmd.Flags().BoolVar(&yes, "yes", false, "Remove without asking")
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
		return fmt.Errorf("another airlock is running from %s: quit it, then run airlock uninstall again", machine.Executable)
	}
	plan, err := uninstall.NewPlan(machine, purge)
	if err != nil {
		return err
	}
	printUninstallPlan(cmd, &plan, running)
	if !yes {
		if !env.interactive(cmd.InOrStdin()) {
			return errors.New("airlock uninstall asks before removing anything: run it in a terminal, or pass --yes")
		}
		if !confirm(cmd, "Remove airlock? [y/N] ") {
			return errors.New("nothing removed")
		}
	}

	// Its sessions only end once the removal is confirmed.
	if running != nil {
		stopped, stopErr := env.stopDaemon(cmd.Context())
		if stopErr != nil {
			return fmt.Errorf("failed to stop the airlock daemon: %w", stopErr)
		}
		if stopped {
			outf(cmd, "Stopped the airlock daemon and its sessions.\n")
		}
	}

	pathChanged := false
	err = uninstall.Apply(&plan, machine, func(item uninstall.Item) {
		switch {
		case item.Kind == uninstall.Binary && env.goos == "windows":
			outf(cmd, "Removing %s once this airlock exits\n", item.Describe())
		case item.Kind == uninstall.InstallDir:
		default:
			outf(cmd, "Removed %s\n", item.Describe())
		}
		pathChanged = pathChanged || item.Kind == uninstall.ProfileEntry || item.Kind == uninstall.UserPathEntry
	})
	if err != nil {
		return err
	}
	outf(cmd, "airlock is uninstalled.")
	if pathChanged {
		outf(cmd, " Open a new terminal to refresh your PATH.")
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
		return nil, fmt.Errorf("cannot find your home folder: %w", err)
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
	return env, nil
}

// defaultGatewayEntries finds the gateway entry config.json names (mcpServerPrefix, mcpGatewayPort)
// in the user-level file of every AI client.
func defaultGatewayEntries() (files []string, remove func(string) error) {
	cfg := config.Defaults()
	if path, err := appdir.ConfigFile(); err == nil {
		if loaded, loadErr := config.Load(path); loadErr == nil {
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

func printUninstallPlan(cmd *cobra.Command, plan *uninstall.Plan, running *runningDaemon) {
	outf(cmd, "airlock uninstall removes:\n")
	for _, item := range plan.Remove {
		outf(cmd, "  - %s\n", item.Describe())
	}
	if running != nil {
		sessions := "sessions"
		if running.Sessions == 1 {
			sessions = "session"
		}
		outf(cmd, "\nAlso stops the airlock daemon and its %d %s.\n", running.Sessions, sessions)
	}
	if len(plan.Kept) > 0 {
		outf(cmd, "\nIt keeps:\n")
		for _, kept := range plan.Kept {
			outf(cmd, "  - %s\n", kept)
		}
	}
	outf(cmd, "\n")
}
