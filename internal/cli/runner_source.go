package cli

import (
	"os"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// sessionsRunner is the runner the sessions are prepared with.
type sessionsRunner = sessions.Runner

// runnerSource rebuilds the runner when config.json changed since the last opening.
type runnerSource[R sessionsRunner] struct {
	modTime time.Time
	current R
	build   func(config.Config) (R, error)
	path    string
	size    int64
	mu      sync.Mutex
	built   bool
}

func newRunnerSource[R sessionsRunner](path string, build func(config.Config) (R, error)) *runnerSource[R] {
	return &runnerSource[R]{path: path, build: build}
}

// Current returns the runner of the next opening. An invalid file fails the opening and keeps
// the previous runner for when it is fixed.
func (s *runnerSource[R]) Current() (R, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var modTime time.Time
	var size int64
	if info, err := os.Stat(s.path); err == nil {
		modTime, size = info.ModTime(), info.Size()
	}
	if s.built && modTime.Equal(s.modTime) && size == s.size {
		return s.current, nil
	}
	var none R
	cfg, err := config.Load(s.path)
	if err != nil {
		return none, err
	}
	runner, err := s.build(cfg)
	if err != nil {
		return none, err
	}
	s.current, s.modTime, s.size, s.built = runner, modTime, size, true
	return runner, nil
}

// Last returns the runner the last valid file built; ok is false before the first one.
func (s *runnerSource[R]) Last() (runner R, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, s.built
}

// Source is the sessions.RunnerSource of s.
func (s *runnerSource[R]) Source() sessions.RunnerSource {
	return func() (sessions.Runner, error) {
		runner, err := s.Current()
		if err != nil {
			// A nil runner of a pointer type would make a non-nil interface.
			return nil, err
		}
		return runner, nil
	}
}
