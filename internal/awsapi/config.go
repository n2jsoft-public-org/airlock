// Package awsapi builds the AWS SDK configuration shared by every AWS call.
package awsapi

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

// Configs hands out one aws.Config per profile and region.
type Configs struct {
	cache map[string]entry
	mu    sync.Mutex
}

type entry struct {
	files [2]time.Time // modification times of the shared config and credentials files at load
	cfg   aws.Config
}

func NewConfigs() *Configs {
	return &Configs{cache: map[string]entry{}}
}

// sharedFilePaths returns the shared config and credentials files, as the SDK resolves them.
func sharedFilePaths() [2]string {
	cfgFile, credFile := os.Getenv("AWS_CONFIG_FILE"), os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if cfgFile == "" {
		cfgFile = config.DefaultSharedConfigFilename()
	}
	if credFile == "" {
		credFile = config.DefaultSharedCredentialsFilename()
	}
	return [2]string{cfgFile, credFile}
}

// sharedFilesStamp returns the modification times of the shared config and credentials files;
// a missing file counts as the zero time.
func sharedFilesStamp() [2]time.Time {
	var stamp [2]time.Time
	for i, path := range sharedFilePaths() {
		// #nosec G703 -- the SDK's own file locations.
		if info, err := os.Stat(path); err == nil {
			stamp[i] = info.ModTime()
		}
	}
	return stamp
}

// Get returns the cached config so the SDK credentials cache (SSO, assume-role, MFA)
// is shared by all callers for the life of the process; it is loaded again when ~/.aws changed
// since it was loaded.
// Errors are not cached.
func (c *Configs) Get(ctx context.Context, profile, region string) (aws.Config, error) {
	key := profile + "\x00" + region
	c.mu.Lock()
	defer c.mu.Unlock()
	stamp := sharedFilesStamp()
	if e, ok := c.cache[key]; ok && e.files == stamp {
		return e.cfg, nil
	}
	// No code prompt here: an MFA profile reads the codes typed by SignInMFA from the shared cache.
	cfg, err := load(ctx, profile, region, nil)
	if err != nil {
		return aws.Config{}, err
	}
	withCachedRole(&cfg)
	c.cache[key] = entry{cfg: cfg, files: stamp}
	return cfg, nil
}
