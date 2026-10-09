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
