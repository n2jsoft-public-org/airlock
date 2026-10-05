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
type runnerSource struct {
	modTime time.Time
	build   func(config.Config) (sessionsRunner, error)
	current sessionsRunner
	path    string
	size    int64
	mu      sync.Mutex
	built   bool
}

func newRunnerSource(path string, build func(config.Config) (sessionsRunner, error)) *runnerSource {
	return &runnerSource{path: path, build: build}
}

// Current returns the runner of the next opening. An invalid file fails the opening and keeps
// the previous runner for when it is fixed.
func (s *runnerSource) Current() (sessionsRunner, error) {
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
	cfg, err := config.Load(s.path)
	if err != nil {
		return nil, err
	}
	runner, err := s.build(cfg)
	if err != nil {
		return nil, err
	}
	s.current, s.modTime, s.size, s.built = runner, modTime, size, true
	return runner, nil
}

// Source is the sessions.RunnerSource of s.
func (s *runnerSource) Source() sessions.RunnerSource { return s.Current }
