package jumphost

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// ListAWSProfiles returns available AWS CLI profiles.
func (r *Runner) ListAWSProfiles(ctx context.Context) ([]string, error) {
	summaries, err := r.ListAWSProfileSummaries(ctx)
	if err != nil {
		return nil, err
	}

	profiles := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		name := strings.TrimSpace(summary.Name)
		if name == "" {
			continue
		}
		profiles = append(profiles, name)
	}

	return profiles, nil
}

// ListAWSProfileNames returns the AWS profile names, sorted, from the shared files: fast
// enough for shell completion, unlike ListAWSProfileSummaries.
func (r *Runner) ListAWSProfileNames() ([]string, error) {
	return r.listProfileNames()
}

// listProfileNames returns the sorted AWS profile names, without the default fallback.
func (r *Runner) listProfileNames() ([]string, error) {
	profiles, err := awsapi.Profiles()
	if err != nil {
		return nil, i18n.NewError("jumphost.runner.failed_to_read_profiles", err)
	}
	return profiles, nil
}

// ListAWSProfileSummaries returns available AWS profiles with display details.
func (r *Runner) ListAWSProfileSummaries(ctx context.Context) ([]AWSProfileSummary, error) {
	profiles, err := r.listProfileNames()
	if err != nil {
		return nil, err
	}

	if len(profiles) == 0 {
		profiles = []string{defaultAwsProfile}
	}

	summaries := make([]AWSProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		description := "region n/a"
		if region := awsapi.ProfileRegion(ctx, profile); region != "" {
			description = fmt.Sprintf("region %s", region)
		}

		summaries = append(summaries, AWSProfileSummary{
			Name:        profile,
			Description: description,
		})
	}

	return summaries, nil
}

// ListResourceNames returns available resource names for the given type.
func (r *Runner) ListResourceNames(ctx context.Context, options *Options) ([]string, error) {
	resources, err := r.ListResourceSummaries(ctx, options)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resources))
	for _, resource := range resources {
		name := strings.TrimSpace(resource.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}

	return names, nil
}

// listResources lists the targets of resourceType with the resolved cfg.
func (r *Runner) listResources(ctx context.Context, cfg awsConfig, resourceType string) ([]ResourceSummary, error) {
	switch resourceType {
	case resourceDynamoDB:
		return r.dynamoDBTargets(ctx, cfg)
	case resourceS3:
		return r.s3Targets(ctx, cfg)
	case resourceLogs:
		return r.logsTargets(ctx, cfg)
	case resourceRDS:
		instances, err := r.rdsInstances(ctx, cfg)
		if err != nil {
			return nil, err
		}

		resources := make([]ResourceSummary, 0, len(instances))
		for idx := range instances {
			instance := &instances[idx]
			name := strings.TrimSpace(aws.ToString(instance.DBInstanceIdentifier))
			if name == "" {
				continue
			}

			engine := aws.ToString(instance.Engine)
			host, port := rdsEndpoint(instance)
			resources = append(resources, ResourceSummary{
				Name: name,
				Description: formatRDSResourceDescription(engine, aws.ToString(instance.EngineVersion), host, port,
					int(aws.ToInt32(instance.AllocatedStorage)), aws.ToString(instance.DBInstanceStatus)),
				IAMAuth: aws.ToBool(instance.IAMDatabaseAuthenticationEnabled),
				Engine:  engine,
			})
		}

		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})

		return resources, nil
	case resourceEKS:
		clusters, err := r.eksClusterNames(ctx, cfg)
		if err != nil {
			return nil, err
		}

		resources := make([]ResourceSummary, 0, len(clusters))
		for _, cluster := range clusters {
			name := strings.TrimSpace(cluster)
			if name == "" {
				continue
			}
			resources = append(resources, ResourceSummary{Name: name})
		}
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})
		return resources, nil
	case resourceElastiCache:
		resources, err := r.listElastiCacheResources(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return resources, nil
	default:
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type", resourceType)
	}
}

// ListResourceSummaries returns available resources with display details for the given type.
func (r *Runner) ListResourceSummaries(ctx context.Context, options *Options) ([]ResourceSummary, error) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if err := r.checkModule(resourceType); err != nil {
		return nil, err
	}
	if !supportedResourceType(resourceType) {
		return nil, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}

	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if resourceType == resourceForward {
		// Nothing to discover: the target is the host:port asked for.
		if _, _, err = parseForwardTarget(options.Name); err != nil {
			return nil, err
		}
		return []ResourceSummary{{Name: strings.TrimSpace(options.Name), Type: resourceForward}}, nil
	}

	resources, err := r.listResources(ctx, cfg, resourceType)
	if err != nil {
		return nil, err
	}
	return withType(resources, resourceType), nil
}

func formatElastiCacheResourceDescription(host string, port int, status string) string {
	parts := make([]string, 0, 3)

	host = strings.TrimSpace(host)
	if host != "" {
		if port > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", host, port))
		} else {
			parts = append(parts, host)
		}
	}

	status = strings.TrimSpace(status)
	if status != "" {
		parts = append(parts, status)
	}

	return strings.Join(parts, " | ")
}

func formatRDSResourceDescription(engine, engineVersion, host string, port, sizeGiB int, status string) string {
	parts := make([]string, 0, 4)

	engine = strings.TrimSpace(engine)
	engineVersion = strings.TrimSpace(engineVersion)
	if engine != "" && engineVersion != "" {
		parts = append(parts, fmt.Sprintf("%s %s", engine, engineVersion))
	} else if engine != "" {
		parts = append(parts, engine)
	}

	host = strings.TrimSpace(host)
	if host != "" {
		if port > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", host, port))
		} else {
			parts = append(parts, host)
		}
	}

	if sizeGiB > 0 {
		parts = append(parts, fmt.Sprintf("%d GiB", sizeGiB))
	} else {
		parts = append(parts, "size n/a")
	}

	status = strings.TrimSpace(status)
	if status != "" {
		parts = append(parts, status)
	}

	return strings.Join(parts, " | ")
}
