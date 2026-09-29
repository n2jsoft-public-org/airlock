package jumphost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// searchableResourceTypes are the types SearchResources looks through, in display order.
var searchableResourceTypes = []string{resourceRDS, resourceEKS, resourceElastiCache}

// SearchResources lists the targets of every type in the profile and region of options. The
// region and identity are resolved once, then each type is listed concurrently: a type whose
// listing fails is reported in failures, keyed by type, and the others still return. err is set
// only when nothing could be listed at all.
func (r *Runner) SearchResources(ctx context.Context, options *Options) ([]ResourceSummary, map[string]error, error) {
	if err := requireAWSCLI(); err != nil {
		return nil, nil, err
	}
	// One identity check up front: concurrent ones could each start an SSO login.
	cfg, err := r.resolveAWSConfig(ctx, *options)
	if err != nil {
		return nil, nil, err
	}
	resources, failures := searchResources(searchableResourceTypes, func(resourceType string) ([]ResourceSummary, error) {
		return r.listResources(ctx, cfg, resourceType)
	})
	return resources, failures, nil
}

// FindResource returns the target named options.Name among the targets of options.ResourceType,
// or an error when it no longer exists in that profile and region.
func (r *Runner) FindResource(ctx context.Context, options *Options) (ResourceSummary, error) {
	resources, err := r.ListResourceSummaries(ctx, options)
	if err != nil {
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
	return ResourceSummary{}, fmt.Errorf("%s target %s no longer exists in %s", strings.ToUpper(resourceType), name, region)
}

// withType sets the type of every resource.
func withType(resources []ResourceSummary, resourceType string) []ResourceSummary {
	for idx := range resources {
		resources[idx].Type = resourceType
	}
	return resources
}
