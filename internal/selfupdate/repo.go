package selfupdate

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// RepositoryFromModule returns the owner/repo of a github.com module path, such as
// github.com/acme/airlock or github.com/acme/airlock/v2.
func RepositoryFromModule(path string) (string, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("cannot tell where airlock is released from (module %s)", path)
	}
	return parts[1] + "/" + parts[2], nil
}

// Repository returns the owner/repo airlock is released from, read from the module path recorded
// in the running binary.
func Repository() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return RepositoryFromModule("")
	}
	return RepositoryFromModule(info.Main.Path)
}
