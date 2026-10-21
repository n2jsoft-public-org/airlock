package awsapi_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi"
)

func writeShared(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProfilesReadsBothFiles(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", writeShared(t, "config",
		"[default]\nregion = eu-west-3\n\n[profile dev]\nsso_session = corp\n\n[sso-session corp]\nsso_region = eu-west-3\n\n[services s]\n"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", writeShared(t, "credentials", "[ci]\naws_access_key_id = x\n\n[dev]\naws_access_key_id = y\n"))
	got, err := awsapi.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ci", "default", "dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %q, want %q", got, want)
	}
}

func TestProfilesWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	got, err := awsapi.Profiles()
	if err != nil || len(got) != 0 {
		t.Fatalf("profiles = %q, err = %v, want none", got, err)
	}
}

func TestProfileRegion(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", writeShared(t, "config", "[profile dev]\nregion = us-east-1\n\n[profile prod]\n"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", writeShared(t, "credentials", ""))
	for profile, want := range map[string]string{"dev": "us-east-1", "prod": "", "unknown": ""} {
		if got := awsapi.ProfileRegion(t.Context(), profile); got != want {
			t.Errorf("ProfileRegion(%q) = %q, want %q", profile, got, want)
		}
	}
}
