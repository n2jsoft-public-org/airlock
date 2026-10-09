//go:build !darwin && !windows

package i18n

// OSLocale returns nothing: on Linux the LANG family already is the system language.
func OSLocale() string { return "" }
