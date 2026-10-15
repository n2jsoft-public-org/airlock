package ui

import (
	"slices"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

// disabledModulesRunner turns off the modules of its types.
type disabledModulesRunner struct {
	fakeSessionRunner
	types []string
}

func (r disabledModulesRunner) ModuleDisabled(resourceType string) bool {
	return slices.Contains(r.types, resourceType)
}

func TestWizardHidesCardsOfDisabledModules(t *testing.T) {
	runner := disabledModulesRunner{types: []string{"rds", "dynamodb", "s3"}}
	m := newTestWizard(t, runner, jumphost.Options{})
	var names []string
	for _, option := range m.typeOptions() {
		names = append(names, option.name)
	}
	if !slices.Equal(names, []string{"eks", "elasticache"}) {
		t.Fatalf("cards = %v, want [eks elasticache]", names)
	}
}
