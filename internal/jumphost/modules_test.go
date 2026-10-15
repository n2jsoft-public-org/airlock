package jumphost

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestPrepareRefusesDisabledModule(t *testing.T) {
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DisabledModules: []string{"sql", "cache"}})
	for _, resourceType := range []string{"rds", "direct", "docker", "docker-cache", "ElastiCache"} {
		if !runner.ModuleDisabled(resourceType) {
			t.Errorf("ModuleDisabled(%s) = false, want true", resourceType)
		}
		if _, err := runner.Prepare(context.Background(), Options{ResourceType: resourceType, Name: "x"}); err == nil || !strings.Contains(err.Error(), "disabledModules") {
			t.Errorf("Prepare(%s) error = %v, want the disabled module", resourceType, err)
		}
	}
	for _, resourceType := range []string{"eks", "dynamodb", "s3"} {
		if runner.ModuleDisabled(resourceType) {
			t.Errorf("ModuleDisabled(%s) = true, want false", resourceType)
		}
	}
}

func TestPrepareRefusesDisabledS3(t *testing.T) {
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DisabledModules: []string{"s3"}})
	if _, err := runner.Prepare(context.Background(), Options{ResourceType: "s3"}); err == nil || !strings.Contains(err.Error(), "disabledModules") {
		t.Fatalf("Prepare(s3) error = %v, want the disabled module", err)
	}
	if runner.ModuleDisabled("dynamodb") {
		t.Fatal("disabling s3 must leave dynamodb on")
	}
}
