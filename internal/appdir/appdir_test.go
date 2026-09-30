package appdir

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDirHonorsXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "airlock"); dir != want {
		t.Fatalf("ConfigDir() = %q, want %q", dir, want)
	}
}

func TestConfigDirIgnoresRelativeXDGConfigHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("home based default is not used on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")

	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "airlock"); dir != want {
		t.Fatalf("ConfigDir() = %q, want %q", dir, want)
	}
}

func TestConfigDirDefaultsToDotConfigInHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("home based default is not used on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "airlock"); dir != want {
		t.Fatalf("ConfigDir() = %q, want %q", dir, want)
	}
}

func TestLogsDirIsUnderConfigDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	dir, err := LogsDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "airlock", "logs"); dir != want {
		t.Fatalf("LogsDir() = %q, want %q", dir, want)
	}
}

func TestKubeconfigDirIsUnderConfigDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	dir, err := KubeconfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "airlock", "kube"); dir != want {
		t.Fatalf("KubeconfigDir() = %q, want %q", dir, want)
	}
}

func TestConfigFileIsConfigJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "airlock", "config.json"); path != want {
		t.Fatalf("ConfigFile() = %q, want %q", path, want)
	}
}
