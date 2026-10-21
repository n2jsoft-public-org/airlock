package sessions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

const (
	// standaloneTailLines is how much of the tunnel output explains a tunnel that ended early.
	standaloneTailLines = 20
	// standaloneStopTimeout bounds the wait for a terminated tunnel to exit.
	standaloneStopTimeout = 10 * time.Second
)

// Standalone is a session opened outside any manager, for a command run with it (airlock exec):
// its tunnel listens and its sign-in works. Close it once done.
type Standalone struct {
	Session *jumphost.Session
	process Process
	exited  chan struct{}
	// Client is how the command reaches the database or cache, signed in.
	Client    jumphost.Client
	closeOnce sync.Once
}

// OpenStandalone prepares options with runner, starts the tunnel with launch, and waits at most
// timeout for it to listen and for the database sign-in to work. Nobody can choose between several
// credentials: they fail. Any failure stops the tunnel and releases the session.
func OpenStandalone(ctx context.Context, runner Runner, launch Launcher, options *jumphost.Options, timeout time.Duration) (_ *Standalone, err error) {
	session, err := runner.Prepare(jumphost.WithLauncher(ctx, launch), *options)
	if err != nil {
		return nil, err
	}
	s := &Standalone{Session: session}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		// The deadline, not the user, ended the wait: say what did not get ready.
		if err != nil && ctx.Err() == nil && errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			err = i18n.NewError("sessions.standalone.not_ready", session.Target, timeout)
		}
	}()
	if !noTunnel(session.ResourceType) {
		if tunnelErr := s.startTunnel(waitCtx, launch); tunnelErr != nil {
			return nil, tunnelErr
		}
	}
	candidate, err := standaloneSignIn(waitCtx, session)
	if err != nil {
		return nil, err
	}
	if s.Client, err = session.Client(waitCtx, candidate); err != nil {
		return nil, err
	}
	return s, nil
}

// startTunnel launches the aws process of the session and waits for it to listen.
func (s *Standalone) startTunnel(ctx context.Context, launch Launcher) error {
	listening := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var tail []string
	args := append(append([]string{}, s.Session.CommandArgs...), s.Session.AwsArgs...)
	process, err := launch(args, func(line string) {
		mu.Lock()
		if tail = append(tail, line); len(tail) > standaloneTailLines {
			tail = tail[1:]
		}
		mu.Unlock()
		if strings.Contains(line, jumphost.TunnelListening) {
			once.Do(func() { close(listening) })
		}
	})
	if err != nil {
		return err
	}
	var waitErr error
	s.process, s.exited = process, make(chan struct{})
	go func() {
		waitErr = process.Wait()
		close(s.exited)
	}()
	select {
	case <-listening:
		return nil
	case <-s.exited:
		mu.Lock()
		defer mu.Unlock()
		return i18n.NewError("sessions.standalone.tunnel_ended", waitErr, strings.Join(tail, "\n"))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// standaloneSignIn checks the only sign-in candidate of session; nil without sign-in check.
func standaloneSignIn(ctx context.Context, session *jumphost.Session) (*jumphost.SignInCandidate, error) {
	if session.SignIn == nil {
		return nil, nil
	}
	candidates, err := session.SignIn.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	switch len(candidates) {
	case 0:
		return nil, i18n.NewError("sessions.signin.no_database_credentials_found")
	case 1:
	default:
		labels := make([]string, len(candidates))
		for idx := range candidates {
			labels[idx] = candidates[idx].Label
		}
		return nil, i18n.NewError("sessions.standalone.several_credentials", session.Target, strings.Join(labels, ", "))
	}
	c := candidates[0]
	if err = session.SignIn.Check(ctx, c); err != nil {
		return nil, i18n.NewError("sessions.standalone.sign_in_failed", c.User, c.Label, err)
	}
	return &c, nil
}

// Close stops the tunnel and releases the session. Safe to call repeatedly.
func (s *Standalone) Close() {
	s.closeOnce.Do(func() {
		if s.process != nil {
			s.process.Terminate()
			select {
			case <-s.exited:
			case <-time.After(standaloneStopTimeout):
			}
		}
		s.Session.Release()
	})
}
