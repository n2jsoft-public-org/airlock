package cli

import (
	"context"
	"os"
	"time"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/selfupdate"
	"github.com/n2jsoft/airlock/internal/version"
)

// noUpdateCheckEnv, set to any value, turns off the background release check and its notice.
const noUpdateCheckEnv = "AIRLOCK_NO_UPDATE_CHECK"

// updateCheckTimeout bounds the background release check.
const updateCheckTimeout = 10 * time.Second

// updateNotice returns the newer release the last check found ("" when none) and whether that
// check must be refreshed. It never calls GitHub.
func updateNotice(env *updateEnv, now time.Time) (latest string, stale bool) {
	if os.Getenv(noUpdateCheckEnv) != "" || env.current == version.Unknown {
		return "", false
	}
	path, err := env.cacheFile()
	if err != nil {
		return "", false
	}
	cache := selfupdate.LoadCache(path)
	return cache.Available(env.current), cache.Stale(env.current, now)
}

// refreshUpdateCheck checks the latest release and saves the result for the next launch.
func refreshUpdateCheck(ctx context.Context, env *updateEnv) error {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	path, err := env.cacheFile()
	if err != nil {
		return err
	}
	updater, err := env.updater(ctx)
	if err != nil {
		return err
	}
	result, err := updater.Check(ctx)
	if err != nil {
		return err
	}
	return selfupdate.SaveCache(path, result.CacheAt(time.Now()))
}

func wizardUpdateNotice(latest string) string {
	return i18n.Format("cli.update_notice.wizard", latest)
}

func stderrUpdateNotice(latest string) string {
	return i18n.Format("cli.update_notice.stderr", selfupdate.PublicURL, latest)
}
