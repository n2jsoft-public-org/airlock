package platform

import (
	"os/exec"
	"runtime"
)

// OpenURL opens url in the user's browser, without waiting for it.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) // #nosec G204 -- the URL is a single argument, no shell.
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) // #nosec G204 -- same.
	default:
		cmd = exec.Command("xdg-open", url) // #nosec G204 -- same.
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() //nolint:errcheck // reaping the child.
	return nil
}
