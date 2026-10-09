package i18n

import (
	"fmt"
	"strings"
)

// Error is an error built from a catalog key: Error() is English, for agents and logs; Localize
// renders it in the user's language.
type Error struct {
	en   error
	key  string
	args []any
}

// NewError is fmt.Errorf with the English text of key as format; %w keeps wrapping.
func NewError(key string, args ...any) error {
	return &Error{key: key, args: args, en: fmt.Errorf(english(key), args...)}
}

func (e *Error) Error() string { return e.en.Error() }

// Unwrap returns the errors wrapped with %w.
func (e *Error) Unwrap() []error {
	switch u := e.en.(type) {
	case interface{ Unwrap() []error }:
		return u.Unwrap()
	case interface{ Unwrap() error }:
		if inner := u.Unwrap(); inner != nil {
			return []error{inner}
		}
	}
	return nil
}

// localized renders e in the current language, its error args localized too.
func (e *Error) localized() string {
	return fmt.Errorf(T(e.key), localizedErrors(e.args)...).Error()
}

// Localize renders err in the current language: each *Error of its chain is translated within the
// text of the error that wraps it, through untranslated wrappers.
func Localize(err error) string {
	if err == nil {
		return ""
	}
	if Language() == "en" {
		return err.Error()
	}
	return localize(err)
}

func localize(err error) string {
	msg := err.Error()
	switch e := err.(type) {
	case *Error:
		return e.localized()
	case interface{ Unwrap() []error }:
		// Joined or multi-%w: the wrapped texts appear left to right.
		cursor := 0
		for _, inner := range e.Unwrap() {
			english := inner.Error()
			at := strings.Index(msg[cursor:], english)
			if at < 0 {
				continue
			}
			local := localize(inner)
			msg = msg[:cursor+at] + local + msg[cursor+at+len(english):]
			cursor += at + len(local)
		}
	case interface{ Unwrap() error }:
		// A single %w usually ends the text.
		if inner := e.Unwrap(); inner != nil {
			if at := strings.LastIndex(msg, inner.Error()); at >= 0 {
				msg = msg[:at] + localize(inner) + msg[at+len(inner.Error()):]
			}
		}
	}
	return msg
}

// LocalizeArgs returns args with each error replaced by its Localize text, for formatting.
func LocalizeArgs(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		if err, ok := arg.(error); ok {
			out[i] = Localize(err)
			continue
		}
		out[i] = arg
	}
	return out
}

// localizedError formats as the Localize text of err and still wraps it, for %w.
type localizedError struct{ err error }

func (l localizedError) Error() string { return Localize(l.err) }
func (l localizedError) Unwrap() error { return l.err }

func localizedErrors(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		if err, ok := arg.(error); ok {
			out[i] = localizedError{err}
			continue
		}
		out[i] = arg
	}
	return out
}

// english returns the English text of key, else the key.
func english(key string) string {
	if text, ok := catalogs["en"][key]; ok {
		return text
	}
	return key
}
