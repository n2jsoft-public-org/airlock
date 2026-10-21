package awsapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

const (
	mfaSerial = "arn:aws:iam::123456789012:mfa/me"
	// mfaCacheFile is the name botocore gives the role of mfaProfiles: the sha1 of
	// json.dumps({"RoleArn": …, "SerialNumber": …}, sort_keys=True).
	mfaCacheFile = "b26e45a5efba11b34b676c5657f44efc9db2add0.json"
)

const mfaRole = "arn:aws:iam::123456789012:role/admin"

// fakeSTS answers AssumeRole with credentials expiring after lifetime, recording the token codes of
// the MFA role and, for every role, the access key that signed the call.
type fakeSTS struct {
	signers  map[string]string // role name → access key id of the caller
	codes    []string
	calls    atomic.Int32
	lifetime time.Duration
}

func (f *fakeSTS) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if op := awsapitest.Operation(r); op != "AssumeRole" {
			t.Errorf("unexpected call %s", op)
			return
		}
		f.calls.Add(1)
		role := r.FormValue("RoleArn")
		serial := r.FormValue("SerialNumber")
		if (role == mfaRole) != (serial == mfaSerial) {
			t.Errorf("role %s: SerialNumber = %q", role, serial)
		}
		if role == mfaRole {
			f.codes = append(f.codes, r.FormValue("TokenCode"))
		}
		name := role[strings.LastIndex(role, "/")+1:]
		_, signer, _ := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		signer, _, _ = strings.Cut(signer, "/")
		f.signers[name] = signer
		key := "ASIAMFA"
		if role != mfaRole {
			key = "ASIA" + strings.ToUpper(name)
		}
		awsapitest.RespondXML(t, w, 200, fmt.Sprintf(`<AssumeRoleResponse><AssumeRoleResult><Credentials>
<AccessKeyId>%s</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken>
<Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`, key, time.Now().Add(f.lifetime).UTC().Format(time.RFC3339)))
	}
}

// mfaEnv serves a fake STS and writes an "mfa" profile assuming a role with MFA from "base", and
// "prod" then "top" chaining roles above it.
func mfaEnv(t *testing.T, lifetime time.Duration) *fakeSTS {
	t.Helper()
	sts := &fakeSTS{lifetime: lifetime, signers: map[string]string{}}
	awsapitest.Env(t, sts.handle(t), "base")
	profile := "[profile base]\nregion = eu-west-3\n\n[profile mfa]\nregion = eu-west-3\nsource_profile = base\n" +
		"role_arn = " + mfaRole + "\nmfa_serial = " + mfaSerial + "\n\n" +
		"[profile prod]\nregion = eu-west-3\nsource_profile = mfa\nrole_arn = arn:aws:iam::123456789012:role/prod\n\n" +
		"[profile top]\nregion = eu-west-3\nsource_profile = prod\nrole_arn = arn:aws:iam::123456789012:role/top\n"
	// #nosec G703 -- the test's own temp file.
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	return sts
}

func TestSignInMFAAsksOnceAndSharesTheCredentials(t *testing.T) {
	sts := mfaEnv(t, time.Hour)
	ctx := context.Background()
	asked := 0
	code := func(serial string) (string, error) {
		asked++
		if serial != mfaSerial {
			t.Errorf("serial = %q", serial)
		}
		return "123456", nil
	}
	if err := awsapi.SignInMFA(ctx, "mfa", code); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || len(sts.codes) != 1 || sts.codes[0] != "123456" {
		t.Fatalf("asked %d times, token codes sent = %v", asked, sts.codes)
	}

	path := filepath.Join(os.Getenv("HOME"), ".aws", "cli", "cache", mfaCacheFile)
	info, err := os.Stat(path) // #nosec G703 -- the test's own temp dir.
	if err != nil {
		t.Fatalf("the AWS CLI's cache file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	// Another process: a fresh Configs reads the cache, with no code and no AssumeRole call.
	for range 2 {
		cfg, err := awsapi.NewConfigs().Get(ctx, "mfa", "eu-west-3")
		if err != nil {
			t.Fatal(err)
		}
		creds, err := cfg.Credentials.Retrieve(ctx)
		if err != nil || creds.AccessKeyID != "ASIAMFA" || creds.SessionToken != "token" {
			t.Fatalf("creds = %+v, err = %v", creds, err)
		}
	}
	if n := sts.calls.Load(); n != 1 {
		t.Errorf("AssumeRole calls = %d, want 1", n)
	}
}

func TestMFAProfileWithoutCodeFailsWithoutPrompting(t *testing.T) {
	sts := mfaEnv(t, time.Hour)
	ctx := context.Background()
	cfg, err := awsapi.NewConfigs().Get(ctx, "mfa", "eu-west-3")
	if err != nil {
		t.Fatalf("an MFA profile must load: %v", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); !errors.Is(err, awsapi.ErrMFACodeRequired) {
		t.Fatalf("err = %v, want ErrMFACodeRequired", err)
	}
	if n := sts.calls.Load(); n != 0 {
		t.Errorf("AssumeRole calls = %d, want none", n)
	}
}

func TestMFACacheNearExpiryAsksAgain(t *testing.T) {
	mfaEnv(t, 18*time.Minute) // inside airlock's 20-minute window, outside the AWS CLI's 15.
	ctx := context.Background()
	if err := awsapi.SignInMFA(ctx, "mfa", func(string) (string, error) { return "123456", nil }); err != nil {
		t.Fatal(err)
	}
	cfg, err := awsapi.NewConfigs().Get(ctx, "mfa", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); !errors.Is(err, awsapi.ErrMFACodeRequired) {
		t.Fatalf("err = %v, want ErrMFACodeRequired", err)
	}
}

func TestLoginProfile(t *testing.T) {
	mfaEnv(t, time.Hour)
	f, err := os.OpenFile(os.Getenv("AWS_CONFIG_FILE"), os.O_APPEND|os.O_WRONLY, 0) // #nosec G703 -- same.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[profile child]\nsource_profile = base\nrole_arn = arn:aws:iam::1:role/r\n" +
		"\n[profile grandchild]\nsource_profile = child\nrole_arn = arn:aws:iam::1:role/g\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for profile, want := range map[string]string{"mfa": "mfa", "top": "mfa", "child": "base", "grandchild": "base", "base": "base"} {
		if got := awsapi.LoginProfile(ctx, profile); got != want {
			t.Errorf("LoginProfile(%s) = %q, want %q", profile, got, want)
		}
	}
}

func TestChainedRolesAboveAnMFARoleUseItsCache(t *testing.T) {
	sts := mfaEnv(t, time.Hour)
	ctx := context.Background()
	if !awsapi.MFAProfile(ctx, "top") || awsapi.MFAProfile(ctx, "base") {
		t.Fatal("MFAProfile must follow source_profile to the MFA role")
	}
	cfg, err := awsapi.NewConfigs().Get(ctx, "top", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.Credentials.Retrieve(ctx); !errors.Is(err, awsapi.ErrMFACodeRequired) {
		t.Fatalf("before sign-in: err = %v, want ErrMFACodeRequired", err)
	}

	// The sign-in of the chain's top asks the code of the MFA role, cached as botocore names it.
	asked := 0
	if err = awsapi.SignInMFA(ctx, "top", func(string) (string, error) { asked++; return "123456", nil }); err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- the test's own temp dir.
	if _, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".aws", "cli", "cache", mfaCacheFile)); asked != 1 || err != nil {
		t.Fatalf("asked %d times, cache file: %v", asked, err)
	}

	cfg, err = awsapi.NewConfigs().Get(ctx, "top", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil || creds.AccessKeyID != "ASIATOP" {
		t.Fatalf("creds = %+v, err = %v", creds, err)
	}
	if len(sts.codes) != 1 || sts.signers["prod"] != "ASIAMFA" || sts.signers["top"] != "ASIAPROD" {
		t.Errorf("codes = %v, callers = %v: want prod assumed with the cached MFA role, top with prod", sts.codes, sts.signers)
	}
}

func TestMFACacheReadsTheAWSCLIExpirationFormat(t *testing.T) {
	sts := mfaEnv(t, time.Hour)
	ctx := context.Background()
	// botocore writes datetimes with strftime("%Y-%m-%dT%H:%M:%S%Z").
	expires := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05") + "UTC"
	dir := filepath.Join(os.Getenv("HOME"), ".aws", "cli", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- the test's own temp dir.
		t.Fatal(err)
	}
	file := `{"Credentials": {"AccessKeyId": "ASIACLI", "SecretAccessKey": "s", "SessionToken": "t", "Expiration": "` + expires + `"}}`
	if err := os.WriteFile(filepath.Join(dir, mfaCacheFile), []byte(file), 0o600); err != nil { // #nosec G703 -- same.
		t.Fatal(err)
	}
	cfg, err := awsapi.NewConfigs().Get(ctx, "mfa", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	if creds, err := cfg.Credentials.Retrieve(ctx); err != nil || creds.AccessKeyID != "ASIACLI" {
		t.Fatalf("creds = %+v, err = %v", creds, err)
	}
	if n := sts.calls.Load(); n != 0 {
		t.Errorf("AssumeRole calls = %d, want none", n)
	}
}
