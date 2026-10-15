package jumphost

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// searchableResourceTypes are the types SearchResources looks through, in display order.
var searchableResourceTypes = []string{resourceRDS, resourceEKS, resourceElastiCache, resourceDynamoDB}

// SearchResources lists the targets of every type in the profile and region of options. The
// region and identity are resolved once, then each type is listed concurrently: a type whose
// listing fails is reported in failures, keyed by type, and the others still return. err is set
// only when nothing could be listed at all. options.AwsRegion is set to the region searched.
func (r *Runner) SearchResources(ctx context.Context, options *Options) ([]ResourceSummary, map[string]error, error) {
	if err := requireAWSCLI(); err != nil {
		return nil, nil, err
	}
	// One identity check up front: concurrent ones could each start an SSO login.
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, nil, err
	}
	options.AwsRegion = cfg.Region
	types := slices.DeleteFunc(slices.Clone(searchableResourceTypes), r.ModuleDisabled)
	resources, failures := searchResources(types, func(resourceType string) ([]ResourceSummary, error) {
		return r.listResources(ctx, cfg, resourceType)
	})
	return resources, failures, nil
}

// AccountMismatchError refuses a profile that reaches another AWS account than the one a
// remembered target was saved in: same-named targets are common across accounts.
type AccountMismatchError struct {
	Profile        string
	ProfileAccount string
	EntryAccount   string
}

func (e *AccountMismatchError) Error() string {
	return fmt.Sprintf("profile %s is account %s, this entry is account %s", e.Profile, e.ProfileAccount, e.EntryAccount)
}

// FindResource returns the target named options.Name among the targets of options.ResourceType,
// or an error when it no longer exists in that profile and region. A non-empty account must be
// the one the profile reaches, else an *AccountMismatchError is returned.
func (r *Runner) FindResource(ctx context.Context, options *Options, account string) (ResourceSummary, error) {
	resources, err := r.ListResourceSummaries(ctx, options)
	if err != nil {
		return ResourceSummary{}, err
	}
	// The listing ran the identity check, which remembered the profile's account.
	profile := resolveProfileName(options.AwsProfile)
	if err := checkAccount(profile, r.accountFor(ctx, profile), account); err != nil {
		return ResourceSummary{}, err
	}
	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, resolveProfileName(options.AwsProfile))
	}
	return findResourceByName(resources, options.Name, options.ResourceType, region)
}

// searchResources runs list for every type concurrently and merges the results, sorted by name
// then type. Failed types are returned in failures.
func searchResources(types []string, list func(resourceType string) ([]ResourceSummary, error)) (merged []ResourceSummary, failures map[string]error) {
	type result struct {
		err       error
		resources []ResourceSummary
	}
	results := make([]result, len(types))
	var wg sync.WaitGroup
	for idx, resourceType := range types {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resources, err := list(resourceType)
			results[idx] = result{resources: resources, err: err}
		}()
	}
	wg.Wait()

	merged = []ResourceSummary{}
	failures = map[string]error{}
	for idx, res := range results {
		if res.err != nil {
			failures[types[idx]] = res.err
			continue
		}
		merged = append(merged, withType(res.resources, types[idx])...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Name != merged[j].Name {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].Type < merged[j].Type
	})
	return merged, failures
}

// findResourceByName returns the resource called name, else an error naming type and region.
func findResourceByName(resources []ResourceSummary, name, resourceType, region string) (ResourceSummary, error) {
	name = strings.TrimSpace(name)
	for _, resource := range resources {
		if resource.Name == name {
			return resource, nil
		}
	}
	return ResourceSummary{}, i18n.NewError("jumphost.search.target_no_longer_exists", strings.ToUpper(resourceType), name, region)
}

// checkAccount accepts profileAccount when it is want, or when no account is expected.
func checkAccount(profile, profileAccount, want string) error {
	want = strings.TrimSpace(want)
	profileAccount = strings.TrimSpace(profileAccount)
	switch {
	case want == "" || profileAccount == want:
		return nil
	case profileAccount == "":
		return i18n.NewError("jumphost.search.cannot_confirm_that_profile", profile, want)
	default:
		return &AccountMismatchError{Profile: profile, ProfileAccount: profileAccount, EntryAccount: want}
	}
}

// withType sets the type of every resource.
func withType(resources []ResourceSummary, resourceType string) []ResourceSummary {
	for idx := range resources {
		resources[idx].Type = resourceType
	}
	return resources
}
