package jumphost

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	maxPortNumber = 65535
	// portSearchLimit bounds how far above the preferred port a free one is searched.
	portSearchLimit = 100
	portProbeDelay  = 200 * time.Millisecond
)

// SessionRegistry tracks the local ports and targets used by the sessions of this process,
// so parallel sessions never bind the same port or open the same target twice.
// It is safe for concurrent use.
type SessionRegistry struct {
	// portFree reports whether the OS lets us use a port; overridden in tests.
	portFree func(port int) bool
	ports    map[int]string
	targets  map[string]int
	mu       sync.Mutex
}

// NewSessionRegistry returns an empty registry probing the local OS for busy ports.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{
		portFree: localPortFree,
		ports:    map[int]string{},
		targets:  map[string]int{},
	}
}

// PortAvailable reports whether port is neither reserved by a session nor used by another process.
func (s *SessionRegistry) PortAvailable(port int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.portAvailableLocked(port)
}

// CheckPort explains why port cannot be used, or returns nil when it is free.
func (s *SessionRegistry) CheckPort(port int) error {
	if port <= 0 || port > maxPortNumber {
		return fmt.Errorf("port %d is out of range (1-%d)", port, maxPortNumber)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if owner, ok := s.ports[port]; ok {
		return fmt.Errorf("port %d is already used by session %s", port, owner)
	}
	if !s.portFree(port) {
		return fmt.Errorf("port %d is already in use by another process", port)
	}
	return nil
}

// SuggestPort returns preferred when it is available, otherwise the next available port above it.
func (s *SessionRegistry) SuggestPort(preferred int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suggestPortLocked(preferred)
}

// reserve records that the session identified by target uses port.
// An explicit port must be free; otherwise the first free port from preferred is taken.
func (s *SessionRegistry) reserve(target string, requested, preferred int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if port, ok := s.targets[target]; ok {
		return 0, fmt.Errorf("%s is already connected on localhost:%d", target, port)
	}

	port := requested
	if port > 0 {
		if owner, ok := s.ports[port]; ok {
			return 0, fmt.Errorf("port %d is already used by session %s", port, owner)
		}
		if !s.portFree(port) {
			return 0, fmt.Errorf("port %d is already in use by another process", port)
		}
	} else {
		var err error
		if port, err = s.suggestPortLocked(preferred); err != nil {
			return 0, err
		}
	}

	s.ports[port] = target
	s.targets[target] = port
	return port, nil
}

// release frees the port and target held by target.
func (s *SessionRegistry) release(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if port, ok := s.targets[target]; ok {
		delete(s.ports, port)
		delete(s.targets, target)
	}
}

func (s *SessionRegistry) suggestPortLocked(preferred int) (int, error) {
	if preferred <= 0 || preferred > maxPortNumber {
		return 0, fmt.Errorf("port %d is out of range (1-%d)", preferred, maxPortNumber)
	}

	last := min(preferred+portSearchLimit, maxPortNumber)
	for port := preferred; port <= last; port++ {
		if s.portAvailableLocked(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free local port found between %d and %d", preferred, last)
}

func (s *SessionRegistry) portAvailableLocked(port int) bool {
	if _, ok := s.ports[port]; ok {
		return false
	}
	return s.portFree(port)
}

// localPortFree reports whether nothing listens on port and it can be bound on the loopback interface,
// which is where session-manager-plugin listens.
func localPortFree(port int) bool {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	// A successful dial catches listeners bound to the wildcard address, which a bind test can miss.
	if conn, err := net.DialTimeout("tcp", address, portProbeDelay); err == nil {
		_ = conn.Close() //nolint:errcheck // the port is busy whether or not the probe closes cleanly.
		return false
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	return listener.Close() == nil
}
