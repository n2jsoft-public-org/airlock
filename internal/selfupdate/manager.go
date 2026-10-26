package selfupdate

import (
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Manager is the tool that installed an airlock no install script installed, with the commands
// that update and remove it in place of airlock update and airlock uninstall.
type Manager struct {
	Name      string
	Update    string
	Uninstall string
}

// ManagerOf guesses from its path the tool that installed the airlock binary at exe; false when
// the path tells nothing.
func ManagerOf(exe string) (Manager, bool) {
	return managerOf(resolvedPath(exe), runtime.GOOS, goBinDirs(), systemPackage)
}

func managerOf(exe, goos string, goBins []string, systemPackage func() string) (Manager, bool) {
	path := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	dir := filepath.Dir(exe)
	switch {
	case strings.Contains(path, "/caskroom/") || strings.Contains(path, "/cellar/") || strings.Contains(path, "/home/linuxbrew/"):
		return Manager{Name: "Homebrew", Update: "brew upgrade airlock", Uninstall: "brew uninstall airlock"}, true
	case strings.Contains(path, "/scoop/apps/"):
		return Manager{Name: "Scoop", Update: "scoop update airlock", Uninstall: "scoop uninstall airlock"}, true
	case strings.Contains(path, "/winget/packages/"):
		return Manager{Name: "winget", Update: "winget upgrade --id N2JSoft.Airlock", Uninstall: "winget uninstall --id N2JSoft.Airlock"}, true
	case slices.ContainsFunc(goBins, func(bin string) bool { return samePath(bin, dir, goos) }):
		remove := "rm "
		if goos == "windows" {
			remove = "del "
		}
		return Manager{Name: "go install", Update: "go install github.com/n2jsoft-public-org/airlock/cmd/airlock@latest",
			Uninstall: remove + `"` + exe + `"`}, true
	case goos == "linux" && (dir == "/usr/bin" || dir == "/usr/local/bin"):
		switch systemPackage() {
		case "deb":
			return Manager{Name: "apt", Update: "sudo apt install --only-upgrade airlock", Uninstall: "sudo apt remove airlock"}, true
		case "rpm":
			return Manager{Name: "dnf", Update: "sudo dnf upgrade airlock", Uninstall: "sudo dnf remove airlock"}, true
		}
	}
	return Manager{}, false
}

// goBinDirs are the folders go install writes to: GOBIN and the bin folder of every GOPATH entry.
// ponytail: a GOBIN set only with go env -w is not seen; read go env if it matters.
func goBinDirs() []string {
	var dirs []string
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	for _, gopath := range filepath.SplitList(build.Default.GOPATH) {
		dirs = append(dirs, filepath.Join(gopath, "bin"))
	}
	return dirs
}

// systemPackage is "deb" or "rpm" when a package of the system installed airlock, "" otherwise.
func systemPackage() string {
	if _, err := os.Stat("/var/lib/dpkg/info/airlock.list"); err == nil {
		return "deb"
	}
	if exec.Command("rpm", "-q", "--quiet", "airlock").Run() == nil {
		return "rpm"
	}
	return ""
}
