package i18n

import (
	"context"
	"os/exec"
	"time"
)

// OSLocale returns the macOS UI language (not the region format: AppleLocale can be
// en_US@rg=frzzzz on an English UI).
func OSLocale() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return firstAppleLanguage(string(out))
}
