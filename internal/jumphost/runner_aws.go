package jumphost

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awssso"
	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// accountFor returns the AWS account ID of profile: the one seen by the identity check, which runs
// before targets are listed, else asked to STS. It is empty when unknown.
func (r *Runner) accountFor(ctx context.Context, profile string) string {
	r.accountsMu.Lock()
	account, ok := r.accounts[profile]
	r.accountsMu.Unlock()
	if ok {
		return account
	}
	awsCfg, err := r.sdkConfig(ctx, awsConfig{Profile: profile})
	if err != nil {
		return ""
	}
	identity, err := callerIdentity(ctx, &awsCfg)
	if err != nil {
		return ""
	}
	account = strings.TrimSpace(aws.ToString(identity.Account))
	r.rememberAccount(profile, account)
	return account
}

// rememberAccount caches the account of profile, from an account ID or a caller identity ARN
// (arn:aws:sts::<account>:assumed-role/...).
func (r *Runner) rememberAccount(profile, identity string) {
	account := strings.TrimSpace(identity)
	if parts := strings.Split(account, ":"); len(parts) > 4 {
		account = parts[4]
	}
	if account == "" {
		return
	}
	r.accountsMu.Lock()
	defer r.accountsMu.Unlock()
	r.accounts[profile] = account
}

// resolveProfileName is profile, else AWS_PROFILE, else the default profile.
func resolveProfileName(profile string) string {
	if profile = strings.TrimSpace(profile); profile != "" {
		return profile
	}
	if profile = strings.TrimSpace(os.Getenv("AWS_PROFILE")); profile != "" {
		return profile
	}
	return defaultAwsProfile
}

func (r *Runner) resolveAWSConfig(ctx context.Context, options *Options) (awsConfig, error) {
	profile := resolveProfileName(options.AwsProfile)

	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return awsConfig{}, i18n.NewError("jumphost.runner.no_aws_region_for",
			profile, profile)
	}

	cfg := awsConfig{
		Profile: profile,
		Region:  region,
		Args:    []string{"--profile", profile, "--region", region},
	}

	fprintf(r.out, "Using AWS profile/region... %s/%s\n", cfg.Profile, cfg.Region)
	if err := r.ensureIdentity(ctx, cfg, !options.Unattended); err != nil {
		return awsConfig{}, err
	}

	return cfg, nil
}

// ResolveRegion returns the region used when none is given explicitly: AWS_REGION, then the region
// configured for profile, then the configured default region; "" when none.
func (r *Runner) ResolveRegion(ctx context.Context, profile string) string {
	if region := strings.TrimSpace(os.Getenv("AWS_REGION")); region != "" {
		return region
	}

	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if profile == "" {
		profile = defaultAwsProfile
	}

	if region := awsapi.ProfileRegion(ctx, profile); region != "" {
		return region
	}
	return r.defaultRegion
}

// settingHint tells how to fix a setting of the config file: look at it, change it, or override it.
func settingHint(key, example, flag string) string {
	return fmt.Sprintf("check it with `airlock config show`, change it with `airlock config set %s %s`, or pass %s", key, example, flag)
}

// jumphostNotFoundError explains that no running instance carries the jumphost tag, which may be
// the wrong profile or region, or a tag that does not match the account's conventions.
func jumphostNotFoundError(tag awstag.Tag, profile, region string) error {
	return i18n.NewError("jumphost.runner.no_running_jumphost_instance",
		tag, region, profile, settingHint("aws.jumphostTag", "Key=Value", "--jumphost-tag"))
}

// ssoLogin signs profile in again with AWS IAM Identity Center, on the user's terminal.
func (r *Runner) ssoLogin(ctx context.Context, profile string) error {
	interactive := interactiveFrom(ctx)
	if interactive == nil {
		interactive = r.interactive
	}
	if interactive == nil {
		return awssso.Login(ctx, profile, awssso.TerminalPrompt(r.err), awssso.TerminalCode(r.in, r.err))
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	purpose := fmt.Sprintf("The AWS credentials of profile %s are missing or expired: signing in again.", profile)
	return interactive(ctx, purpose, executable, "sso-login", "--profile", profile)
}

// ensureIdentity checks the AWS identity of cfg, signing in again when it is missing or expired,
// unless interactive is false: then it fails with ErrSignInRequired. A profile that cannot be
// loaded or AWS out of reach fails as is: signing in would not fix it.
func (r *Runner) ensureIdentity(ctx context.Context, cfg awsConfig, interactive bool) error {
	awsCfg, err := r.sdkConfig(ctx, cfg)
	if err != nil {
		return err
	}
	fprintf(r.out, "Using AWS identity... ")
	identity, err := callerArn(ctx, &awsCfg)
	if err == nil {
		fprintf(r.out, "%s\n", identity)
		r.rememberAccount(cfg.Profile, identity)
		return nil
	}

	// One sign-in at a time: checks failing together would each open the browser. A check that
	// waited finds the identity signed in by the one before it.
	r.loginMu.Lock()
	defer r.loginMu.Unlock()
	if identity, err = callerArn(ctx, &awsCfg); err == nil {
		fprintf(r.out, "%s\n", identity)
		r.rememberAccount(cfg.Profile, identity)
		return nil
	}
	if checkErr := identityCheckError(ctx, cfg.Profile, err); checkErr != nil {
		return checkErr
	}

	if !interactive {
		return i18n.NewError("jumphost.identity.aws_credentials_of_profile", ErrSignInRequired, cfg.Profile)
	}
	fprintf(r.out, "Failed. Reauthorize...\n")
	loginProfile := awsapi.LoginProfile(ctx, cfg.Profile)
	if loginProfile == "" {
		loginProfile = defaultAwsProfile
	}

	if authErr := r.ssoLogin(ctx, loginProfile); authErr != nil {
		return i18n.NewError("jumphost.runner.failed_to_authorize_please", authErr)
	}

	// Credentials cached before the sign-in are the ones STS rejected.
	if cache, ok := awsCfg.Credentials.(*aws.CredentialsCache); ok {
		cache.Invalidate()
	}
	fprintf(r.out, "Using AWS identity... ")
	if identity, err = callerArn(ctx, &awsCfg); err != nil {
		return i18n.NewError("jumphost.runner.failed_to_retrieve_current")
	}

	fprintf(r.out, "%s\n", identity)
	r.rememberAccount(cfg.Profile, identity)
	return nil
}

func (r *Runner) getJumphostID(ctx context.Context, cfg awsConfig) (string, error) {
	fprintf(r.out, "Using jumphost... ")
	awsCfg, err := r.sdkConfig(ctx, cfg)
	if err != nil {
		return "", err
	}
	pages := ec2.NewDescribeInstancesPaginator(ec2.NewFromConfig(awsCfg), &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:" + r.jumphostTag.Key), Values: []string{r.jumphostTag.Value}},
		{Name: aws.String("instance-state-name"), Values: []string{"running"}},
	}})
	// A filtered page can be empty and still have a next one: the first instance of any page wins.
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", i18n.NewError("jumphost.runner.jumphost_lookup", awsCallError(err))
		}
		for _, reservation := range page.Reservations {
			for i := range reservation.Instances {
				if jumphostID := aws.ToString(reservation.Instances[i].InstanceId); jumphostID != "" {
					fprintf(r.out, "%s\n", jumphostID)
					return jumphostID, nil
				}
			}
		}
	}
	return "", jumphostNotFoundError(r.jumphostTag, cfg.Profile, cfg.Region)
}

// sdkConfig is r.configs.Get with a translated error.
func (r *Runner) sdkConfig(ctx context.Context, cfg awsConfig) (aws.Config, error) {
	c, err := r.configs.Get(ctx, cfg.Profile, cfg.Region)
	if err != nil {
		return aws.Config{}, i18n.NewError("jumphost.runner.aws_config", err)
	}
	return c, nil
}
