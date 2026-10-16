package cli

import (
	"context"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/orgconfig"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// sessionsRunner is the runner the sessions are prepared with.
type sessionsRunner = sessions.Runner

// runnerSource rebuilds the runner when config.json changed since the last opening; org is the
// organization layer under it. Following the organization, it also rebuilds when the organization
// cache, the managed file or the file source changed, and re-resolves the organization then.
type runnerSource[R sessionsRunner] struct {
	current R
	build   func(config.Config, *config.Organization) (R, error)
	org     *config.Organization
	// orgEnv re-resolves org, from the cache only; nil keeps org.
	orgEnv *orgEnv
	path   string
	// orgFile is the path of the file source of org; empty for any other source.
	orgFile string
	// stamps are those of the files of the last build (stampFiles).
	stamps []fileStamp
	mu     sync.Mutex
	built  bool
}

// fileStamp is the modification time and size of a file; zero when it cannot be read.
type fileStamp struct {
	modTime time.Time
	size    int64
}

func newRunnerSource[R sessionsRunner](path string, org *config.Organization, build func(config.Config, *config.Organization) (R, error)) *runnerSource[R] {
	return &runnerSource[R]{path: path, org: org, build: build}
}

// followOrganization makes s re-resolve the organization of env at every build, never waiting
// on the network: the refresh of the cache is the daemon loop's.
func (s *runnerSource[R]) followOrganization(env orgEnv) *runnerSource[R] { //nolint:gocritic // orgEnv is copied to be made cache-only.
	env.cacheOnly = true
	s.orgEnv = &env
	return s
}

// Current returns the runner of the next opening. An invalid file fails the opening and keeps
// the previous runner for when it is fixed.
func (s *runnerSource[R]) Current() (R, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamps := s.stampFiles(s.orgFile)
	if s.built && slices.EqualFunc(stamps, s.stamps, fileStamp.equal) {
		return s.current, nil
	}
	var none R
	org, orgFile := s.org, s.orgFile
	if s.orgEnv != nil {
		state := resolveOrganization(context.Background(), *s.orgEnv, s.path)
		org, orgFile = state.Org, ""
		if state.src.Raw != "" && state.src.Kind == orgconfig.KindFile {
			orgFile = state.src.Path
		}
		if orgFile != s.orgFile {
			// Its own stamp, so that the next opening does not rebuild for it.
			stamps = s.stampFiles(orgFile)
		}
	}
	cfg, err := config.Load(s.path, org)
	if err != nil {
		return none, err
	}
	runner, err := s.build(cfg, org)
	if err != nil {
		return none, err
	}
	s.current, s.org, s.orgFile, s.stamps, s.built = runner, org, orgFile, stamps, true
	return runner, nil
}

// stampFiles stamps config.json and, following the organization, the cache, the managed file and
// orgFile.
func (s *runnerSource[R]) stampFiles(orgFile string) []fileStamp {
	stamps := []fileStamp{stampOf(s.path)}
	if s.orgEnv != nil {
		stamps = append(stamps, stampOf(s.orgEnv.cachePath), stampOf(s.orgEnv.managedPath), stampOf(orgFile))
	}
	return stamps
}

func (f fileStamp) equal(other fileStamp) bool {
	return f.modTime.Equal(other.modTime) && f.size == other.size
}

func stampOf(path string) fileStamp {
	if path == "" {
		return fileStamp{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{info.ModTime(), info.Size()}
}

// organization is the organization layer of the last runner.
func (s *runnerSource[R]) organization() *config.Organization {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.org
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
