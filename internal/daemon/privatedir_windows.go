package daemon

import "os"

// checkPrivateDir does nothing: the daemon directory is in the user's profile, which Windows
// keeps private, and has no Unix owner to check.
func checkPrivateDir(string) error { return nil }

// checkSocketDir does nothing on Windows (see checkPrivateDir).
func checkSocketDir(string) error { return nil }

// restrictSocket is best effort: Windows has no Unix permissions.
func restrictSocket(path string) error {
	discard(os.Chmod(path, 0o600))
	return nil
}
