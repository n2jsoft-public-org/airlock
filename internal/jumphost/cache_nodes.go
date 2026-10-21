package jumphost

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// cacheNodeForward opens, for the MCP server of a cluster-mode cache, one more SSM port forward
// through jumphostID per node the cluster names: nodes announce private addresses only the
// jumphost reaches. endpoint (host:port) is already forwarded to localPort, the session's tunnel:
// a serverless cache announces its endpoint for every slot. The tunnels start with launch.
func (r *Runner) cacheNodeForward(launch Launcher, jumphostID string, awsArgs []string, label, endpoint string, localPort int) cachemcp.Forward {
	return func(ctx context.Context, addr string) (string, func(), <-chan struct{}, error) {
		if addr == endpoint {
			return net.JoinHostPort("localhost", strconv.Itoa(localPort)), func() {}, nil, nil
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return "", nil, nil, err
		}
		nodeLabel := label + " node " + addr
		nodePort, err := r.registry.reserve(nodeLabel, 0, 0)
		if err != nil {
			return "", nil, nil, err
		}
		release := func() { r.registry.release(nodeLabel) }
		args, err := ssmForwardArgs(jumphostID, map[string][]string{"host": {host}, "portNumber": {port}, "localPortNumber": {strconv.Itoa(nodePort)}})
		if err != nil {
			release()
			return "", nil, nil, err
		}
		args = append(args, awsArgs...)
		stop, done, err := startNodeTunnel(ctx, launch, args)
		if err != nil {
			release()
			return "", nil, nil, i18n.NewError("jumphost.cache.node_tunnel_failed", addr, err)
		}
		return net.JoinHostPort("localhost", strconv.Itoa(nodePort)), func() { stop(); release() }, done, nil
	}
}

// startNodeTunnel starts aws with args through launch and waits until the port forward listens;
// stop ends it, done closes once it ended.
func startNodeTunnel(ctx context.Context, launch Launcher, args []string) (stop func(), done <-chan struct{}, err error) {
	ready := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	last := ""
	process, err := launch(args, func(line string) {
		if line = strings.TrimSpace(line); strings.Contains(line, TunnelListening) {
			once.Do(func() { close(ready) })
		} else if line != "" {
			mu.Lock()
			last = line
			mu.Unlock()
		}
	})
	if err != nil {
		return nil, nil, err
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = process.Wait()
		close(exited)
	}()
	select {
	case <-ready:
		return process.Terminate, exited, nil
	case <-exited:
		mu.Lock()
		defer mu.Unlock()
		return nil, nil, i18n.NewError("jumphost.cache.node_tunnel_ended", waitErr, last)
	case <-ctx.Done():
		process.Terminate()
		return nil, nil, ctx.Err()
	}
}
