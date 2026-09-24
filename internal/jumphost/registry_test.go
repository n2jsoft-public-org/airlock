package jumphost

import (
	"net"
	"strings"
	"testing"
)

// newTestRegistry returns a registry where only the given ports are busy at the OS level.
func newTestRegistry(busy ...int) *SessionRegistry {
	registry := NewSessionRegistry()
	busySet := map[int]bool{}
	for _, port := range busy {
		busySet[port] = true
	}
	registry.portFree = func(port int) bool { return !busySet[port] }
	return registry
}

func TestReserveUsesPreferredPortWhenFree(t *testing.T) {
	registry := newTestRegistry()

	port, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if port != 5432 {
		t.Fatalf("port = %d, want 5432", port)
	}
}

func TestReserveSkipsPortsOfOtherSessions(t *testing.T) {
	registry := newTestRegistry()

	if _, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432); err != nil {
		t.Fatalf("reserve a: %v", err)
	}
	port, err := registry.reserve("RDS b (prod/eu-west-3)", 0, 5432)
	if err != nil {
		t.Fatalf("reserve b: %v", err)
	}
	if port != 5433 {
		t.Fatalf("port = %d, want 5433", port)
	}
}

func TestReserveSkipsPortsBusyInOS(t *testing.T) {
	registry := newTestRegistry(5432, 5433)

	port, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if port != 5434 {
		t.Fatalf("port = %d, want 5434", port)
	}
}

func TestReserveRejectsBusyExplicitPort(t *testing.T) {
	registry := newTestRegistry(6379)

	if _, err := registry.reserve("ELASTICACHE a (dev/eu-west-3)", 6379, 6379); err == nil || !strings.Contains(err.Error(), "another process") {
		t.Fatalf("err = %v, want busy by another process", err)
	}

	if _, err := registry.reserve("RDS a (dev/eu-west-3)", 5432, 5432); err != nil {
		t.Fatalf("reserve a: %v", err)
	}
	if _, err := registry.reserve("RDS b (prod/eu-west-3)", 5432, 5432); err == nil || !strings.Contains(err.Error(), "RDS a (dev/eu-west-3)") {
		t.Fatalf("err = %v, want used by session RDS a", err)
	}
}

func TestReserveRejectsSameTargetTwice(t *testing.T) {
	registry := newTestRegistry()

	if _, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432); err == nil || !strings.Contains(err.Error(), "already connected on localhost:5432") {
		t.Fatalf("err = %v, want already connected", err)
	}
}

func TestReleaseFreesPortAndTarget(t *testing.T) {
	registry := newTestRegistry()

	if _, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	registry.release("RDS a (dev/eu-west-3)")

	port, err := registry.reserve("RDS a (dev/eu-west-3)", 0, 5432)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	if port != 5432 {
		t.Fatalf("port = %d, want 5432", port)
	}
}

func TestSuggestAndCheckPort(t *testing.T) {
	registry := newTestRegistry(9444)

	port, err := registry.SuggestPort(9444)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if port != 9445 {
		t.Fatalf("port = %d, want 9445", port)
	}
	if err := registry.CheckPort(9444); err == nil {
		t.Fatal("CheckPort(9444) = nil, want busy error")
	}
	if err := registry.CheckPort(9445); err != nil {
		t.Fatalf("CheckPort(9445) = %v, want nil", err)
	}
	if err := registry.CheckPort(70000); err == nil {
		t.Fatal("CheckPort(70000) = nil, want out of range error")
	}
}

func TestSuggestPortFailsWhenRangeExhausted(t *testing.T) {
	registry := NewSessionRegistry()
	registry.portFree = func(int) bool { return false }

	if _, err := registry.SuggestPort(5432); err == nil {
		t.Fatal("SuggestPort = nil error, want exhausted range")
	}
}

func TestLocalPortFreeDetectsListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected address type %T", listener.Addr())
	}
	if localPortFree(addr.Port) {
		t.Fatalf("localPortFree(%d) = true while a listener is bound", addr.Port)
	}
}
