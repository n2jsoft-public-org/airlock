package doctor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// AWSTimeout bounds the checks of one profile.
const AWSTimeout = 5 * time.Second

// AWS is the part of jumphost.Runner the profile checks call: neither signs in.
type AWS interface {
	Account(ctx context.Context, profile, region string) (string, error)
	FindJumphost(ctx context.Context, profile, region string) (string, error)
}

// Profile is an AWS profile to check in Region ("" when none resolves).
type Profile struct {
	Name, Region string
}

// Profiles checks the sign-in of each profile and, when withJumphost is true and the sign-in is
// valid, its running jumphost; profiles are checked concurrently, each within timeout.
func Profiles(ctx context.Context, aws AWS, profiles []Profile, withJumphost bool, timeout time.Duration) []Result {
	perProfile := make([][]Result, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Go(func() {
			profileCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			perProfile[i] = checkProfile(profileCtx, aws, p, withJumphost, timeout)
		})
	}
	wg.Wait()
	var results []Result
	for _, r := range perProfile {
		results = append(results, r...)
	}
	return results
}

func checkProfile(ctx context.Context, aws AWS, p Profile, withJumphost bool, timeout time.Duration) []Result {
	if p.Region == "" {
		return []Result{warning("aws-profile", p.Name, i18n.Format("doctor.profile.no_region", p.Name), i18n.Format("doctor.profile.no_region_fix", p.Name))}
	}
	account, err := aws.Account(ctx, p.Name, p.Region)
	if err != nil {
		return []Result{signInResult(ctx, p.Name, err, timeout)}
	}
	results := []Result{ok("aws-profile", p.Name, i18n.Format("doctor.profile.ok", p.Name, p.Region, account))}
	if !withJumphost {
		return results
	}
	id, err := aws.FindJumphost(ctx, p.Name, p.Region)
	if err != nil {
		return append(results, jumphostWarning(ctx, p.Name, err, timeout))
	}
	return append(results, ok("jumphost", p.Name, i18n.Format("doctor.jumphost.ok", p.Name, id)))
}

// signInResult tells why the sign-in of profile could not be checked, keeping expired sign-ins
// apart from AWS out of reach. Result is a warning for an expired sign-in, else a failure.
func signInResult(ctx context.Context, profile string, err error, timeout time.Duration) Result {
	switch {
	case errors.Is(err, jumphost.ErrSignInRequired):
		login := profile
		if parent := awsapi.SourceProfile(ctx, profile); parent != "" {
			login = parent
		}
		// A warning: airlock signs in again when the profile is picked.
		return warning("aws-profile", profile, i18n.Format("doctor.profile.expired", profile), i18n.Format("doctor.profile.expired_fix", profile, login))
	case ctx.Err() != nil:
		return failure("aws-profile", profile, i18n.Format("doctor.profile.timeout", profile, timeout), i18n.T("doctor.profile.timeout_fix"))
	case awsapi.Unreachable(err):
		return failure("aws-profile", profile, i18n.Localize(err), i18n.T("doctor.network_fix"))
	case mfaProfile(ctx, profile):
		return failure("aws-profile", profile, i18n.Localize(err), i18n.T("doctor.profile.mfa_fix"))
	default:
		return failure("aws-profile", profile, i18n.Localize(err), i18n.Format("doctor.profile.config_fix", profile))
	}
}

// mfaProfile reports a profile assuming a role with MFA, which airlock does not support.
func mfaProfile(ctx context.Context, profile string) bool {
	shared, err := awsapi.SharedProfile(ctx, profile)
	return err == nil && shared.MFASerial != ""
}

// jumphostWarning words a failed jumphost lookup: a missing IAM permission, AWS out of reach, or
// no running instance with the tag.
func jumphostWarning(ctx context.Context, profile string, err error, timeout time.Duration) Result {
	text := err.Error()
	switch {
	// ponytail: the lookup error keeps the AWS error code only in its text (mcpserve.AWSError).
	case strings.Contains(text, "UnauthorizedOperation") || strings.Contains(text, "AccessDenied"):
		return warning("jumphost", profile, i18n.Format("doctor.jumphost.permission", profile, "ec2:DescribeInstances"), i18n.Format("doctor.jumphost.permission_fix", "ec2:DescribeInstances", profile))
	case ctx.Err() != nil:
		return warning("jumphost", profile, i18n.Format("doctor.jumphost.timeout", profile, timeout), i18n.T("doctor.network_fix"))
	case awsapi.Unreachable(err):
		return warning("jumphost", profile, i18n.Localize(err), i18n.T("doctor.network_fix"))
	default:
		return warning("jumphost", profile, i18n.Localize(err), i18n.T("doctor.jumphost.fix"))
	}
}
