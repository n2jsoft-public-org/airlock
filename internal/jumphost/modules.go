package jumphost

import (
	"slices"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// ModuleOf is the module (config.Modules) of the targets of resourceType; empty for an unknown type.
func ModuleOf(resourceType string) string {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case resourceRDS, resourceDirect, resourceDocker:
		return "sql"
	case resourceEKS:
		return "k8s"
	case resourceElastiCache, resourceDockerCache:
		return "cache"
	case resourceDynamoDB:
		return "dynamodb"
	case resourceS3:
		return "s3"
	case resourceLogs:
		return "logs"
	default:
		return ""
	}
}

// ModuleDisabled reports whether the module of resourceType is in RunnerOptions.DisabledModules.
func (r *Runner) ModuleDisabled(resourceType string) bool {
	module := ModuleOf(resourceType)
	return module != "" && slices.Contains(r.disabledModules, module)
}

// checkModule refuses a target whose module is disabled.
func (r *Runner) checkModule(resourceType string) error {
	if r.ModuleDisabled(resourceType) {
		return i18n.NewError("jumphost.modules.disabled", ModuleOf(resourceType), resourceType)
	}
	return nil
}
