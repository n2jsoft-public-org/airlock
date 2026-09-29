package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// searchRunner answers searches with fixed targets and failures, recording each search.
type searchRunner struct {
	*prefsRunner
	failures map[string]error
	searches *[]jumphost.Options
	targets  []jumphost.ResourceSummary
}

func (r searchRunner) SearchResources(_ context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error) {
	if r.searches != nil {
		*r.searches = append(*r.searches, *options)
	}
	return r.targets, r.failures, nil
}

var everyTypeTargets = []jumphost.ResourceSummary{
	{Name: "archive", Type: "rds", Description: "postgres 16", IAMAuth: true},
	{Name: "archive-cache", Type: "elasticache", Description: "valkey 8"},
	{Name: "platform", Type: "eks"},
}

func asWizard(t *testing.T, model tea.Model) jumphostWizardModel {
	t.Helper()
	m, ok := model.(jumphostWizardModel)
	if !ok {
		t.Fatalf("model is %T, want jumphostWizardModel", model)
	}
	return m
}

func asListItem(t *testing.T, item list.Item) jumphostListItem {
	t.Helper()
	got, ok := item.(jumphostListItem)
	if !ok {
		t.Fatalf("item is %T, want jumphostListItem", item)
	}
	return got
}

func searchWizard(t *testing.T, runner JumphostDiscovery, base jumphost.Options) jumphostWizardModel {
	t.Helper()
	m := newTestWizard(t, runner, base)
	updated, cmd := m.beginSearch()
	return runCmd(t, asWizard(t, updated), cmd)
}

func TestWizardSearchListsEveryType(t *testing.T) {
	m := searchWizard(t, searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets}, jumphost.Options{AwsRegion: "eu-west-3"})
	if m.step != jumphostStepTarget || len(m.targetList.Items()) != 3 {
		t.Fatalf("step = %v items = %d, want the three targets", m.step, len(m.targetList.Items()))
	}
	item := asListItem(t, m.targetList.Items()[2])
	if item.desc != "eks" || !strings.Contains(item.FilterValue(), "eks") {
		t.Fatalf("item = %+v, the type must be shown and filterable", item)
	}
	if first := asListItem(t, m.targetList.Items()[0]); first.desc != "rds · postgres 16" {
		t.Fatalf("desc = %q", first.desc)
	}
	if !strings.Contains(m.View(), "Search a target") {
		t.Fatal("the target step of a search must say so")
	}
}

func TestWizardSearchSavesThePickedType(t *testing.T) {
	runner := searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets}
	m := searchWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})
	m.targetList.Select(2) // platform, eks
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepReview || m.options.ResourceType != "eks" || m.targetIAMAuth {
		t.Fatalf("step = %v type = %q iam = %v", m.step, m.options.ResourceType, m.targetIAMAuth)
	}
	m, _ = wizardUpdate(t, m, key("enter"))
	if runner.lastSave.ResourceType != "eks" || runner.lastSave.Name != "platform" {
		t.Fatalf("saved %+v, want the eks target", *runner.lastSave)
	}
}

func TestWizardSearchWarnsAboutFailedTypes(t *testing.T) {
	runner := searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets[:1], failures: map[string]error{"eks": errors.New("AccessDenied")}}
	m := searchWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})
	if m.step != jumphostStepTarget || !strings.Contains(m.stepHint(), "EKS: AccessDenied") {
		t.Fatalf("step = %v hint = %q", m.step, m.stepHint())
	}
}

func TestWizardSearchFailsWhenEveryTypeFails(t *testing.T) {
	failures := map[string]error{"rds": errors.New("denied"), "eks": errors.New("denied"), "elasticache": errors.New("denied")}
	m := newJumphostWizardModel(context.Background(), searchRunner{prefsRunner: newPrefsRunner(), failures: failures}, jumphost.Options{AwsRegion: "eu-west-3"})
	m = pastStart(t, m)
	updated, cmd := m.beginSearch()
	m = asWizard(t, updated)
	m, _ = wizardUpdate(t, m, findMsg[jumphostProfilesLoadedMsg](t, cmd))
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepProfile || !strings.Contains(m.errorMsg, "RDS: denied") {
		t.Fatalf("step = %v error = %q, want the error on the profile step", m.step, m.errorMsg)
	}
}

func TestWizardSearchAsksProfileFirst(t *testing.T) {
	m := pastStart(t, newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{}))
	updated, _ := m.beginSearch()
	m = asWizard(t, updated)
	if m.flow != flowSearch || m.step != jumphostStepProfile {
		t.Fatalf("flow = %v step = %v, want the profile step of a search", m.flow, m.step)
	}
	steps, _ := m.breadcrumb()
	if strings.Join(steps, ",") != "Profile,Target,Connect" {
		t.Fatalf("breadcrumb = %v", steps)
	}
}

func TestWizardSearchRegionChangeSearchesAgain(t *testing.T) {
	var searches []jumphost.Options
	runner := searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets, searches: &searches}
	m := searchWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})

	m, _ = wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, key("ireland"))
	m, cmd := wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if len(searches) != 2 || searches[1].AwsRegion != "eu-west-1" || m.step != jumphostStepTarget {
		t.Fatalf("searches = %+v step = %v", searches, m.step)
	}
}

func TestWizardSearchFailedReloadLeavesStaleTargets(t *testing.T) {
	m := searchWizard(t, searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets}, jumphost.Options{AwsRegion: "eu-west-3"})
	m, _ = wizardUpdate(t, m, jumphostSearchLoadedMsg{failures: map[string]error{"rds": errors.New("x"), "eks": errors.New("x"), "elasticache": errors.New("x")}})
	// Without history nor profile step there is nowhere to step back to: the list must empty.
	if m.step == jumphostStepTarget && len(m.targetList.Items()) != 0 {
		t.Fatal("a failed search must not keep the targets of the previous region on offer")
	}
}

func TestSearchFailureSummaryOrdersTypes(t *testing.T) {
	got := searchFailureSummary(map[string]error{"elasticache": errors.New("b"), "rds": errors.New("a")})
	if got != "RDS: a · ELASTICACHE: b" {
		t.Fatalf("got %q", got)
	}
}

var _ tea.Model = jumphostWizardModel{}

func TestSearchFailureSummaryKeepsTheAWSErrorCode(t *testing.T) {
	got := searchFailureSummary(map[string]error{
		"eks": errors.New("aws eks list-clusters failed: An error occurred (AccessDeniedException) when calling the ListClusters operation: denied"),
		"rds": errors.New("no credentials"),
	})
	if got != "RDS: no credentials · EKS: AccessDeniedException" {
		t.Fatalf("got %q", got)
	}
}

func TestWizardFailedSearchClearsWarnings(t *testing.T) {
	runner := searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets[:1], failures: map[string]error{"eks": errors.New("AccessDenied")}}
	m := searchWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})
	if m.searchWarnings == "" {
		t.Fatal("the first search must warn about eks")
	}
	m, _ = wizardUpdate(t, m, jumphostSearchLoadedMsg{err: errors.New("expired token")})
	if m.searchWarnings != "" {
		t.Fatalf("warnings = %q, a failed search must not keep the previous warnings", m.searchWarnings)
	}
}

func TestWizardSearchFailedReloadStepsBackToProfile(t *testing.T) {
	m := pastStart(t, newJumphostWizardModel(context.Background(), searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets}, jumphost.Options{AwsRegion: "eu-west-3"}))
	updated, cmd := m.beginSearch()
	m = asWizard(t, updated)
	m, _ = wizardUpdate(t, m, findMsg[jumphostProfilesLoadedMsg](t, cmd))
	m, cmd = wizardUpdate(t, m, key("enter")) // dev
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepTarget || len(m.targetList.Items()) != 3 {
		t.Fatalf("step = %v items = %d, want the three targets", m.step, len(m.targetList.Items()))
	}

	failures := map[string]error{"rds": errors.New("denied"), "eks": errors.New("denied"), "elasticache": errors.New("denied")}
	m.runner = searchRunner{prefsRunner: newPrefsRunner(), failures: failures}
	m, _ = wizardUpdate(t, m, key("r"))
	m, _ = wizardUpdate(t, m, key("ireland"))
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepProfile || len(m.targetList.Items()) != 0 {
		t.Fatalf("step = %v items = %d, want the profile step and no stale targets", m.step, len(m.targetList.Items()))
	}
}
