package jumphost

import (
	"context"
	"errors"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// ResolveDefaultLocalPort computes the recommended local port for a selected target:
// the remote port when it is free locally, otherwise the next free port above it; with
// randomLocalPort, a free port chosen by the OS.
func (r *Runner) ResolveDefaultLocalPort(ctx context.Context, options Options) (int, error) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	// An API-only session listens on no port.
	if APIOnly(options.ResourceType) {
		return 0, nil
	}
	if r.randomLocalPort {
		return r.registry.SuggestPort(0)
	}
	port, err := r.resolvePreferredLocalPort(ctx, &options)
	if err != nil {
		return 0, err
	}
	return r.registry.SuggestPort(port)
}

// LoadConnectionPrefs returns the Connect step choices saved for the target of options, if any.
func (r *Runner) LoadConnectionPrefs(ctx context.Context, options Options) (ConnectionPrefs, bool) { //nolint:gocritic // signature fixed by ui.JumphostDiscovery.
	target, ok := r.prefsTarget(ctx, &options)
	if !ok {
		return ConnectionPrefs{}, false
	}
	return r.prefs.Load(target)
}

// SaveConnectionPrefs remembers the choices of options for its target: profile and local port,
// plus user, sign-in method, secret and MCP write access for RDS, ElastiCache and direct databases. The password itself is never
// written to the file: with RememberPassword it goes to the OS keychain. Keychain failures are
// returned wrapped in ErrKeychain, after the choices are saved.
func (r *Runner) SaveConnectionPrefs(ctx context.Context, options Options) error { //nolint:gocritic // signature fixed by sessions.Runner and ui.JumphostDiscovery.
	target, ok := r.prefsTarget(ctx, &options)
	if !ok {
		return nil
	}
	prefs := &ConnectionPrefs{
		Profile:      resolveProfileName(options.AwsProfile),
		ResourceType: target.Type,
		LocalPort:    options.LocalPortNumber,
	}
	if target.Type == resourceDirect {
		// A direct database has neither AWS profile nor local port.
		prefs.Profile, prefs.LocalPort = "", 0
	}
	var keychainErr error
	if target.Type == resourceRDS || target.Type == resourceElastiCache || target.Type == resourceDirect {
		prefs.DBUser = strings.TrimSpace(options.DBUser)
		prefs.SignIn = options.SignInMethod()
		if target.Type == resourceDirect {
			// A direct database always signs in with its password.
			prefs.SignIn = SignInPassword
		}
		if target.Type == resourceElastiCache && prefs.SignIn == SignInPassword {
			// The keychain entry and the session sign the empty user in as the default user.
			prefs.DBUser = keychainUser(target.Type, prefs.DBUser)
		}
		if prefs.SignIn == SignInSecret {
			prefs.DBSecretARN = strings.TrimSpace(options.DBSecretARN)
		}
		prefs.MCPAllowWrites = options.MCPAllowWrites
		var previous *ConnectionPrefs
		if saved, found := r.prefs.Load(target); found {
			previous = &saved
			if r.writesLocked {
				// The lock ignores the saved choice, it does not delete it.
				prefs.MCPAllowWrites = saved.MCPAllowWrites
			}
		}
		if previous != nil && keepPreviousSignIn(previous, &options) {
			prefs.SignIn, prefs.DBSecretARN, prefs.PasswordInKeychain = previous.SignIn, previous.DBSecretARN, previous.PasswordInKeychain
			if method := previous.SignInMethod(); method == SignInPassword || method == SignInSecret {
				prefs.DBUser = previous.DBUser
			}
		} else {
			keychainErr = r.syncKeychain(target, previous, prefs, &options)
		}
	}
	return errors.Join(r.prefs.Save(target, prefs), keychainErr)
}

// keepPreviousSignIn reports whether the saved sign-in choices outlive a connection that did not
// choose one: the wizard offered neither a password nor a secret (e.g. --no-mcp) and leaves
// options.SignIn empty. A remembered password or secret, with its user, is then kept.
func keepPreviousSignIn(previous *ConnectionPrefs, options *Options) bool {
	return previous != nil && options.SignIn == "" && options.DBPassword == "" && strings.TrimSpace(options.DBSecretARN) == ""
}

// prefsTarget identifies the target keying the preferences of options; false when none apply
// (unsupported type, disabled, or the AWS account is unknown).
func (r *Runner) prefsTarget(ctx context.Context, options *Options) (TargetRef, bool) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	if resourceType == resourceDirect {
		// A direct database belongs to no AWS account: no lookup is needed.
		if r.prefs == nil || strings.TrimSpace(options.Name) == "" {
			return TargetRef{}, false
		}
		return DirectTargetRef(options.Name), true
	}
	if r.prefs == nil || !supportedResourceType(resourceType) || strings.TrimSpace(options.Name) == "" {
		return TargetRef{}, false
	}
	profile := resolveProfileName(options.AwsProfile)
	region := strings.TrimSpace(options.AwsRegion)
	if region == "" {
		region = r.ResolveRegion(ctx, profile)
	}
	if region == "" {
		return TargetRef{}, false
	}
	account := r.accountFor(ctx, profile)
	if account == "" {
		return TargetRef{}, false
	}
	return TargetRef{Account: account, Region: region, Type: resourceType, Name: strings.TrimSpace(options.Name)}, true
}

// CheckLocalPort returns an error explaining why port cannot be used for a new session.
func (r *Runner) CheckLocalPort(port int) error {
	return r.registry.CheckPort(port)
}

func (r *Runner) resolvePreferredLocalPort(ctx context.Context, options *Options) (int, error) {
	resourceType := strings.ToLower(strings.TrimSpace(options.ResourceType))
	switch resourceType {
	case resourceEKS:
		return defaultEKSLocalPort, nil
	case resourceForward:
		_, port, err := parseForwardTarget(options.Name)
		return port, err
	case resourceElastiCache:
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return 0, i18n.NewError("jumphost.runner.elasticache_cluster_name_is")
		}

		cfg, err := r.resolveAWSConfig(ctx, options)
		if err != nil {
			return 0, err
		}

		// Try replication group first, then standalone cache cluster, then serverless cache.
		if groups, err := r.replicationGroups(ctx, cfg, name); err == nil && len(groups) > 0 {
			if _, port := endpointOf(primaryEndpoint(&groups[0])); port > 0 {
				return port, nil
			}
		}
		if clusters, err := r.cacheClusters(ctx, cfg, name); err == nil && len(clusters) > 0 {
			if _, port := endpointOf(nodeEndpoint(&clusters[0])); port > 0 {
				return port, nil
			}
		}
		if caches, err := r.serverlessCaches(ctx, cfg, name); err == nil && len(caches) > 0 {
			if _, port := endpointOf(caches[0].Endpoint); port > 0 {
				return port, nil
			}
		}

		return defaultRedisPort, nil
	case resourceRDS:
		name := strings.TrimSpace(options.Name)
		if name == "" {
			return 0, i18n.NewError("jumphost.runner.rds_instance_name_is")
		}

		cfg, err := r.resolveAWSConfig(ctx, options)
		if err != nil {
			return 0, err
		}

		instance, err := r.describeDBInstance(ctx, cfg, name)
		if err != nil {
			return 0, err
		}
		if _, port := rdsEndpoint(&instance); port != 0 {
			return port, nil
		}
		return 0, i18n.NewError("jumphost.runner.rds_instance_endpoint_port")
	default:
		return 0, i18n.NewError("jumphost.runner.unsupported_resource_type_supported", options.ResourceType)
	}
}
