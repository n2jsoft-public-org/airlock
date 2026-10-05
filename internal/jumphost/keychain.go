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
	return target.key() + "/" + keychainUser(target.Type, user)
}

// keychainUser is the user keying a password: caches sign an empty user in as the default user
// (AUTH token), so their password is remembered under it.
func keychainUser(resourceType, user string) string {
	if resourceType == resourceElastiCache {
		return orCacheDefaultUser(user)
	}
	return strings.TrimSpace(user)
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
// The password of a direct database is only returned while its remembered choices flag it for that
// user: an entry left in the keychain under its name (by a definition replaced since, say) must
// never reach the server the name designates now.
func (r *Runner) LoadKeychainPassword(ctx context.Context, options Options) (string, bool) { //nolint:gocritic // same by-value Options as LoadConnectionPrefs, its sibling
	if !r.KeychainAvailable() {
		return "", false
	}
	target, ok := r.prefsTarget(ctx, &options)
	if !ok || keychainUser(target.Type, options.DBUser) == "" {
		return "", false
	}
	if target.Type == resourceDirect && !r.directPasswordFlagged(target, options.DBUser) {
		return "", false
	}
	password, err := r.keychain.Get(keychainAccount(target, options.DBUser))
	if err != nil || password == "" {
		return "", false
	}
	return password, true
}

// ForgetDirectPassword deletes the keychain password of user remembered under the direct database
// name, for an entry whose choices cannot be read (an invalid one) and that is about to be removed
// or replaced: the password must not wait in the keychain for the next database of that name.
// flagged tells that the entry reports such a password; an unflagged one is still deleted while
// the keychain is available. A missing password is not an error.
func (r *Runner) ForgetDirectPassword(name, user string, flagged bool) error {
	ref := DirectTargetRef(name)
	if r.keychain == nil || strings.TrimSpace(ref.Name) == "" || keychainUser(ref.Type, user) == "" {
		return nil
	}
	if !flagged && !r.KeychainAvailable() {
		return nil
	}
	if err := r.keychain.Delete(keychainAccount(ref, user)); err != nil {
		return fmt.Errorf("%w: failed to forget the password: %w", ErrKeychain, err)
	}
	return nil
}

// directPasswordFlagged reports whether the remembered choices of the direct database target flag
// a password in the keychain for user.
func (r *Runner) directPasswordFlagged(target TargetRef, user string) bool {
	prefs, ok := r.prefs.Load(target)
	return ok && prefs.PasswordInKeychain && strings.TrimSpace(prefs.DBUser) == strings.TrimSpace(user)
}

// syncKeychain stores or deletes the password of next, deleting the entry of the previous user
// when it changed. It sets next.PasswordInKeychain to whether a password is now remembered: an
// entry that could not be deleted stays flagged (with its user), so a later save or forgetting the
// connection still removes it.
func (r *Runner) syncKeychain(target TargetRef, previous, next *ConnectionPrefs, options *Options) error {
	remember := options.RememberPassword && next.SignIn == SignInPassword && options.DBPassword != "" && keychainUser(target.Type, next.DBUser) != ""
	next.PasswordInKeychain = false
	remembered := previous != nil && previous.PasswordInKeychain
	if !r.KeychainAvailable() {
		if remembered {
			next.PasswordInKeychain, next.DBUser = true, previous.DBUser
		}
		return nil
	}

	var errs []error
	newAccount := ""
	if remember {
		newAccount = keychainAccount(target, next.DBUser)
	}
	sameAccount := remembered && keychainAccount(target, previous.DBUser) == newAccount
	if remembered && !sameAccount {
		if err := r.keychain.Delete(keychainAccount(target, previous.DBUser)); err != nil {
			errs = append(errs, fmt.Errorf("failed to forget the previous password: %w", err))
		}
	}
	if !remember {
		// The previous entry, if any, was deleted above (newAccount is empty).
		return joinKeychainErrors(errs)
	}
	if err := r.keychain.Set(newAccount, options.DBPassword); err != nil {
		errs = append(errs, fmt.Errorf("the password was not remembered: %w", err))
		// The entry of the same user is still there, with the password it had.
		next.PasswordInKeychain = sameAccount
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

// forgetKeychainFunc deletes the keychain entry account once the database rejected its password
// rejected, unless the entry holds another password by then (saved by a later session); nil when
// there is nothing to forget. It is best effort: an entry left behind only makes the next attempt
// use a rejected password.
func (r *Runner) forgetKeychainFunc(account, rejected string) func() {
	if account == "" || r.keychain == nil {
		return nil
	}
	return func() {
		if stored, err := r.keychain.Get(account); err != nil || stored != rejected {
			return // absent, unreadable, or replaced by a later session
		}
		if err := r.keychain.Delete(account); err != nil {
			return // best effort, see above
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
