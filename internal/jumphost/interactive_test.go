package jumphost

import (
	"context"
	"io"
	"testing"
)

func TestSSOLoginUsesTheInteractiveOfTheContext(t *testing.T) {
	var fromRunner, fromContext []string
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard,
		Interactive: func(_ context.Context, _, name string, args ...string) error {
			fromRunner = append([]string{name}, args...)
			return nil
		}})
	ctx := WithInteractive(context.Background(), func(_ context.Context, _, name string, args ...string) error {
		fromContext = append([]string{name}, args...)
		return nil
	})

	if err := runner.ssoLogin(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if fromRunner != nil || len(fromContext) != 5 || fromContext[0] != "aws" || fromContext[4] != "dev" {
		t.Fatalf("runner = %v, context = %v", fromRunner, fromContext)
	}
	if err := runner.ssoLogin(context.Background(), "dev"); err != nil || fromRunner == nil {
		t.Fatalf("without a context interactive, the runner's applies: %v, %v", fromRunner, err)
	}
}
