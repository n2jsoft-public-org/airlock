package sessions

import "testing"

func TestDockerSessionRunsNoProcessAndKeysByType(t *testing.T) {
	docker := Session{ResourceType: DockerResourceType, Target: "PG"}
	if !docker.Direct() || docker.Key() != "docker|pg" {
		t.Fatalf("docker: direct = %v key = %q", docker.Direct(), docker.Key())
	}
	direct := Session{ResourceType: DirectResourceType, Target: "PG"}
	if !direct.Direct() || direct.Key() != "direct|pg" {
		t.Fatalf("direct: direct = %v key = %q", direct.Direct(), direct.Key())
	}
}

func TestDockerCacheSessionRunsNoProcessAndKeysByType(t *testing.T) {
	s := Session{ResourceType: DockerCacheResourceType, Target: "Cache"}
	if !s.Direct() || s.Key() != "docker-cache|cache" {
		t.Fatalf("direct = %v key = %q", s.Direct(), s.Key())
	}
}

func TestDynamoDBSessionRunsNoProcessButIsAnAWSSession(t *testing.T) {
	s := Session{ResourceType: DynamoDBResourceType, Target: "eu-west-3", Profile: "dev", Region: "eu-west-3"}
	if !s.NoTunnel() || s.Direct() || s.Key() != "dev|eu-west-3|dynamodb|eu-west-3" {
		t.Fatalf("no tunnel = %v direct = %v key = %q", s.NoTunnel(), s.Direct(), s.Key())
	}
	if !(&Session{ResourceType: DirectResourceType}).NoTunnel() || (&Session{ResourceType: "rds"}).NoTunnel() {
		t.Fatal("direct sessions have no tunnel, RDS ones have one")
	}
}

func TestS3SessionRunsNoProcessButIsAnAWSSession(t *testing.T) {
	s := Session{ResourceType: S3ResourceType, Target: "buckets", Profile: "dev", Region: "eu-west-3"}
	if !s.NoTunnel() || s.Direct() || s.Key() != "dev|s3|buckets" {
		t.Fatalf("no tunnel = %v direct = %v key = %q", s.NoTunnel(), s.Direct(), s.Key())
	}
	other := Session{ResourceType: S3ResourceType, Target: "buckets", Profile: "dev", Region: "us-east-1"}
	if other.Key() != s.Key() {
		t.Fatalf("keys differ by region: %q, %q", s.Key(), other.Key())
	}
}

func TestLogsSessionRunsNoProcessButIsAnAWSSession(t *testing.T) {
	s := Session{ResourceType: LogsResourceType, Target: "eu-west-3", Profile: "dev", Region: "eu-west-3"}
	if !s.NoTunnel() || s.Direct() || s.Key() != "dev|eu-west-3|logs|eu-west-3" {
		t.Fatalf("no tunnel = %v direct = %v key = %q", s.NoTunnel(), s.Direct(), s.Key())
	}
}
