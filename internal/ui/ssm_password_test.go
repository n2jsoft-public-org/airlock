package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

// newPasswordTestModel returns a dashboard whose session reads creds from its secrets, reads
// counting the calls, and whose copies land in *copied.
func newPasswordTestModel(t *testing.T, creds []jumphost.DBCredentials, readErr error) (m ssmModel, copied *[]string, reads *int) {
	t.Helper()
	m, copied = newCopyTestModel(t)
	reads = new(int)
	session := m.sessions[0].session
	session.Details.Auth = "Secrets Manager · rds/archive/app · app_rw"
	session.ReadCredentials = func(context.Context) ([]jumphost.DBCredentials, error) {
		*reads++
		return creds, readErr
	}
	return m, copied, reads
}

// pickedSecret is the credentials of a session signing in with a picked secret.
var pickedSecret = []jumphost.DBCredentials{{User: "app_rw", Password: "s3cr3t-Pa55"}}

// revealed presses p and feeds the secret read back.
func revealed(t *testing.T, m ssmModel) ssmModel {
	t.Helper()
	m, cmd := update(t, m, key("p"))
	if m.mode != ssmModePassword || cmd == nil {
		t.Fatalf("mode = %v, want the password dialog reading the secret", m.mode)
	}
	if view := plainView(m); !strings.Contains(view, "Reading the secrets") {
		t.Fatalf("dialog does not say the secret is being read:\n%s", view)
	}
	m, _ = update(t, m, cmd())
	return m
}

func TestPasswordDialogShowsTheSecretPassword(t *testing.T) {
	m, _, reads := newPasswordTestModel(t, pickedSecret, nil)

	if view := plainView(m); !strings.Contains(view, "show password") {
		t.Fatalf("details do not offer to show the password:\n%s", view)
	}
	if strings.Contains(plainView(m), "s3cr3t-Pa55") {
		t.Fatal("password shown before being asked for")
	}

	m = revealed(t, m)
	view := plainView(m)
	for _, want := range []string{"Database password", "rds/archive/app", "s3cr3t-Pa55", "Copy", "Hide"} {
		if !strings.Contains(view, want) {
			t.Fatalf("password dialog lacks %q:\n%s", want, view)
		}
	}

	m, _ = update(t, m, key("esc"))
	if m.mode != ssmModeSessions || m.password.creds != nil || strings.Contains(plainView(m), "s3cr3t-Pa55") {
		t.Fatalf("hiding kept the password: mode = %v\n%s", m.mode, plainView(m))
	}

	// Each reveal reads the secret again, so a rotated one shows its current value.
	revealed(t, m)
	if *reads != 2 {
		t.Fatalf("secret read %d times, want 2", *reads)
	}
}

func TestPasswordDialogCopiesThePassword(t *testing.T) {
	m, copied, _ := newPasswordTestModel(t, pickedSecret, nil)

	m = revealed(t, m)
	m = copyChosen(t, m, "c")
	if len(*copied) != 1 || (*copied)[0] != "s3cr3t-Pa55" {
		t.Fatalf("copied = %q, want the password", *copied)
	}
	if m.mode != ssmModeSessions || m.password.creds != nil || !strings.Contains(plainView(m), "Copied Password") {
		t.Fatalf("mode = %v, want the sessions back with a confirmation:\n%s", m.mode, plainView(m))
	}

	// enter picks the focused button: Copy first, Hide once moved to.
	m = copyChosen(t, revealed(t, m), "enter")
	if len(*copied) != 2 {
		t.Fatalf("enter on Copy copied %d values, want 2", len(*copied))
	}
	m = copyChosen(t, revealed(t, m), "right", "enter")
	if len(*copied) != 2 || m.mode != ssmModeSessions {
		t.Fatalf("enter on Hide copied or stayed open: copied = %d, mode = %v", len(*copied), m.mode)
	}
}

func TestPasswordDialogShowsAReadFailure(t *testing.T) {
	m, copied, _ := newPasswordTestModel(t, nil, errors.New("failed to read secret rds/archive/app: AccessDeniedException"))

	m = revealed(t, m)
	if view := plainView(m); !strings.Contains(view, "AccessDeniedException") {
		t.Fatalf("dialog does not explain the failure:\n%s", view)
	}
	m = copyChosen(t, m, "c")
	if len(*copied) != 0 || m.mode != ssmModePassword {
		t.Fatalf("copy without a password: copied = %q, mode = %v", *copied, m.mode)
	}
	m, _ = update(t, m, key("enter"))
	if m.mode != ssmModeSessions {
		t.Fatalf("enter after a failure: mode = %v, want the dialog hidden", m.mode)
	}
}

func TestPasswordReadAfterHidingIsDropped(t *testing.T) {
	m, _, _ := newPasswordTestModel(t, pickedSecret, nil)

	m, cmd := update(t, m, key("p"))
	m, _ = update(t, m, key("esc"))
	m, _ = update(t, m, cmd())
	if m.mode != ssmModeSessions || m.password.creds != nil {
		t.Fatalf("late read reopened or kept the password: mode = %v, creds = %v", m.mode, m.password.creds)
	}
}

func TestPasswordKeyNeedsASecretSession(t *testing.T) {
	m, _ := newCopyTestModel(t)

	if view := plainView(m); strings.Contains(view, "show password") {
		t.Fatalf("session without secret offers to show a password:\n%s", view)
	}
	m, cmd := update(t, m, key("p"))
	if m.mode != ssmModeSessions || cmd != nil {
		t.Fatalf("p opened mode %v on a session without secret", m.mode)
	}
	if view := plainView(m); !strings.Contains(view, "does not read its password from Secrets Manager") {
		t.Fatalf("p gives no explanation:\n%s", view)
	}
}

func TestPasswordDialogListsTheReadOnlySecretOfEachDatabase(t *testing.T) {
	m, copied, _ := newPasswordTestModel(t, []jumphost.DBCredentials{
		{Database: "app_db", User: "app_ro", Password: "app-Pa55"},
		{Secret: "audit-integ", User: "app_ro", Password: "audit-Pa55"},
	}, nil)
	m.sessions[0].session.Details.Auth = "Secrets Manager · read-only secrets Role=read-only"

	m = revealed(t, m)
	view := plainView(m)
	for _, want := range []string{"Database passwords", "Role=read-only", "1  app_db · app_ro", "app-Pa55", "2  audit-integ · app_ro", "audit-Pa55"} {
		if !strings.Contains(view, want) {
			t.Fatalf("password dialog lacks %q:\n%s", want, view)
		}
	}

	m = copyChosen(t, m, "2")
	if len(*copied) != 1 || (*copied)[0] != "audit-Pa55" || !strings.Contains(plainView(m), "Copied Password of audit-integ") {
		t.Fatalf("copied = %q, want the password of audit-integ:\n%s", *copied, plainView(m))
	}
	copyChosen(t, revealed(t, m), "down", "up", "down", "down", "c")
	if len(*copied) != 2 || (*copied)[1] != "audit-Pa55" {
		t.Fatalf("copied = %q, want the selected password", *copied)
	}
}

func TestPasswordDialogWithoutCredentialsCopiesNothing(t *testing.T) {
	m, copied, _ := newPasswordTestModel(t, []jumphost.DBCredentials{}, nil)

	m = copyChosen(t, revealed(t, m), "c", "1", "down")
	if len(*copied) != 0 || !strings.Contains(plainView(m), "No credentials found") {
		t.Fatalf("copied = %q with no credentials:\n%s", *copied, plainView(m))
	}
}
