package jumphost

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// resourceForward is the type of the tunnels to any host:port the jumphost reaches (airlock
// forward): no discovery, IDE data source nor MCP server. Its target name is host:port.
const resourceForward = "forward"

// ForwardTarget is the target name of a forward to port of host, checked: a host name or IP
// address without scheme, path nor port, and a port from 1 to 65535.
func ForwardTarget(host, port string) (string, error) {
	host = strings.TrimSpace(host)
	switch {
	case host == "":
		return "", i18n.NewError("jumphost.forward.host_required")
	case strings.Contains(host, "://"):
		return "", i18n.NewError("jumphost.forward.host_scheme", host)
	case strings.ContainsAny(host, "/?#@ \t"):
		return "", i18n.NewError("jumphost.forward.host_invalid", host)
	case strings.Contains(host, ":"):
		// Only an IPv6 address has colons; anything else carries a port.
		ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
		if ip == nil {
			return "", i18n.NewError("jumphost.forward.host_port", host)
		}
		host = ip.String()
	}
	number, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || number <= 0 || number > maxPortNumber {
		return "", i18n.NewError("jumphost.forward.port_invalid", port, maxPortNumber)
	}
	return net.JoinHostPort(host, strconv.Itoa(number)), nil
}

// parseForwardTarget splits and checks the target name of a forward.
func parseForwardTarget(name string) (host string, port int, err error) {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(name))
	if err != nil {
		return "", 0, i18n.NewError("jumphost.forward.target_invalid", name)
	}
	if _, err = ForwardTarget(host, rawPort); err != nil {
		return "", 0, err
	}
	port, err = strconv.Atoi(rawPort)
	return host, port, err
}

// prepareForward reserves the local port of a forward, the remote port when it is free.
func (r *Runner) prepareForward(cfg awsConfig, options *Options) (preparedTarget, error) {
	host, port, err := parseForwardTarget(options.Name)
	if err != nil {
		return preparedTarget{}, err
	}
	name := net.JoinHostPort(host, strconv.Itoa(port))
	localPort, err := r.reservePort(sessionLabel(resourceForward, name, cfg), options.LocalPortNumber, port)
	if err != nil {
		return preparedTarget{}, err
	}
	return preparedTarget{
		name:    name,
		display: fmt.Sprintf("Forward %s -> localhost:%d", name, localPort),
		info: []string{
			fmt.Sprintf("host:port  = localhost:%d", localPort),
			"remote     = " + name,
		},
		ssmParameters: map[string][]string{
			"host":            {host},
			"portNumber":      {strconv.Itoa(port)},
			"localPortNumber": {strconv.Itoa(localPort)},
		},
	}, nil
}

// forwardRestartArgs are the host and port arguments of airlock forward for the target name.
func forwardRestartArgs(name string) string {
	host, port, err := net.SplitHostPort(name)
	if err != nil {
		return name
	}
	return host + " " + port
}
