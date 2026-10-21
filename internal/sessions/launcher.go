package sessions

import "github.com/n2jsoft/airlock/internal/jumphost"

// The launcher lives in jumphost, which also starts the tunnels to cluster cache nodes with it.
type (
	// Process is a started aws process.
	Process = jumphost.Process
	// Launcher starts the aws process of a session with args, sending each output line to output.
	Launcher = jumphost.Launcher
)

// AWSLauncher starts aws with args in a process group of its own, so session-manager-plugin stops
// with it.
func AWSLauncher(args []string, output func(line string)) (Process, error) {
	return jumphost.AWSLauncher(args, output)
}
