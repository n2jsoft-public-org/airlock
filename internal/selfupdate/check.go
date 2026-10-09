package selfupdate

import (
	"context"
	"fmt"
)

// Updater checks and installs the releases of Client's repository for the running build.
type Updater struct {
	Client *Client
	// Current is the running version, as version.String gives it.
	Current string
	GOOS    string
	GOARCH  string
}

// Result is the outcome of a check.
type Result struct {
	Current string
	Latest  string
	Status  Status
	Release Release
}

// Check finds the latest release and tells how the running build compares with it.
func (u Updater) Check(ctx context.Context) (Result, error) {
	release, err := u.Client.LatestRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{Current: u.Current, Latest: release.Tag, Status: Decide(u.Current, release.Tag), Release: release}, nil
}

// Install downloads the release of r built for u's platform, checks it against the release's
// checksums.txt and swaps it for target. Nothing on disk changes until the download is verified.
func (u Updater) Install(ctx context.Context, r *Result, target string) error {
	name := AssetName(r.Latest, u.GOOS, u.GOARCH)
	archiveAsset, ok := findAsset(&r.Release, name)
	if !ok {
		return fmt.Errorf("release %s has no build for %s/%s", r.Latest, u.GOOS, u.GOARCH)
	}
	checksumsAsset, ok := findAsset(&r.Release, ChecksumsName)
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt: nothing was changed", r.Latest)
	}
	archive, err := u.Client.Download(ctx, archiveAsset)
	if err != nil {
		return err
	}
	checksums, err := u.Client.Download(ctx, checksumsAsset)
	if err != nil {
		return err
	}
	if err := VerifyChecksum(archive, checksums, name); err != nil { //nolint:govet // shadowing in if condition is idiomatic
		return err
	}
	binary, err := ExtractBinary(archive, u.GOOS)
	if err != nil {
		return err
	}
	return Replace(target, binary)
}

func findAsset(release *Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}
