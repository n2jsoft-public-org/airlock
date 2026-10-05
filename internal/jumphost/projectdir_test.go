package jumphost

import (
	"io"
	"testing"
)

func TestProjectDirOfOptionsWinsOverTheRunners(t *testing.T) {
	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: "/runner"})
	if dir, err := runner.projectDirFor(&Options{ProjectDir: "/opening"}); err != nil || dir != "/opening" {
		t.Fatalf("dir = %q, %v", dir, err)
	}
	if dir, err := runner.projectDirFor(&Options{}); err != nil || dir != "/runner" {
		t.Fatalf("dir = %q, %v", dir, err)
	}
}
