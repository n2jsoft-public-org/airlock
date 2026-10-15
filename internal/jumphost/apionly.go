package jumphost

import "strings"

// resourceS3 is the session type of the S3 buckets of a profile: no jumphost, no tunnel, no local
// port; its MCP server is the session.
const resourceS3 = "s3"

// APIOnly reports a session type served by the AWS API alone: no jumphost, no tunnel, no local
// port; its MCP server is the session.
func APIOnly(resourceType string) bool {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case resourceDynamoDB, resourceS3:
		return true
	default:
		return false
	}
}
