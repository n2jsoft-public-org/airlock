package awsapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
)

// Profiles returns the sorted, de-duplicated profile names of the shared config file
// ([default], [profile x]) and of the credentials file ([x]); a missing file counts as empty.
func Profiles() ([]string, error) {
	paths := sharedFilePaths()
	set := map[string]struct{}{}
	for i, path := range paths {
		if err := readProfileSections(path, i == 0, set); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// readProfileSections adds the profile names found in the section headers of path to set.
// The config file prefixes them with "profile " (except default) and also holds sso-session
// and services sections, which are not profiles.
func readProfileSections(path string, isConfig bool, set map[string]struct{}) error {
	data, err := os.ReadFile(path) // #nosec G304 G703 -- the SDK's own file locations.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		name := strings.TrimSpace(line[1:end])
		if isConfig && name != "default" {
			rest, ok := strings.CutPrefix(name, "profile ")
			if !ok {
				continue
			}
			name = strings.TrimSpace(rest)
		}
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return nil
}

// ProfileRegion returns the region set for profile in the shared files; "" when there is none or
// the profile is unknown.
func ProfileRegion(ctx context.Context, profile string) string {
	shared, err := sharedProfile(ctx, profile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(shared.Region)
}

// SourceProfile returns the source_profile of profile in the shared files; "" when there is none
// or the profile is unknown.
func SourceProfile(ctx context.Context, profile string) string {
	shared, err := sharedProfile(ctx, profile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(shared.SourceProfileName)
}

// sharedProfile reads profile from the shared files. The files are named explicitly:
// LoadSharedConfigProfile ignores AWS_CONFIG_FILE otherwise.
func sharedProfile(ctx context.Context, profile string) (config.SharedConfig, error) {
	paths := sharedFilePaths()
	return config.LoadSharedConfigProfile(ctx, profile, func(o *config.LoadSharedConfigOptions) {
		o.ConfigFiles = paths[:1]
		o.CredentialsFiles = paths[1:]
	})
}
