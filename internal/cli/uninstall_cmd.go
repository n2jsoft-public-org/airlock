package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/n2jsoft/airlock/internal/appdir"
	"github.com/n2jsoft/airlock/internal/keychain"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/uninstall"
	"github.com/spf13/cobra"
)

// uninstallEnv is what airlock uninstall takes from the machine; tests replace it.
type uninstallEnv struct {
	executable     func() (string, error)
	home           func() (string, error)
	getenv         func(string) string
	dataDir        func() (string, error)
	keychain       func() keychain.Store
	userPath       func() (string, error)
	otherInstances func(exe string) (int, error)
	interactive    func(io.Reader) bool
	goos           string
}

func defaultUninstallEnv() *uninstallEnv {
	return &uninstallEnv{
		executable:     selfupdate.Executable,
		home:           os.UserHomeDir,
		getenv:         os.Getenv,
		dataDir:        appdir.ConfigDir,
		keychain:       availableKeychain,
		userPath:       uninstall.UserPath,
		otherInstances: uninstall.OtherInstances,
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
	others, err := env.otherInstances(machine.Executable)
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
	printUninstallPlan(cmd, &plan)
	if !yes {
		if !env.interactive(cmd.InOrStdin()) {
			return errors.New("airlock uninstall asks before removing anything: run it in a terminal, or pass --yes")
		}
		if !confirm(cmd, "Remove airlock? [y/N] ") {
			return errors.New("nothing removed")
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
	return &uninstall.Env{
		GOOS:       e.goos,
		Executable: exe,
		Profiles:   uninstall.ShellProfiles(home, e.getenv),
		UserPath:   e.userPath,
		DataDir:    dataDir,
		Keychain:   e.keychain(),
	}, nil
}

func printUninstallPlan(cmd *cobra.Command, plan *uninstall.Plan) {
	outf(cmd, "airlock uninstall removes:\n")
	for _, item := range plan.Remove {
		outf(cmd, "  - %s\n", item.Describe())
	}
	if len(plan.Kept) > 0 {
		outf(cmd, "\nIt keeps:\n")
		for _, kept := range plan.Kept {
			outf(cmd, "  - %s\n", kept)
		}
	}
	outf(cmd, "\n")
}
