package awsapi

import (
	"context"
	"crypto/sha1" // #nosec G505 -- the AWS CLI's cache file name, not a security hash.
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// TokenCode asks the user for the current code of the MFA device serial.
type TokenCode func(serial string) (string, error)

// ErrMFACodeRequired reports role credentials that only a terminal can renew, by typing an MFA code.
var ErrMFACodeRequired = errors.New("MFA code required")

// mfaExpiryWindow is how early cached role credentials count as expired: 5 minutes more than the
// AWS CLI's 15, so a tunnel's `aws ssm start-session` started from them never asks for a code.
const mfaExpiryWindow = 20 * time.Minute

// MFAProfile reports whether profile, or a profile it takes its role credentials from through
// source_profile, assumes a role with an MFA code (role_arn and mfa_serial). The SDK, like the AWS
// CLI, ignores mfa_serial on a profile without role_arn.
func MFAProfile(ctx context.Context, profile string) bool {
	shared, err := SharedProfile(ctx, profile)
	if err != nil {
		return false
	}
	hop, _ := mfaHop(&shared)
	return hop != nil
}

// LoginProfile is the profile to sign in so that profile works again: the one of its source_profile
// chain that asks for an MFA code, else the end of the chain (profile itself without source_profile).
func LoginProfile(ctx context.Context, profile string) string {
	shared, err := SharedProfile(ctx, profile)
	if err != nil {
		return profile
	}
	if hop, _ := mfaHop(&shared); hop != nil {
		return hop.Profile
	}
	last := &shared
	for last.Source != nil {
		last = last.Source
	}
	return last.Profile
}

// mfaHop returns the profile of the source_profile chain of shared that assumes a role with an MFA
// code, and the profiles assuming a role above it, shared first; nil when none asks for a code.
func mfaHop(shared *config.SharedConfig) (hop *config.SharedConfig, above []*config.SharedConfig) {
	for s := shared; s != nil && s.RoleARN != ""; s = s.Source {
		if s.MFASerial != "" {
			return s, above
		}
		above = append(above, s)
	}
	return nil, nil
}

// SignInMFA assumes the MFA role of profile (LoginProfile) with a code read by code, always asking
// for a new one, and caches the credentials where every airlock process and the AWS CLI find them.
func SignInMFA(ctx context.Context, profile string, code TokenCode) error {
	profile = LoginProfile(ctx, profile)
	cfg, err := load(ctx, profile, "", code, config.WithDefaultRegion("us-east-1")) // STS is global.
	if err != nil {
		return err
	}
	path := mfaCachePath(&cfg)
	if path == "" {
		return i18n.NewError("awsapi.mfa.not_mfa", profile)
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	return writeCachedRole(path, &creds)
}

// load loads the config of profile and region; an MFA code is asked with code, or fails with
// ErrMFACodeRequired when code is nil.
func load(ctx context.Context, profile, region string, code TokenCode, opts ...func(*config.LoadOptions) error) (aws.Config, error) {
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	// Runs after the SDK's own options, which set SerialNumber from mfa_serial.
	opts = append(opts, config.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
		serial := aws.ToString(o.SerialNumber)
		o.TokenProvider = func() (string, error) {
			if code == nil {
				return "", i18n.NewError("awsapi.mfa.required", ErrMFACodeRequired, profile, serial, profile)
			}
			return code(serial)
		}
	}))
	return config.LoadDefaultConfig(ctx, opts...)
}

// withCachedRole makes the credentials of a profile asking for an MFA code, itself or down its
// source_profile chain, read the shared role cache first, so the code typed in one process serves
// all of them until the role session expires.
func withCachedRole(ctx context.Context, cfg *aws.Config) error {
	shared, ok := sharedConfigOf(cfg)
	if !ok {
		return nil
	}
	hop, above := mfaHop(&shared)
	if hop == nil {
		return nil
	}
	if len(above) == 0 {
		cfg.Credentials = aws.NewCredentialsCache(&cachedRole{path: mfaCachePath(cfg), assume: cfg.Credentials}, func(o *aws.CredentialsCacheOptions) {
			o.ExpiryWindow = mfaExpiryWindow
		})
		return nil
	}
	// The SDK's chain would assume the MFA role without the cache: assume the roles above it from
	// the cached MFA role credentials instead, as the AWS CLI does.
	hopCfg, err := load(ctx, hop.Profile, cfg.Region, nil)
	if err != nil {
		return err
	}
	if err := withCachedRole(ctx, &hopCfg); err != nil {
		return err
	}
	creds := hopCfg.Credentials
	for i := len(above) - 1; i >= 0; i-- {
		creds = assumeRole(cfg, above[i], creds)
	}
	cfg.Credentials = creds
	return nil
}

// assumeRole assumes the role of profile with source credentials, as the SDK's chain would.
func assumeRole(cfg *aws.Config, profile *config.SharedConfig, source aws.CredentialsProvider) aws.CredentialsProvider {
	client := sts.NewFromConfig(*cfg, func(o *sts.Options) { o.Credentials = source })
	return aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(client, profile.RoleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = profile.RoleSessionName
		if profile.RoleDurationSeconds != nil && *profile.RoleDurationSeconds > 15*time.Minute {
			o.Duration = *profile.RoleDurationSeconds
		}
		if profile.ExternalID != "" {
			o.ExternalID = aws.String(profile.ExternalID)
		}
	}))
}

// cachedRole returns the cached role credentials, else assumes the role (asking for a code).
type cachedRole struct {
	assume aws.CredentialsProvider
	path   string
}

func (c *cachedRole) Retrieve(ctx context.Context) (aws.Credentials, error) {
	if creds, ok := readCachedRole(c.path); ok {
		return creds, nil
	}
	creds, err := c.assume.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	if err := writeCachedRole(c.path, &creds); err != nil {
		return aws.Credentials{}, err
	}
	return creds, nil
}

// sharedConfigOf returns the shared profile cfg was loaded from.
func sharedConfigOf(cfg *aws.Config) (config.SharedConfig, bool) {
	for _, source := range cfg.ConfigSources {
		if shared, ok := source.(config.SharedConfig); ok {
			return shared, true
		}
	}
	return config.SharedConfig{}, false
}

// mfaCachePath returns the AWS CLI's cache file of the role of cfg's profile ("" when it asks for no
// MFA code): ~/.aws/cli/cache/<sha1 of the AssumeRole arguments>.json, as botocore names it.
func mfaCachePath(cfg *aws.Config) string {
	shared, ok := sharedConfigOf(cfg)
	if !ok || shared.RoleARN == "" || shared.MFASerial == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", "cli", "cache", roleCacheKey(&shared)+".json")
}

// roleCacheKey is botocore's AssumeRoleCredentialFetcher cache key: the sha1 of the AssumeRole
// arguments taken from the profile, as Python's json.dumps(args, sort_keys=True) writes them.
func roleCacheKey(shared *config.SharedConfig) string {
	var args []string // in key order.
	add := func(key, value string) { args = append(args, strconv.Quote(key)+": "+value) }
	if shared.RoleDurationSeconds != nil {
		add("DurationSeconds", strconv.Itoa(int(shared.RoleDurationSeconds.Seconds())))
	}
	if shared.ExternalID != "" {
		add("ExternalId", strconv.Quote(shared.ExternalID))
	}
	add("RoleArn", strconv.Quote(shared.RoleARN))
	if shared.RoleSessionName != "" {
		add("RoleSessionName", strconv.Quote(shared.RoleSessionName))
	}
	add("SerialNumber", strconv.Quote(shared.MFASerial))
	// ponytail: strconv.Quote matches json.dumps for ASCII only; ARNs, ids and session names are ASCII.
	sum := sha1.Sum([]byte("{" + strings.Join(args, ", ") + "}")) // #nosec G401 -- same.
	return hex.EncodeToString(sum[:])
}

// cachedRoleFile is the part of an AWS CLI cache file (an AssumeRole response) airlock uses.
type cachedRoleFile struct {
	Credentials struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	} `json:"Credentials"`
}

// readCachedRole returns the credentials cached at path, unless they are missing, unreadable or
// within mfaExpiryWindow of their expiry.
func readCachedRole(path string) (aws.Credentials, bool) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the AWS CLI's cache file.
	if err != nil {
		return aws.Credentials{}, false
	}
	var file cachedRoleFile
	if json.Unmarshal(raw, &file) != nil {
		return aws.Credentials{}, false
	}
	expires, err := time.Parse(time.RFC3339, file.Credentials.Expiration)
	if err != nil { // botocore writes strftime("%Y-%m-%dT%H:%M:%S%Z"): 2026-10-21T10:00:00UTC.
		expires, err = time.Parse("2006-01-02T15:04:05MST", file.Credentials.Expiration)
	}
	if err != nil || file.Credentials.AccessKeyID == "" || time.Until(expires) <= mfaExpiryWindow {
		return aws.Credentials{}, false
	}
	return aws.Credentials{AccessKeyID: file.Credentials.AccessKeyID, SecretAccessKey: file.Credentials.SecretAccessKey,
		SessionToken: file.Credentials.SessionToken, Source: "airlock MFA cache", CanExpire: true, Expires: expires}, true
}

// writeCachedRole replaces path atomically with creds, in a file only the user can read.
func writeCachedRole(path string, creds *aws.Credentials) error {
	var file cachedRoleFile
	file.Credentials.AccessKeyID, file.Credentials.SecretAccessKey = creds.AccessKeyID, creds.SecretAccessKey
	file.Credentials.SessionToken = creds.SessionToken
	file.Credentials.Expiration = creds.Expires.UTC().Format(time.RFC3339)
	raw, err := json.Marshal(&file) // #nosec G117 -- the credentials are meant for this 0600 file.
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return mkErr
	}
	tmp, err := os.CreateTemp(dir, ".airlock-mfa-*") // 0600.
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:errcheck // gone after the rename.
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close() //nolint:errcheck // reporting the write error.
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
