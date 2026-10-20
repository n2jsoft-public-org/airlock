package jumphost

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// apiConfig is the SDK configuration of cfg for discovering targets: a failure to read its
// credentials asks to sign in again (see awsCallError).
func (r *Runner) apiConfig(ctx context.Context, cfg awsConfig) (aws.Config, error) {
	awsCfg, err := r.sdkConfig(ctx, cfg)
	if err != nil {
		return aws.Config{}, err
	}
	awsCfg.Credentials = mcpserve.SignInCredentials(awsCfg.Credentials)
	return awsCfg, nil
}

// awsCallError is the failure of an AWS call as users read it: AWS out of reach as its network
// cause, never as expired credentials; else as mcpserve.AWSError words it, without the SDK's
// operation chain.
func awsCallError(err error) error {
	if awsapi.Unreachable(err) {
		// The credentials error around an unreachable assume-role or SSO call would ask to sign in.
		if sendErr, ok := errors.AsType[*smithyhttp.RequestSendError](err); ok {
			return sendErr.Err
		}
		return err
	}
	return mcpserve.AWSError(err)
}

// rdsInstances lists every RDS instance of cfg.
func (r *Runner) rdsInstances(ctx context.Context, cfg awsConfig) ([]rdstypes.DBInstance, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var instances []rdstypes.DBInstance
	pages := rds.NewDescribeDBInstancesPaginator(rds.NewFromConfig(awsCfg), &rds.DescribeDBInstancesInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, i18n.NewError("jumphost.runner.rds_list", awsCallError(err))
		}
		instances = append(instances, page.DBInstances...)
	}
	return instances, nil
}

// describeDBInstance describes the RDS instance name of cfg; the zero instance when AWS lists none.
func (r *Runner) describeDBInstance(ctx context.Context, cfg awsConfig, name string) (rdstypes.DBInstance, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return rdstypes.DBInstance{}, err
	}
	out, err := rds.NewFromConfig(awsCfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(name)})
	if err != nil {
		return rdstypes.DBInstance{}, i18n.NewError("jumphost.runner.rds_describe", name, awsCallError(err))
	}
	if len(out.DBInstances) == 0 {
		return rdstypes.DBInstance{}, nil
	}
	return out.DBInstances[0], nil
}

// rdsEndpoint is the address and port of instance; empty and 0 while it has no endpoint.
func rdsEndpoint(instance *rdstypes.DBInstance) (address string, port int) {
	if instance.Endpoint == nil {
		return "", 0
	}
	return aws.ToString(instance.Endpoint.Address), int(aws.ToInt32(instance.Endpoint.Port))
}

// rdsMasterSecretARN is the ARN of the master user secret of instance; empty without one.
func rdsMasterSecretARN(instance *rdstypes.DBInstance) string {
	if instance.MasterUserSecret == nil {
		return ""
	}
	return aws.ToString(instance.MasterUserSecret.SecretArn)
}

// eksClusterNames lists the name of every EKS cluster of cfg.
func (r *Runner) eksClusterNames(ctx context.Context, cfg awsConfig) ([]string, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var names []string
	pages := eks.NewListClustersPaginator(eks.NewFromConfig(awsCfg), &eks.ListClustersInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, i18n.NewError("jumphost.runner.eks_list", awsCallError(err))
		}
		names = append(names, page.Clusters...)
	}
	return names, nil
}

// describeEKSCluster describes the EKS cluster name of cfg; the zero cluster when AWS returns none.
func (r *Runner) describeEKSCluster(ctx context.Context, cfg awsConfig, name string) (ekstypes.Cluster, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return ekstypes.Cluster{}, err
	}
	out, err := eks.NewFromConfig(awsCfg).DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		return ekstypes.Cluster{}, i18n.NewError("jumphost.runner.eks_describe", name, awsCallError(err))
	}
	if out.Cluster == nil {
		return ekstypes.Cluster{}, nil
	}
	return *out.Cluster, nil
}

// cachePager is one of the ElastiCache paginators, returning pages of type O.
type cachePager[O any] interface {
	HasMorePages() bool
	NextPage(ctx context.Context, optFns ...func(*elasticache.Options)) (O, error)
}

// cachePages collects the items of every page of pages; errKey words a failure.
func cachePages[O, T any](ctx context.Context, pages cachePager[O], items func(O) []T, errKey string) ([]T, error) {
	var all []T
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, i18n.NewError(errKey, awsCallError(err))
		}
		all = append(all, items(page)...)
	}
	return all, nil
}

func (r *Runner) elastiCache(ctx context.Context, cfg awsConfig) (*elasticache.Client, error) {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return elasticache.NewFromConfig(awsCfg), nil
}

// optionalID is the identifier filter of a describe: nil describes them all.
func optionalID(id string) *string {
	if id == "" {
		return nil
	}
	return aws.String(id)
}

// replicationGroups describes the ElastiCache replication group id of cfg, or every one without id.
func (r *Runner) replicationGroups(ctx context.Context, cfg awsConfig, id string) ([]ectypes.ReplicationGroup, error) {
	client, err := r.elastiCache(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pages := elasticache.NewDescribeReplicationGroupsPaginator(client, &elasticache.DescribeReplicationGroupsInput{ReplicationGroupId: optionalID(id)})
	return cachePages(ctx, pages, func(o *elasticache.DescribeReplicationGroupsOutput) []ectypes.ReplicationGroup {
		return o.ReplicationGroups
	},
		"jumphost.runner.elasticache_replication_groups")
}

// cacheClusters describes, with their nodes, the ElastiCache cluster id of cfg, or every one without id.
func (r *Runner) cacheClusters(ctx context.Context, cfg awsConfig, id string) ([]ectypes.CacheCluster, error) {
	client, err := r.elastiCache(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pages := elasticache.NewDescribeCacheClustersPaginator(client, &elasticache.DescribeCacheClustersInput{
		CacheClusterId: optionalID(id), ShowCacheNodeInfo: aws.Bool(true),
	})
	return cachePages(ctx, pages, func(o *elasticache.DescribeCacheClustersOutput) []ectypes.CacheCluster { return o.CacheClusters },
		"jumphost.runner.elasticache_cache_clusters")
}

// serverlessCaches describes the ElastiCache serverless cache name of cfg, or every one without name.
func (r *Runner) serverlessCaches(ctx context.Context, cfg awsConfig, name string) ([]ectypes.ServerlessCache, error) {
	client, err := r.elastiCache(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pages := elasticache.NewDescribeServerlessCachesPaginator(client, &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: optionalID(name)})
	return cachePages(ctx, pages, func(o *elasticache.DescribeServerlessCachesOutput) []ectypes.ServerlessCache {
		return o.ServerlessCaches
	},
		"jumphost.runner.elasticache_serverless_caches")
}

// cacheUserGroups lists every ElastiCache RBAC user group of cfg.
func (r *Runner) cacheUserGroups(ctx context.Context, cfg awsConfig) ([]ectypes.UserGroup, error) {
	client, err := r.elastiCache(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pages := elasticache.NewDescribeUserGroupsPaginator(client, &elasticache.DescribeUserGroupsInput{})
	return cachePages(ctx, pages, func(o *elasticache.DescribeUserGroupsOutput) []ectypes.UserGroup { return o.UserGroups },
		"jumphost.cache.elasticache_user_groups")
}

// cacheUsers lists every ElastiCache RBAC user of cfg.
func (r *Runner) cacheUsers(ctx context.Context, cfg awsConfig) ([]ectypes.User, error) {
	client, err := r.elastiCache(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pages := elasticache.NewDescribeUsersPaginator(client, &elasticache.DescribeUsersInput{})
	return cachePages(ctx, pages, func(o *elasticache.DescribeUsersOutput) []ectypes.User { return o.Users },
		"jumphost.cache.elasticache_users")
}

// endpointOf is the address and port of endpoint; empty and 0 without one.
func endpointOf(endpoint *ectypes.Endpoint) (address string, port int) {
	if endpoint == nil {
		return "", 0
	}
	return aws.ToString(endpoint.Address), int(aws.ToInt32(endpoint.Port))
}

// primaryEndpoint is the primary endpoint of the first node group of group; nil without one.
func primaryEndpoint(group *ectypes.ReplicationGroup) *ectypes.Endpoint {
	if len(group.NodeGroups) == 0 {
		return nil
	}
	return group.NodeGroups[0].PrimaryEndpoint
}

// groupEndpoint is where group is reached: its primary endpoint, else (cluster mode has none) its
// configuration endpoint, which reaches one shard.
func groupEndpoint(group *ectypes.ReplicationGroup) (address string, port int) {
	if address, port = endpointOf(primaryEndpoint(group)); address != "" {
		return address, port
	}
	return endpointOf(group.ConfigurationEndpoint)
}

// nodeEndpoint is the endpoint of the first node of cluster; nil without one.
func nodeEndpoint(cluster *ectypes.CacheCluster) *ectypes.Endpoint {
	if len(cluster.CacheNodes) == 0 {
		return nil
	}
	return cluster.CacheNodes[0].Endpoint
}
