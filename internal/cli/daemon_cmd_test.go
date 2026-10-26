package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/ui"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "al")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestDaemonStatusWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", shortTempDir(t))
	t.Setenv("HOME", shortTempDir(t))
	cmd := newDaemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestNonInteractiveRunsIgnoreDaemonMode(t *testing.T) {
	if !shouldUseDaemon(&config.Config{Mode: config.ModeDaemon}, true) || shouldUseDaemon(&config.Config{Mode: config.ModeDaemon}, false) {
		t.Fatal("daemon mode applies to interactive runs only")
	}
	if shouldUseDaemon(&config.Config{Mode: config.ModeCLI}, true) {
		t.Fatal("cli mode never uses the daemon")
	}
}

func TestDaemonViewRefusesSettingFlags(t *testing.T) {
	for _, setting := range daemonSettingFlags {
		cmd := NewRootCmd()
		if err := cmd.ParseFlags([]string{"--" + setting.flag, "v"}); err != nil {
			t.Fatal(err)
		}
		err := runDaemonView(cmd, &jumphost.Options{}, nil, nil, &ui.DashboardOptions{})
		want := "--" + setting.flag + " does not apply in daemon mode: the daemon uses config.json (airlock config set " + setting.key + " <value>), or run with --mode cli"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	if err := checkDaemonFlags(NewRootCmd().Flags()); err != nil {
		t.Fatalf("no setting flag: %v", err)
	}
}
