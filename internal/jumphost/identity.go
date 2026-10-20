package jumphost

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// CheckIdentity checks the AWS sign-in of profile in region without ever signing in: expired or
// missing credentials fail with ErrSignInRequired; AWS out of reach or a profile that cannot be
// loaded fails with another error.
func (r *Runner) CheckIdentity(ctx context.Context, profile, region string) error {
	cfg := identityConfig(profile, region)
	awsCfg, err := r.sdkConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if _, err = callerIdentity(ctx, &awsCfg); err == nil {
		return nil
	}
	if checkErr := identityCheckError(ctx, cfg.Profile, err); checkErr != nil {
		return checkErr
	}
	return i18n.NewError("jumphost.identity.aws_credentials_of_profile", ErrSignInRequired, cfg.Profile)
}

// identityCheckError is the failure err of an identity check of profile when it says nothing
// about the credentials (ctx done, AWS out of reach): signing in would not fix it. Nil otherwise.
func identityCheckError(ctx context.Context, profile string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if awsapi.Unreachable(err) {
		return i18n.NewError("jumphost.identity.aws_unreachable", profile, err)
	}
	return nil
}

// callerIdentity asks STS whom the credentials of awsCfg belong to.
func callerIdentity(ctx context.Context, awsCfg *aws.Config) (*sts.GetCallerIdentityOutput, error) {
	return sts.NewFromConfig(*awsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
}

// callerArn returns the ARN of the caller identity of awsCfg.
func callerArn(ctx context.Context, awsCfg *aws.Config) (string, error) {
	out, err := callerIdentity(ctx, awsCfg)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Arn), nil
}

// SignIn signs profile in again when its AWS sign-in expired (airlock sso-login, through the
// interactive runner of ctx), leaving running tunnels alone.
func (r *Runner) SignIn(ctx context.Context, profile, region string) error {
	return r.ensureIdentity(ctx, identityConfig(profile, region), true)
}

func identityConfig(profile, region string) awsConfig {
	profile = resolveProfileName(profile)
	return awsConfig{Profile: profile, Region: region, Args: []string{"--profile", profile, "--region", region}}
}
