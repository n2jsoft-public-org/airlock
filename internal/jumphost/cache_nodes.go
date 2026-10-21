package jumphost

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/platform"
)

// tunnelListening is what session-manager-plugin prints once a port forward accepts connections.
const tunnelListening = "Waiting for connections"

// cacheNodeForward opens, for the MCP server of a cluster-mode cache, one more SSM port forward
// through jumphostID per node the cluster names: nodes announce private addresses only the
// jumphost reaches. endpoint (host:port) is already forwarded to localPort, the session's tunnel:
// a serverless cache announces its endpoint for every slot.
func (r *Runner) cacheNodeForward(jumphostID string, awsArgs []string, label, endpoint string, localPort int) cachemcp.Forward {
	return func(ctx context.Context, addr string) (string, func(), error) {
		if addr == endpoint {
			return net.JoinHostPort("localhost", strconv.Itoa(localPort)), func() {}, nil
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return "", nil, err
		}
		nodeLabel := label + " node " + addr
		nodePort, err := r.registry.reserve(nodeLabel, 0, 0)
		if err != nil {
			return "", nil, err
		}
		release := func() { r.registry.release(nodeLabel) }
		parameters, err := json.Marshal(map[string][]string{"host": {host}, "portNumber": {port}, "localPortNumber": {strconv.Itoa(nodePort)}})
		if err != nil {
			release()
			return "", nil, err
		}
		args := append([]string{
			"ssm", "start-session", "--target", jumphostID,
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost", "--parameters", string(parameters),
		}, awsArgs...)
		stop, err := startNodeTunnel(ctx, args)
		if err != nil {
			release()
			return "", nil, i18n.NewError("jumphost.cache.node_tunnel_failed", addr, err)
		}
		return net.JoinHostPort("localhost", strconv.Itoa(nodePort)), func() { stop(); release() }, nil
	}
}

// startNodeTunnel starts aws with args in a process group of its own and waits until the port
// forward listens; stop ends it.
func startNodeTunnel(ctx context.Context, args []string) (stop func(), err error) {
	// exec.Command, not CommandContext: the tunnel outlives ctx, and stop ends the process group.
	cmd := exec.Command("aws", args...) // #nosec G204 -- the aws CLI with airlock's own arguments, no shell.
	platform.StartInOwnGroup(cmd)
	output, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The output is read until the end, so the process never blocks writing it.
	ready, ended, exited := make(chan struct{}, 1), make(chan string, 1), make(chan error, 1)
	go func() {
		last := ""
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); strings.Contains(line, tunnelListening) {
				select {
				case ready <- struct{}{}:
				default:
				}
			} else if line != "" {
				last = line
			}
		}
		_, _ = io.Copy(io.Discard, output) //nolint:errcheck // only drains what a too long line left
		ended <- last
	}()
	go func() {
		err := cmd.Wait()
		_ = writer.Close() //nolint:errcheck // a pipe writer closes without error
		exited <- err
	}()
	select {
	case <-ready:
		return func() { platform.TerminateGroup(cmd) }, nil
	case last := <-ended:
		return nil, i18n.NewError("jumphost.cache.node_tunnel_ended", <-exited, last)
	case <-ctx.Done():
		platform.TerminateGroup(cmd)
		return nil, ctx.Err()
	}
}
