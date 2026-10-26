package selfupdate

import (
	"path/filepath"
	"testing"
)

func TestManagerOfEachPathFamily(t *testing.T) {
	goBins := []string{filepath.FromSlash("/home/dev/go/bin"), filepath.FromSlash("/opt/gobin")}
	for _, tt := range []struct {
		name, exe, goos, pkg string
		want                 string // Manager.Name, "" when unknown
		update               string
	}{
		{"cask", "/opt/homebrew/Caskroom/airlock/1.28.0/airlock", "darwin", "", "Homebrew", "brew upgrade airlock"},
		{"formula", "/usr/local/Cellar/airlock/1.28.0/bin/airlock", "darwin", "", "Homebrew", "brew upgrade airlock"},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/airlock", "linux", "", "Homebrew", "brew upgrade airlock"},
		{"scoop", `C:\Users\dev\scoop\apps\airlock\1.28.0\airlock.exe`, "windows", "", "Scoop", "scoop update airlock"},
		{"winget", `C:\Users\dev\AppData\Local\Microsoft\WinGet\Packages\N2JSoft.Airlock_Microsoft.Winget.Source_8wekyb3d8bbwe\airlock.exe`, "windows", "", "winget", "winget upgrade --id N2JSoft.Airlock"},
		{"gopath", filepath.FromSlash("/home/dev/go/bin/airlock"), "linux", "", "go install", "go install github.com/n2jsoft-public-org/airlock/cmd/airlock@latest"},
		{"gobin", filepath.FromSlash("/opt/gobin/airlock"), "linux", "", "go install", "go install github.com/n2jsoft-public-org/airlock/cmd/airlock@latest"},
		{"deb", "/usr/bin/airlock", "linux", "deb", "apt", "sudo apt install --only-upgrade airlock"},
		{"rpm", "/usr/local/bin/airlock", "linux", "rpm", "dnf", "sudo dnf upgrade airlock"},
		{"copied to /usr/local/bin", "/usr/local/bin/airlock", "linux", "", "", ""},
		{"/usr/local/bin on macOS", "/usr/local/bin/airlock", "darwin", "deb", "", ""},
		{"script install", "/home/dev/.local/bin/airlock", "linux", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := managerOf(tt.exe, tt.goos, goBins, func() string { return tt.pkg })
			if ok != (tt.want != "") || got.Name != tt.want || got.Update != tt.update {
				t.Fatalf("managerOf(%q) = %+v, %v, want %q updating with %q", tt.exe, got, ok, tt.want, tt.update)
			}
			if ok && got.Uninstall == "" {
				t.Fatalf("managerOf(%q) has no uninstall command", tt.exe)
			}
		})
	}
}
