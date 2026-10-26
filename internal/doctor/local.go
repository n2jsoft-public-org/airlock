package doctor

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// toolTimeout bounds `<tool> --version`.
const toolTimeout = 5 * time.Second

// Version reports the running version: a newer release (latest, "" when none) or a failed check
// is a warning. updateCommand installs latest; "" when it is unknown.
func Version(current, latest, updateCommand string, checkErr error) Result {
	switch {
	case checkErr != nil:
		return warning("version", "", i18n.Format("doctor.version.unchecked", current, checkErr), i18n.T("doctor.version.unchecked_fix"))
	case latest != "" && updateCommand == "":
		return warning("version", "", i18n.Format("doctor.version.available", current, latest), i18n.T("doctor.version.available_fix_manual"))
	case latest != "":
		return warning("version", "", i18n.Format("doctor.version.available", current, latest), i18n.Format("doctor.version.available_fix", updateCommand))
	default:
		return ok("version", "", i18n.Format("doctor.version.ok", current))
	}
}

// OtherBinaries looks for airlock binaries other than self in the directories of pathEnv (the
// installer's warn_other_copies): one of them may run instead of self.
func OtherBinaries(self, pathEnv string) Result {
	name := "airlock"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	selfInfo, selfErr := os.Stat(self)
	var others []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathEnv) {
		candidate := filepath.Join(dir, name)
		if dir == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
			continue
		}
		if selfErr == nil && os.SameFile(info, selfInfo) {
			continue
		}
		others = append(others, candidate)
	}
	if len(others) == 0 {
		return ok("path", "", i18n.Format("doctor.path.ok", self))
	}
	return warning("path", "", i18n.Format("doctor.path.others", strings.Join(others, ", "), self), i18n.T("doctor.path.others_fix"))
}

// TunnelTools checks the AWS CLI and the Session Manager plugin RDS, EKS and ElastiCache tunnels
// run. A missing one fails unless needed is false: every tunnel module is disabled.
func TunnelTools(ctx context.Context, needed bool) []Result {
	tools := []struct{ name, missingKey string }{
		{"aws", "jumphost.runner.aws_cli_is_not"},
		{"session-manager-plugin", "jumphost.runner.session_manager_plugin_is_not"},
	}
	results := make([]Result, 0, len(tools))
	for _, tool := range tools {
		path, err := exec.LookPath(tool.name)
		switch {
		case err != nil && needed:
			results = append(results, failure("tunnel-tools", tool.name, i18n.Format("doctor.tool.missing", tool.name), i18n.T(tool.missingKey)))
		case err != nil:
			results = append(results, warning("tunnel-tools", tool.name, i18n.Format("doctor.tool.missing_unneeded", tool.name), i18n.T(tool.missingKey)))
		default:
			results = append(results, ok("tunnel-tools", tool.name, i18n.Format("doctor.tool.ok", tool.name, toolVersion(ctx, path))))
		}
	}
	return results
}

// toolVersion is the first line `path --version` prints; the path when it prints nothing.
func toolVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output() // #nosec G204 -- a tool found on the PATH by its fixed name.
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || line == "" {
		return path
	}
	return strings.TrimSpace(line)
}

// Config reports config.json at path: loadErr fails it, deprecated keys (as "old → new") warn.
func Config(path string, loadErr error, deprecated []string) Result {
	switch {
	case loadErr != nil:
		return failure("config", "", i18n.Format("doctor.config.invalid", path, loadErr), i18n.T("doctor.config.invalid_fix"))
	case len(deprecated) > 0:
		return warning("config", "", i18n.Format("doctor.config.deprecated", path, strings.Join(deprecated, ", ")), i18n.T("doctor.config.deprecated_fix"))
	default:
		return ok("config", "", i18n.Format("doctor.config.ok", path))
	}
}

// Organization reports the organization config of source ("" when none) from its notices.
func Organization(source string, notices []string) []Result {
	if source == "" {
		return []Result{ok("organization", "", i18n.T("doctor.organization.none"))}
	}
	if len(notices) == 0 {
		return []Result{ok("organization", source, i18n.Format("doctor.organization.ok", source))}
	}
	results := make([]Result, 0, len(notices))
	for _, notice := range notices {
		results = append(results, warning("organization", source, notice, i18n.T("doctor.organization.fix")))
	}
	return results
}

// Daemon reports the background daemon: running another version than current warns; a stopped
// daemon is fine, the next airlock starts it in daemon mode.
func Daemon(daemonMode, running bool, daemonVersion, current string) Result {
	switch {
	case running && daemonVersion != current:
		return warning("daemon", "", i18n.Format("doctor.daemon.mismatch", daemonVersion, current), i18n.T("doctor.daemon.mismatch_fix"))
	case running:
		return ok("daemon", "", i18n.Format("doctor.daemon.running", daemonVersion))
	case daemonMode:
		return ok("daemon", "", i18n.T("doctor.daemon.stopped"))
	default:
		return ok("daemon", "", i18n.T("doctor.daemon.cli_mode"))
	}
}

// GatewayPort reports the gateway's port: the gateway status of a running daemon ("off: why" when
// it failed), else whether port is free to listen on.
func GatewayPort(port int, running bool, status string) Result {
	if running {
		if reason, off := strings.CutPrefix(status, "off: "); off {
			return warning("gateway", "", i18n.Format("doctor.gateway.off", reason), i18n.T("doctor.gateway.port_fix"))
		}
		return ok("gateway", "", i18n.Format("doctor.gateway.on", status))
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return warning("gateway", "", i18n.Format("doctor.gateway.port_in_use", port, err), i18n.T("doctor.gateway.port_fix"))
	}
	if err = listener.Close(); err != nil {
		return warning("gateway", "", i18n.Format("doctor.gateway.port_in_use", port, err), i18n.T("doctor.gateway.port_fix"))
	}
	return ok("gateway", "", i18n.Format("doctor.gateway.port_free", port))
}

// GatewayClients reports the user-level AI client files holding the gateway entry.
func GatewayClients(files []string, err error) Result {
	switch {
	case err != nil:
		return warning("gateway-clients", "", i18n.Format("doctor.gateway.clients_unreadable", err), i18n.T("doctor.gateway.clients_fix"))
	case len(files) == 0:
		return warning("gateway-clients", "", i18n.T("doctor.gateway.clients_none"), i18n.T("doctor.gateway.clients_fix"))
	default:
		return ok("gateway-clients", "", i18n.Format("doctor.gateway.clients_ok", strings.Join(files, ", ")))
	}
}

// Keychain reports whether passwords can be remembered in the OS keychain.
func Keychain(available bool) Result {
	if available {
		return ok("keychain", "", i18n.T("doctor.keychain.ok"))
	}
	return warning("keychain", "", i18n.T("doctor.keychain.unavailable"), i18n.T("doctor.keychain.unavailable_fix"))
}

// KeychainNotResponding reports an OS keychain that did not answer in time: a locked Linux
// keyring may wait for an unlock forever.
func KeychainNotResponding() Result {
	return warning("keychain", "", i18n.T("doctor.keychain.timeout"), i18n.T("doctor.keychain.timeout_fix"))
}
