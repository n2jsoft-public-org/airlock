package jumphost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func savedFixture() []SavedConnection {
	return []SavedConnection{
		{Ref: TargetRef{Account: "1", Region: "eu-west-3", Type: "rds", Name: "archive"}, Prefs: ConnectionPrefs{Profile: "dev"}},
		{Ref: TargetRef{Account: "1", Region: "eu-west-1", Type: "eks", Name: "cluster"}, Prefs: ConnectionPrefs{Profile: "dev"}},
		{Ref: TargetRef{Account: "2", Region: "eu-west-3", Type: "rds", Name: "orders"}, Prefs: ConnectionPrefs{Profile: "prod"}},
		{Ref: TargetRef{Account: "3", Region: "eu-west-3", Type: "rds", Name: "legacy"}, Prefs: ConnectionPrefs{Profile: "gone"}},
	}
}

func TestFilterSavedConnections(t *testing.T) {
	known := map[string]bool{"dev": true, "prod": true}
	cases := []struct {
		name            string
		profile, region string
		want            []string
	}{
		{name: "no filter", want: []string{"archive", "cluster", "orders", "legacy"}},
		{name: "profile", profile: "dev", want: []string{"archive", "cluster"}},
		{name: "region", region: "eu-west-3", want: []string{"archive", "orders", "legacy"}},
		{name: "both", profile: "dev", region: "eu-west-3", want: []string{"archive"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterSavedConnections(savedFixture(), tc.profile, tc.region, known)
			names := make([]string, 0, len(got))
			for _, conn := range got {
				names = append(names, conn.Ref.Name)
			}
			if len(names) != len(tc.want) {
				t.Fatalf("names = %v, want %v", names, tc.want)
			}
			for idx := range names {
				if names[idx] != tc.want[idx] {
					t.Fatalf("names = %v, want %v", names, tc.want)
				}
			}
		})
	}
}

func TestFilterSavedConnectionsFlagsMissingProfiles(t *testing.T) {
	got := filterSavedConnections(savedFixture(), "", "", map[string]bool{"dev": true, "prod": true})
	for _, conn := range got {
		if want := conn.Prefs.Profile == "gone"; conn.ProfileMissing != want {
			t.Fatalf("%s ProfileMissing = %v, want %v", conn.Ref.Name, conn.ProfileMissing, want)
		}
	}
	for _, conn := range filterSavedConnections(savedFixture(), "", "", nil) {
		if conn.ProfileMissing {
			t.Fatalf("unknown profiles must flag nothing, got %s", conn.Ref.Name)
		}
	}
}

func TestRunnerForgetConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	runner := NewRunner(RunnerOptions{PrefsPath: path})
	runner.rememberAccount("dev", "111111111111")
	options := Options{ResourceType: "eks", Name: "cluster", AwsProfile: "dev", AwsRegion: "eu-west-3", LocalPortNumber: 9444}
	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}

	ref := TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "eks", Name: "cluster"}
	if err := runner.ForgetConnection(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.LoadConnectionPrefs(context.Background(), options); ok {
		t.Fatal("the forgotten connection is still remembered")
	}
}

func TestFilterSavedConnectionsKeepsDirectDatabases(t *testing.T) {
	saved := append(savedFixture(), SavedConnection{Ref: DirectTargetRef("legacy-billing"), Prefs: ConnectionPrefs{ResourceType: "direct"}})
	got := filterSavedConnections(saved, "dev", "eu-west-3", map[string]bool{"dev": true})
	last := got[len(got)-1]
	if len(got) != 2 || last.Ref.Name != "legacy-billing" || last.ProfileMissing {
		t.Fatalf("got = %+v, want archive and the direct database, which has no profile to miss", got)
	}
}

func TestListSavedConnectionsFlagsRemovedDirectDatabases(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "connections.json")
	store := NewPrefsStore(path)
	for _, name := range []string{"legacy-billing", "gone-db"} {
		if err := store.Save(DirectTargetRef(name), &ConnectionPrefs{ResourceType: "direct", DBUser: "mcp_ro"}); err != nil {
			t.Fatal(err)
		}
	}
	missing := func(databases DirectDatabases) map[string]bool {
		t.Helper()
		runner := NewRunner(RunnerOptions{PrefsPath: path, Databases: databases})
		saved, err := runner.ListSavedConnections(context.Background(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		flags := map[string]bool{}
		for _, conn := range saved {
			flags[conn.Ref.Name] = conn.TargetMissing
		}
		return flags
	}

	if got := missing(directDatabases(billingDatabase())); got["legacy-billing"] || !got["gone-db"] {
		t.Fatalf("TargetMissing = %v, want only gone-db", got)
	}
	if got := missing(nil); got["legacy-billing"] || got["gone-db"] {
		t.Fatalf("without databases nothing can be missing: %v", got)
	}
	unreadable := fakeDirectDatabases{err: errors.New("databases.json unreadable")}
	if got := missing(unreadable); got["legacy-billing"] || got["gone-db"] {
		t.Fatalf("an unreadable file proves nothing missing: %v", got)
	}
}
