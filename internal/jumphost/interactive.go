package jumphost

import (
	"context"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// InteractiveFunc runs a command needing the user's terminal (aws sso login), purpose saying why.
type InteractiveFunc func(ctx context.Context, purpose, name string, args ...string) error

type interactiveKey struct{}

// WithInteractive makes the openings prepared with ctx run their terminal commands with fn: the
// terminal of the dashboard that asked for them, which may not be the runner's.
func WithInteractive(ctx context.Context, fn InteractiveFunc) context.Context {
	return context.WithValue(ctx, interactiveKey{}, fn)
}

func interactiveFrom(ctx context.Context) InteractiveFunc {
	if fn, ok := ctx.Value(interactiveKey{}).(InteractiveFunc); ok {
		return fn
	}
	return nil
}

// RunInteractive runs name with args through the interactive of ctx; an error when it has none.
func RunInteractive(ctx context.Context, purpose, name string, args ...string) error {
	fn := interactiveFrom(ctx)
	if fn == nil {
		return i18n.NewError("jumphost.interactive.no_terminal", name)
	}
	return fn(ctx, purpose, name, args...)
}
