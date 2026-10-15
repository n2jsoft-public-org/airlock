package jumphost

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchResourcesMergesTypesByName(t *testing.T) {
	var calls atomic.Int32
	listed := map[string][]ResourceSummary{
		"rds":         {{Name: "archive", IAMAuth: true}, {Name: "orders"}},
		"eks":         {{Name: "archive"}},
		"elasticache": {{Name: "cache"}},
	}
	got, failures := searchResources(searchableResourceTypes, func(resourceType string) ([]ResourceSummary, error) {
		calls.Add(1)
		return listed[resourceType], nil
	})

	if calls.Load() != 4 || len(failures) != 0 {
		t.Fatalf("calls = %d failures = %v", calls.Load(), failures)
	}
	want := []string{"archive/eks", "archive/rds", "cache/elasticache", "orders/rds"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for idx, resource := range got {
		if key := resource.Name + "/" + resource.Type; key != want[idx] {
			t.Fatalf("got[%d] = %s, want %s", idx, key, want[idx])
		}
	}
	if !got[1].IAMAuth {
		t.Fatal("the RDS summary lost its IAM auth flag")
	}
}

func TestSearchResourcesKeepsResultsOfTypesThatWork(t *testing.T) {
	got, failures := searchResources(searchableResourceTypes, func(resourceType string) ([]ResourceSummary, error) {
		if resourceType == "eks" {
			return nil, errors.New("AccessDenied")
		}
		return []ResourceSummary{{Name: resourceType + "-target"}}, nil
	})
	if len(got) != 3 || len(failures) != 1 || failures["eks"] == nil {
		t.Fatalf("got = %+v failures = %v", got, failures)
	}
}

func TestFindResourceByName(t *testing.T) {
	resources := []ResourceSummary{{Name: "archive", Type: "rds", IAMAuth: true}, {Name: "orders", Type: "rds"}}
	found, err := findResourceByName(resources, " archive ", "rds", "eu-west-3")
	if err != nil || !found.IAMAuth || found.Name != "archive" {
		t.Fatalf("found = %+v, %v", found, err)
	}
	_, err = findResourceByName(resources, "gone", "rds", "eu-west-3")
	if err == nil || !strings.Contains(err.Error(), "RDS target gone no longer exists in eu-west-3") {
		t.Fatalf("err = %v", err)
	}
}

func TestWithTypeTagsEveryResource(t *testing.T) {
	got := withType([]ResourceSummary{{Name: "a"}, {Name: "b"}}, "eks")
	if got[0].Type != "eks" || got[1].Type != "eks" {
		t.Fatalf("got = %+v", got)
	}
}

func TestCheckAccount(t *testing.T) {
	if err := checkAccount("dev", "111111111111", "111111111111"); err != nil {
		t.Fatalf("same account refused: %v", err)
	}
	if err := checkAccount("dev", "111111111111", ""); err != nil {
		t.Fatalf("no expected account must not refuse: %v", err)
	}

	err := checkAccount("prod", "222222222222", "111111111111")
	if err == nil {
		t.Fatal("another account must be refused")
	}
	if mismatch, ok := errors.AsType[*AccountMismatchError](err); !ok || mismatch.Profile != "prod" {
		t.Fatalf("err = %v, want an AccountMismatchError for prod", err)
	}
	if want := "profile prod is account 222222222222, this entry is account 111111111111"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}

	if err := checkAccount("prod", "", "111111111111"); err == nil || !strings.Contains(err.Error(), "cannot confirm that profile prod reaches account 111111111111") {
		t.Fatalf("unknown account err = %v", err)
	}
}
