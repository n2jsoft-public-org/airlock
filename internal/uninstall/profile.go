package uninstall

import (
	"path/filepath"
	"strings"
)

// profileMarker heads the PATH block install.sh appends to a shell profile.
const profileMarker = "# Added by the airlock installer"

// ShellProfiles returns the shell profiles install.sh may have written its PATH block to.
func ShellProfiles(home string, getenv func(string) string) []string {
	zdotdir := getenv("ZDOTDIR")
	if zdotdir == "" {
		zdotdir = home
	}
	configHome := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(configHome) {
		configHome = filepath.Join(home, ".config")
	}
	return []string{
		filepath.Join(zdotdir, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(configHome, "fish", "conf.d", "airlock.fish"),
	}
}

// removeProfileBlock returns content without the PATH blocks install.sh wrote for dir, and whether
// it found one. The blank line the installer put before a block goes with it.
func removeProfileBlock(content, dir string) (string, bool) {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	found := false
	for i := 0; i < len(lines); i++ {
		if lines[i] == profileMarker && i+1 < len(lines) && sameDir(profileLineDir(lines[i+1]), dir) {
			if n := len(kept); n > 0 && kept[n-1] == "" {
				kept = kept[:n-1]
			}
			found = true
			i++
			continue
		}
		kept = append(kept, lines[i])
	}
	if !found {
		return content, false
	}
	result := strings.Join(kept, "\n")
	if strings.TrimSpace(result) == "" {
		return "", true
	}
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result, true
}

// profileLineDir returns the folder of a PATH line install.sh writes, or "" for any other line.
func profileLineDir(line string) string {
	for _, form := range [][2]string{
		{`export PATH="`, `:$PATH"`},
		{`fish_add_path -g "`, `"`},
	} {
		if strings.HasPrefix(line, form[0]) && strings.HasSuffix(line, form[1]) && len(line) > len(form[0])+len(form[1]) {
			return line[len(form[0]) : len(line)-len(form[1])]
		}
	}
	return ""
}

// sameDir tells whether a and b name the same folder, following symlinks such as /tmp on macOS.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && resolvedA == resolvedB
}
