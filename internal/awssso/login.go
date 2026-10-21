// Package awssso signs a profile in with AWS IAM Identity Center or an MFA code, without the aws CLI.
package awssso

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/platform"
)

// Prompt tells the user where to confirm the sign-in.
type Prompt func(verificationURL, userCode string)

// TerminalPrompt prints the confirmation page and code to w and opens the page in the browser;
// a browser that does not open only leaves the printed URL.
func TerminalPrompt(w io.Writer) Prompt {
	return func(verificationURL, userCode string) {
		_, _ = fmt.Fprintf(w, "Open %s and confirm the code %s\n", verificationURL, userCode) //nolint:errcheck // a closed terminal has nothing to tell.
		if err := platform.OpenURL(verificationURL); err != nil {
			_, _ = fmt.Fprintf(w, "Could not open the browser: %v\n", err) //nolint:errcheck // same.
		}
	}
}

// TerminalCode asks for the MFA code on w and reads it from in, one line.
func TerminalCode(in io.Reader, w io.Writer) awsapi.TokenCode {
	return func(serial string) (string, error) {
		_, _ = fmt.Fprintf(w, "MFA code for %s: ", serial) //nolint:errcheck // a closed terminal fails the read below.
		line, err := bufio.NewReader(in).ReadString('\n')
		code := strings.TrimSpace(line)
		if code == "" {
			return "", i18n.NewError("awssso.no_mfa_code", errors.Join(err, awsapi.ErrMFACodeRequired))
		}
		return code, nil
	}
}

const (
	grantType       = "urn:ietf:params:oauth:grant-type:device_code"
	defaultInterval = 5 * time.Second
	slowDownStep    = 5 * time.Second
)

// wait sleeps for d, or until ctx ends; tests replace it.
var wait = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tokenFile is the AWS CLI's SSO cache file, which the SDK's token provider reads.
type tokenFile struct {
	StartURL              string `json:"startUrl"`
	Region                string `json:"region"`
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	RegistrationExpiresAt string `json:"registrationExpiresAt,omitempty"`
	RefreshToken          string `json:"refreshToken,omitempty"`
}

// Login signs profile in and caches the result where the SDK and the aws CLI find it: a profile
// assuming a role with MFA asks for a code with code, others use the OAuth device-code flow.
// ponytail: device-code flow only; add PKCE (authorization_code + loopback redirect) if users ask for the AWS CLI's default sign-in UX.
func Login(ctx context.Context, profile string, prompt Prompt, code awsapi.TokenCode) error {
	if awsapi.MFAProfile(ctx, profile) {
		return awsapi.SignInMFA(ctx, profile, code)
	}
	shared, err := awsapi.SharedProfile(ctx, profile)
	if err != nil {
		return i18n.NewError("awssso.profile", profile, err)
	}
	session := shared.SSOSession
	key, startURL, region := "", shared.SSOStartURL, shared.SSORegion
	if session != nil {
		key, startURL, region = session.Name, session.SSOStartURL, session.SSORegion
	}
	if startURL == "" || region == "" {
		return i18n.NewError("awssso.not_sso", profile)
	}
	if key == "" {
		key = startURL
	}
	path, err := ssocreds.StandardCachedTokenFilepath(key)
	if err != nil {
		return err
	}

	// The OIDC calls are unsigned: a profile whose own credentials are broken must still sign in.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithSharedConfigProfile(profile),
		config.WithSharedConfigFiles(sharedFiles()), config.WithCredentialsProvider(aws.AnonymousCredentials{}))
	if err != nil {
		return err
	}
	client := ssooidc.NewFromConfig(cfg)

	registerInput := &ssooidc.RegisterClientInput{ClientName: aws.String("airlock"), ClientType: aws.String("public")}
	// ponytail: sso_registration_scopes is ignored, so a session with extra scopes gets a narrower token in the shared cache; read it from the [sso-session] section if a user needs it.
	if session != nil {
		registerInput.Scopes = []string{"sso:account:access"}
	}
	registration, err := client.RegisterClient(ctx, registerInput)
	if err != nil {
		return i18n.NewError("awssso.call", err)
	}
	device, err := client.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId: registration.ClientId, ClientSecret: registration.ClientSecret, StartUrl: aws.String(startURL)})
	if err != nil {
		return i18n.NewError("awssso.call", err)
	}
	prompt(aws.ToString(device.VerificationUriComplete), aws.ToString(device.UserCode))

	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	for {
		if err := wait(ctx, interval); err != nil {
			return err
		}
		token, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{ClientId: registration.ClientId,
			ClientSecret: registration.ClientSecret, DeviceCode: device.DeviceCode, GrantType: aws.String(grantType)})
		_, slowDown := errors.AsType[*types.SlowDownException](err)
		_, pending := errors.AsType[*types.AuthorizationPendingException](err)
		_, expired := errors.AsType[*types.ExpiredTokenException](err)
		switch {
		case err == nil:
			file := tokenFile{StartURL: startURL, Region: region, AccessToken: aws.ToString(token.AccessToken),
				ExpiresAt: utc(time.Now().Add(time.Duration(token.ExpiresIn) * time.Second))}
			if session != nil {
				file.ClientID, file.ClientSecret = aws.ToString(registration.ClientId), aws.ToString(registration.ClientSecret)
				file.RegistrationExpiresAt = utc(time.Unix(registration.ClientSecretExpiresAt, 0))
				file.RefreshToken = aws.ToString(token.RefreshToken)
			}
			return writeToken(path, &file)
		case slowDown:
			interval += slowDownStep
		case pending:
		case expired:
			return i18n.NewError("awssso.expired")
		default:
			return i18n.NewError("awssso.call", err)
		}
	}
}

// sharedFiles is the shared config file the SDK would use: LoadSharedConfigProfile ignores
// AWS_CONFIG_FILE unless it is named.
func sharedFiles() []string {
	if path := os.Getenv("AWS_CONFIG_FILE"); path != "" {
		return []string{path}
	}
	return []string{config.DefaultSharedConfigFilename()}
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// writeToken replaces path atomically, so a crash never leaves a truncated token the SDK would
// fail to parse.
func writeToken(path string, file *tokenFile) error {
	raw, err := json.Marshal(file) // #nosec G117 -- the token is meant for this 0600 file.
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return mkErr
	}
	tmp, err := os.CreateTemp(dir, ".airlock-sso-*") // 0600.
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
