package jumphost

import (
	"context"
	"fmt"
	"strings"
)

// unreachableMarkers are what the aws CLI prints when AWS could not be reached: such a failure
// says nothing about the credentials.
var unreachableMarkers = []string{
	"Could not connect to the endpoint URL",
	"Connect timeout on endpoint URL",
	"Read timeout on endpoint URL",
	"Connection was closed before we received a valid response",
}

// CheckIdentity checks the AWS sign-in of profile in region without ever signing in: expired or
// missing credentials fail with ErrSignInRequired; AWS out of reach fails with another error.
func (r *Runner) CheckIdentity(ctx context.Context, profile, region string) error {
	cfg := identityConfig(profile, region)
	_, err := r.runAWSCapture(ctx, cfg.Args, "sts", "get-caller-identity", "--query", "Arn", "--output", "text")
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, marker := range unreachableMarkers {
		if strings.Contains(err.Error(), marker) {
			return err
		}
	}
	return fmt.Errorf("%w: the AWS credentials of profile %s are missing or expired", ErrSignInRequired, cfg.Profile)
}

// SignIn signs profile in again when its AWS sign-in expired (aws sso login, through the
// interactive runner of ctx), leaving running tunnels alone.
func (r *Runner) SignIn(ctx context.Context, profile, region string) error {
	return r.ensureIdentity(ctx, identityConfig(profile, region), true)
}

func identityConfig(profile, region string) awsConfig {
	profile = resolveProfileName(profile)
	return awsConfig{Profile: profile, Region: region, Args: []string{"--profile", profile, "--region", region}}
}
