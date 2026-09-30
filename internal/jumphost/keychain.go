package jumphost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/keychain"
)

// ErrKeychain marks the keychain failures of SaveConnectionPrefs: the choices are saved, the
// password is not remembered.
var ErrKeychain = errors.New("keychain")

// keychainAccount names the keychain entry of the password of user on target.
func keychainAccount(target TargetRef, user string) string {
	return target.key() + "/" + strings.TrimSpace(user)
}

// KeychainAvailable reports whether passwords can be remembered in the OS keychain; the keychain
// is probed once.
func (r *Runner) KeychainAvailable() bool {
	if r.keychain == nil {
		return false
	}
	r.keychainOnce.Do(func() { r.keychainOK = keychain.Available(r.keychain) })
	return r.keychainOK
}

// LoadKeychainPassword returns the password of options.DBUser remembered for the target of options.
func (r *Runner) LoadKeychainPassword(ctx context.Context, options Options) (string, bool) { //nolint:gocritic // same by-value Options as LoadConnectionPrefs, its sibling
	if !r.KeychainAvailable() || strings.TrimSpace(options.DBUser) == "" {
		return "", false
	}
	target, ok := r.prefsTarget(ctx, &options)
	if !ok {
		return "", false
	}
	password, err := r.keychain.Get(keychainAccount(target, options.DBUser))
	if err != nil || password == "" {
		return "", false
	}
	return password, true
}

// syncKeychain stores or deletes the password of next, deleting the entry of the previous user
// when it changed. It sets next.PasswordInKeychain to whether the password is now remembered.
func (r *Runner) syncKeychain(target TargetRef, previous, next *ConnectionPrefs, options *Options) error {
	remember := options.RememberPassword && next.SignIn == SignInPassword && options.DBPassword != "" && next.DBUser != ""
	next.PasswordInKeychain = false
	if !r.KeychainAvailable() {
		return nil
	}

	var errs []error
	newAccount := ""
	if remember {
		newAccount = keychainAccount(target, next.DBUser)
	}
	if previous != nil && previous.PasswordInKeychain {
		if old := keychainAccount(target, previous.DBUser); old != newAccount {
			if err := r.keychain.Delete(old); err != nil {
				errs = append(errs, fmt.Errorf("failed to forget the previous password: %w", err))
			}
		}
	}
	if !remember {
		// The previous entry, if any, was deleted above (newAccount is empty).
		return joinKeychainErrors(errs)
	}
	if err := r.keychain.Set(newAccount, options.DBPassword); err != nil {
		errs = append(errs, fmt.Errorf("the password was not remembered: %w", err))
		return joinKeychainErrors(errs)
	}
	next.PasswordInKeychain = true
	return joinKeychainErrors(errs)
}

func joinKeychainErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrKeychain, errors.Join(errs...))
}

// forgetKeychainFunc deletes the keychain entry account, e.g. once the database rejected its
// password; nil when there is nothing to forget.
func (r *Runner) forgetKeychainFunc(account string) func() {
	if account == "" || r.keychain == nil {
		return nil
	}
	return func() {
		if err := r.keychain.Delete(account); err != nil {
			return // best effort: the entry only makes the next attempt use a rejected password
		}
	}
}

// forgetKeychainPassword deletes the password remembered for prefs on target, if any.
func (r *Runner) forgetKeychainPassword(target TargetRef, prefs *ConnectionPrefs) error {
	if !prefs.PasswordInKeychain || r.keychain == nil {
		return nil
	}
	if err := r.keychain.Delete(keychainAccount(target, prefs.DBUser)); err != nil {
		return fmt.Errorf("%w: failed to forget the password: %w", ErrKeychain, err)
	}
	return nil
}
