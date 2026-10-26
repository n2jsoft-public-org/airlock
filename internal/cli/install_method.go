package cli

import (
	"errors"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/selfupdate"
)

// scriptInstalled tells whether install.json says an install script installed the running airlock:
// only then do airlock update and airlock uninstall exist.
func scriptInstalled(executable, markerFile func() (string, error)) bool {
	exe, err := executable()
	if err != nil {
		return false
	}
	path, err := markerFile()
	return err == nil && selfupdate.ScriptInstalled(path, exe)
}

// notScriptInstalled is the error of airlock update (update true) or airlock uninstall run on an
// airlock no install script installed: the command of the tool managing it instead.
func notScriptInstalled(executable func() (string, error), update bool) error {
	exe, err := executable()
	manager, known := selfupdate.Manager{}, false
	if err == nil {
		manager, known = selfupdate.ManagerOf(exe)
	}
	switch {
	case known && update:
		return i18n.NewError("cli.install.update_with", manager.Name, manager.Update)
	case known:
		return i18n.NewError("cli.install.uninstall_with", manager.Name, manager.Uninstall)
	case update:
		return errors.New(i18n.T("cli.install.update_not_script"))
	default:
		return errors.New(i18n.T("cli.install.uninstall_not_script"))
	}
}

// updateCommand is the command updating this airlock: airlock update for a script install, the
// package manager's otherwise; "" when unknown.
func (e *updateEnv) updateCommand() string {
	if scriptInstalled(e.executable, e.markerFile) {
		return "airlock update"
	}
	if exe, err := e.executable(); err == nil {
		if manager, ok := selfupdate.ManagerOf(exe); ok {
			return manager.Update
		}
	}
	return ""
}
