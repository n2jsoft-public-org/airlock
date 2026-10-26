package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/n2jsoft-public-org/airlock/internal/platform"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// procStore records the PIDs of the running aws processes, so the next daemon stops those a
// crashed one left behind.
type procStore struct {
	path string
	mu   sync.Mutex
}

func newProcStore(path string) *procStore { return &procStore{path: path} }

func (s *procStore) read() ([]int, error) {
	data, err := os.ReadFile(s.path) // #nosec G304 -- airlock's own cache file.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pids []int
	if err := json.Unmarshal(data, &pids); err != nil {
		return nil, nil // an unreadable record is dropped: it only helps cleaning up.
	}
	return pids, nil
}

func (s *procStore) write(pids []int) error {
	data, err := json.Marshal(pids)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *procStore) add(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pids, err := s.read()
	if err != nil {
		return err
	}
	return s.write(append(pids, pid))
}

func (s *procStore) remove(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pids, err := s.read()
	if err != nil {
		return err
	}
	return s.write(slices.DeleteFunc(pids, func(p int) bool { return p == pid }))
}

// killLeftovers stops the process groups of the recorded aws processes (only their PIDs are
// recorded): those still running aws, and those whose aws ended but not session-manager-plugin. A
// PID reused by another program is left alone. It empties the record and returns how many it stopped.
func (s *procStore) killLeftovers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	pids, err := s.read()
	discard(err) // an unreadable record only means nothing to clean up.
	killed := 0
	for _, pid := range pids {
		name, err := platform.ProcessName(pid)
		if err != nil && !platform.GroupAlive(pid) {
			continue
		}
		if name = strings.ToLower(name); err == nil && name != "aws" && name != "aws.exe" { // not aws-vault and the like.
			continue
		}
		if platform.TerminatePID(pid) == nil {
			killed++
		}
	}
	discard(s.write(nil))
	return killed
}

// tracking wraps launch so every process it starts is recorded while it runs.
func (s *procStore) tracking(launch sessions.Launcher) sessions.Launcher {
	return func(args []string, output func(string)) (sessions.Process, error) {
		p, err := launch(args, output)
		if err != nil {
			return nil, err
		}
		discard(s.add(p.PID()))
		return &trackedProcess{Process: p, store: s}, nil
	}
}

type trackedProcess struct {
	sessions.Process
	store *procStore
}

func (p *trackedProcess) Wait() error {
	err := p.Process.Wait()
	discard(p.store.remove(p.PID()))
	return err
}
