package uninstall

import (
	"path/filepath"
	"testing"
)

func TestRemoveProfileBlock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".local", "bin")
	other := filepath.Join(t.TempDir(), "bin")
	tests := []struct {
		name    string
		content string
		want    string
		found   bool
	}{
		{
			name:    "zsh block after other settings",
			content: "alias ll='ls -l'\n\n# Added by the airlock installer\nexport PATH=\"" + dir + ":$PATH\"\n",
			want:    "alias ll='ls -l'\n",
			found:   true,
		},
		{
			name:    "block at the start of the file",
			content: "# Added by the airlock installer\nexport PATH=\"" + dir + ":$PATH\"\nexport EDITOR=vim\n",
			want:    "export EDITOR=vim\n",
			found:   true,
		},
		{
			name:    "fish block",
			content: "\n# Added by the airlock installer\nfish_add_path -g \"" + dir + "\"\n",
			want:    "",
			found:   true,
		},
		{
			name:    "block of another folder",
			content: "\n# Added by the airlock installer\nexport PATH=\"" + other + ":$PATH\"\n",
			want:    "\n# Added by the airlock installer\nexport PATH=\"" + other + ":$PATH\"\n",
			found:   false,
		},
		{
			name:    "same line written by hand",
			content: "export PATH=\"" + dir + ":$PATH\"\n",
			want:    "export PATH=\"" + dir + ":$PATH\"\n",
			found:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := removeProfileBlock(tt.content, dir)
			if got != tt.want || found != tt.found {
				t.Fatalf("removeProfileBlock = %q, %v; want %q, %v", got, found, tt.want, tt.found)
			}
		})
	}
}

func TestShellProfiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	env := map[string]string{}
	getenv := func(name string) string { return env[name] }

	got := ShellProfiles(home, getenv)
	want := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".config", "fish", "conf.d", "airlock.fish"),
	}
	assertStrings(t, got, want)

	zdot := filepath.Join(t.TempDir(), "zsh")
	xdg := filepath.Join(t.TempDir(), "xdg")
	env["ZDOTDIR"], env["XDG_CONFIG_HOME"] = zdot, xdg
	got = ShellProfiles(home, getenv)
	want[0] = filepath.Join(zdot, ".zshrc")
	want[3] = filepath.Join(xdg, "fish", "conf.d", "airlock.fish")
	assertStrings(t, got, want)
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
