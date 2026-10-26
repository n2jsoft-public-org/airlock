package sessions

import (
	"context"
	"errors"

	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

// ErrUnknownSession reports an ID no session has (closed meanwhile).
var ErrUnknownSession = errors.New("unknown session")

// ErrClosed reports a request made once every session is being stopped.
var ErrClosed = errors.New("sessions are shutting down")

// Service is what a dashboard needs from the sessions, wherever they run.
type Service interface {
	// Subscribe returns the current sessions, then every change on the channel until ctx ends.
	Subscribe(ctx context.Context) (Snapshot, <-chan Event, error)
	// Snapshot returns the current sessions (after an EventResync).
	Snapshot(ctx context.Context) (Snapshot, error)
	// Open prepares and starts a session; canceling ctx during the preparation releases it.
	Open(ctx context.Context, req OpenRequest) (ID, error)
	Stop(ctx context.Context, id ID) error
	// TakeOver gives the session id, opened by an agent, to the user's stack: the gateway no
	// longer closes it, airlock down closes it with the stack.
	TakeOver(ctx context.Context, id ID, stack string) error
	// Remove closes an ended session.
	Remove(ctx context.Context, id ID) error
	// Restart prepares an ended session again: same target, profile, region, port and sign-in.
	Restart(ctx context.Context, id ID, interactive jumphost.InteractiveFunc) error
	// SignInAWS signs the AWS profile of a running session in again (airlock sso-login, run with
	// interactive), keeping its tunnel open.
	SignInAWS(ctx context.Context, id ID, interactive jumphost.InteractiveFunc) error
	// ChooseSignIn checks the sign-in candidate index of a session waiting for a choice.
	ChooseSignIn(ctx context.Context, id ID, index int) error
	// SkipSignIn leaves the sign-in of a session unchecked.
	SkipSignIn(ctx context.Context, id ID) error
	// RetrySignIn finds the credentials of a running session again.
	RetrySignIn(ctx context.Context, id ID) error
	// Credentials reads the credentials the password dialog shows.
	Credentials(ctx context.Context, id ID) ([]jumphost.DBCredentials, error)
	// ResolveApproval answers the pending approval id: approve allows the opening, whose terminal
	// commands (airlock sso-login) run with interactive. An approving answer returns once the opening
	// was prepared. A request already answered, withdrawn or expired gives ErrApprovalResolved.
	ResolveApproval(ctx context.Context, id ApprovalID, approve bool, interactive jumphost.InteractiveFunc) error
	// StopAll stops every session and waits for them to end.
	StopAll(ctx context.Context) error
}

// OpenRequest is a session to open, as the wizard built it.
type OpenRequest struct {
	// Interactive runs the commands needing the user's terminal during the preparation (airlock
	// sso-login); nil uses the runner's.
	Interactive jumphost.InteractiveFunc `json:"-"`
	// Warning, when set, ends the session's intro.
	Warning string           `json:"warning,omitempty"`
	Options jumphost.Options `json:"options"`
}
