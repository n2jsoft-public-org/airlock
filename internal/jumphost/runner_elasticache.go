package jumphost

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

func (r *Runner) prepareElastiCache(ctx context.Context, cfg awsConfig, options *Options) (preparedTarget, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		resources, err := r.listElastiCacheResources(ctx, cfg)
		if err != nil {
			return preparedTarget{}, err
		}
		if len(resources) == 0 {
			return preparedTarget{}, i18n.NewError("jumphost.runner.no_elasticache_cluster_found")
		}
		if len(resources) == 1 {
			name = resources[0].Name
		} else {
			names := make([]string, 0, len(resources))
			for _, r := range resources {
				names = append(names, r.Name)
			}
			selectedName, err := r.selectName("ElastiCache clusters", names, "Please specify the cluster name to connect to")
			if err != nil {
				return preparedTarget{}, err
			}
			if selectedName == "" {
				return preparedTarget{}, i18n.NewError("jumphost.runner.elasticache_cluster_name_is")
			}
			name = selectedName
		}
	}

	// Try replication group first.
	if groups, err := r.replicationGroups(ctx, cfg, name); err == nil && len(groups) > 0 {
		group := &groups[0]
		groupID := aws.ToString(group.ReplicationGroupId)
		if address, port := groupEndpoint(group); address != "" {
			return r.prepareCacheTarget(ctx, cfg, options, groupID, address, port, &ideTarget{
				Engine:    elastiCacheEngine(aws.ToString(group.Engine)),
				CacheAuth: cacheAuthMode(aws.ToBool(group.AuthTokenEnabled), group.UserGroupIds),
				CacheTLS:  aws.ToBool(group.TransitEncryptionEnabled),
			}, func(localPort int) string {
				return fmt.Sprintf("ElastiCache %s %s:%d -> localhost:%d", groupID, address, port, localPort)
			})
		}
	}

	// Fall back to standalone cache cluster.
	if clusters, err := r.cacheClusters(ctx, cfg, name); err == nil && len(clusters) > 0 {
		cluster := &clusters[0]
		if address, port := endpointOf(nodeEndpoint(cluster)); address != "" {
			clusterID, engine := aws.ToString(cluster.CacheClusterId), aws.ToString(cluster.Engine)
			target := &ideTarget{
				Engine:    elastiCacheEngine(engine),
				CacheAuth: cacheAuthMode(aws.ToBool(cluster.AuthTokenEnabled), nil),
				CacheTLS:  aws.ToBool(cluster.TransitEncryptionEnabled),
			}
			if groupID := aws.ToString(cluster.ReplicationGroupId); groupID != "" {
				// A node of a replication group signs in as its group does.
				if groupErr := r.applyNodeReplicationGroup(ctx, cfg, groupID, target); groupErr != nil {
					return preparedTarget{}, i18n.NewError("jumphost.runner.failed_to_describe_replication", groupID, clusterID, groupErr)
				}
			}
			return r.prepareCacheTarget(ctx, cfg, options, clusterID, address, port, target, func(localPort int) string {
				return fmt.Sprintf("ElastiCache %s (%s %s) %s:%d -> localhost:%d",
					clusterID, engine, aws.ToString(cluster.EngineVersion), address, port, localPort)
			})
		}
	}

	// Fall back to serverless cache.
	caches, err := r.serverlessCaches(ctx, cfg, name)
	if err != nil {
		return preparedTarget{}, i18n.NewError("jumphost.runner.elasticache_cluster_not_found", name, err)
	}
	var sc ectypes.ServerlessCache
	if len(caches) > 0 {
		sc = caches[0]
	}
	address, port := endpointOf(sc.Endpoint)
	if address == "" {
		return preparedTarget{}, i18n.NewError("jumphost.runner.serverless_cache_endpoint_is", name)
	}

	var userGroups []string
	if groupID := aws.ToString(sc.UserGroupId); groupID != "" {
		userGroups = []string{groupID}
	}
	scName, engine := aws.ToString(sc.ServerlessCacheName), aws.ToString(sc.Engine)
	return r.prepareCacheTarget(ctx, cfg, options, scName, address, port, &ideTarget{
		Engine:          elastiCacheEngine(engine),
		CacheAuth:       cacheAuthMode(false, userGroups),
		CacheTLS:        true,
		CacheServerless: true,
	}, func(localPort int) string {
		return fmt.Sprintf("ElastiCache serverless %s (%s %s) %s:%d -> localhost:%d",
			scName, engine, aws.ToString(sc.MajorEngineVersion), address, port, localPort)
	})
}

// elastiCacheEngine defaults to redis when AWS does not report the engine.
func elastiCacheEngine(engine string) string {
	if strings.TrimSpace(engine) == "" {
		return "redis"
	}
	return engine
}

func (r *Runner) listElastiCacheResources(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	var resources []ResourceSummary

	// Replication groups (Redis/Valkey with replication).
	groups, rgErr := r.replicationGroups(ctx, cfg, "")
	for i := range groups {
		g := &groups[i]
		name := strings.TrimSpace(aws.ToString(g.ReplicationGroupId))
		if name == "" {
			continue
		}
		host, port := groupEndpoint(g)
		resources = append(resources, ResourceSummary{
			Name:        name,
			Description: formatElastiCacheResourceDescription(host, port, aws.ToString(g.Status)),
			Engine:      elastiCacheEngine(aws.ToString(g.Engine)),
			CacheAuth:   cacheAuthMode(aws.ToBool(g.AuthTokenEnabled), g.UserGroupIds),
		})
	}

	// Standalone cache clusters (no replication group — includes single-node Valkey/Redis).
	clusters, clErr := r.cacheClusters(ctx, cfg, "")
	for i := range clusters {
		c := &clusters[i]
		name := strings.TrimSpace(aws.ToString(c.CacheClusterId))
		if name == "" || aws.ToString(c.ReplicationGroupId) != "" {
			continue
		}
		host, port := endpointOf(nodeEndpoint(c))
		resources = append(resources, ResourceSummary{
			Name:        name,
			Description: formatElastiCacheResourceDescription(host, port, aws.ToString(c.CacheClusterStatus)),
			Engine:      elastiCacheEngine(aws.ToString(c.Engine)),
			CacheAuth:   cacheAuthMode(aws.ToBool(c.AuthTokenEnabled), nil),
		})
	}

	// Serverless caches (Valkey/Redis serverless — different API entirely).
	serverless, scErr := r.serverlessCaches(ctx, cfg, "")
	for i := range serverless {
		s := &serverless[i]
		name := strings.TrimSpace(aws.ToString(s.ServerlessCacheName))
		if name == "" {
			continue
		}
		var userGroups []string
		if groupID := aws.ToString(s.UserGroupId); groupID != "" {
			userGroups = []string{groupID}
		}
		host, port := endpointOf(s.Endpoint)
		resources = append(resources, ResourceSummary{
			Name: name, Description: formatElastiCacheResourceDescription(host, port, aws.ToString(s.Status)),
			Engine: elastiCacheEngine(aws.ToString(s.Engine)), CacheAuth: cacheAuthMode(false, userGroups),
		})
	}
	// One listing may fail on its own (a region without serverless caches); all failing means
	// the caches could not be listed at all.
	if rgErr != nil && clErr != nil && scErr != nil {
		return nil, i18n.NewError("jumphost.runner.failed_to_list_elasticache", errors.Join(rgErr, clErr, scErr))
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})

	return resources, nil
}
