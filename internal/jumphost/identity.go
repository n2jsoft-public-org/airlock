package jumphost

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// CheckIdentity checks the AWS sign-in of profile in region without ever signing in: expired or
// missing credentials fail with ErrSignInRequired; AWS out of reach, a profile that cannot be
// loaded or credentials AWS refuses (stsConfigErrors) fail with another error.
func (r *Runner) CheckIdentity(ctx context.Context, profile, region string) error {
	_, err := r.Account(ctx, profile, region)
	return err
}

// Account is CheckIdentity returning the AWS account id the credentials of profile belong to.
func (r *Runner) Account(ctx context.Context, profile, region string) (string, error) {
	cfg := identityConfig(profile, region)
	awsCfg, err := r.sdkConfig(ctx, cfg)
	if err != nil {
		return "", err
	}
	out, err := callerIdentity(ctx, &awsCfg)
	if err == nil {
		return aws.ToString(out.Account), nil
	}
	if checkErr := identityCheckError(ctx, cfg.Profile, err); checkErr != nil {
		return "", checkErr
	}
	return "", i18n.NewError("jumphost.identity.aws_credentials_of_profile", ErrSignInRequired, cfg.Profile)
}

// FindJumphost returns the id of a running instance carrying the jumphost tag in profile and
// region, without checking or renewing the sign-in.
func (r *Runner) FindJumphost(ctx context.Context, profile, region string) (string, error) {
	return r.getJumphostID(ctx, identityConfig(profile, region))
}

// stsConfigErrors are the error codes of AWS refusing credentials for a reason no sign-in fixes: a
// role that cannot be assumed (role_arn, trust policy, MFA device), unknown or wrong access keys,
// invalid AssumeRole parameters.
var stsConfigErrors = map[string]bool{"AccessDenied": true, "InvalidClientTokenId": true, "SignatureDoesNotMatch": true, "ValidationError": true}

// identityCheckError is the failure err of an identity check of profile when signing in would not
// fix it (ctx done, AWS out of reach, a configuration AWS refuses). Nil otherwise.
func identityCheckError(ctx context.Context, profile string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if awsapi.Unreachable(err) {
		return i18n.NewError("jumphost.identity.aws_unreachable", profile, err)
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok && stsConfigErrors[apiErr.ErrorCode()] {
		return i18n.NewError("jumphost.identity.aws_refused", profile, err)
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
