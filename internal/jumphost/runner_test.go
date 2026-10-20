package jumphost

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/connections"
)

func TestPrepareWritesLockedForcesReadOnly(t *testing.T) {
	started := stubDirectMCP(t)
	runner := directRunner(t, &RunnerOptions{Containers: fakeContainers{"pg": pgContainer()}, WritesLocked: true})
	session, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg", MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if len(*started) != 1 || (*started)[0].AllowWrites {
		t.Fatalf("started = %+v; want one read-only MCP server", *started)
	}
}

func TestSaveConnectionPrefsWritesLockedKeepsTheSavedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	databases := connections.NewStore(path).Databases()
	if err := databases.Put("", billingDatabase()); err != nil {
		t.Fatal(err)
	}
	options := directOptions("", false)
	options.MCPAllowWrites = true
	if err := NewRunner(&RunnerOptions{PrefsPath: path, Databases: databases}).SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	locked := NewRunner(&RunnerOptions{PrefsPath: path, Databases: databases, WritesLocked: true})
	options.MCPAllowWrites = false
	if err := locked.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if prefs, ok := locked.LoadConnectionPrefs(context.Background(), options); !ok || !prefs.MCPAllowWrites {
		t.Fatalf("prefs = %+v, %v; a lock ignores the saved writes, it does not delete them", prefs, ok)
	}
}

// rdsPageXML is a DescribeDBInstances reply listing instances (DBInstance elements); marker,
// when set, names the next page.
func rdsPageXML(marker string, instances ...string) string {
	if marker != "" {
		marker = "<Marker>" + marker + "</Marker>"
	}
	return "<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances>" + strings.Join(instances, "") +
		"</DBInstances>" + marker + "</DescribeDBInstancesResult></DescribeDBInstancesResponse>"
}

// installRDS answers DescribeDBInstances with pages chained by markers page2, page3…, and fails
// any other operation.
func installRDS(t *testing.T, pages ...string) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if op := awsapitest.Operation(r); op != "DescribeDBInstances" {
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		awsapitest.RespondXML(t, w, http.StatusOK, pages[pageIndex(t, r.FormValue("Marker"))])
	})
}

// pageIndex is the index of the page that the pagination token pageN names; 0 without a token.
func pageIndex(t testing.TB, token string) int {
	if token == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(token, "page"))
	if err != nil {
		t.Error(err)
	}
	return n - 1
}

func TestRDSTargetsListsEveryPage(t *testing.T) {
	installRDS(t,
		rdsPageXML("page2", `<DBInstance><DBInstanceIdentifier>orders</DBInstanceIdentifier><Engine>postgres</Engine><EngineVersion>16.4</EngineVersion>`+
			`<DBInstanceStatus>available</DBInstanceStatus><AllocatedStorage>20</AllocatedStorage><IAMDatabaseAuthenticationEnabled>true</IAMDatabaseAuthenticationEnabled>`+
			`<Endpoint><Address>orders.rds.amazonaws.com</Address><Port>5432</Port></Endpoint></DBInstance>`),
		rdsPageXML("", `<DBInstance><DBInstanceIdentifier>archive</DBInstanceIdentifier><Engine>mysql</Engine></DBInstance>`))
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "rds", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	want := []ResourceSummary{
		{Name: "archive", Type: "rds", Description: "mysql | size n/a", Engine: "mysql"},
		{Name: "orders", Type: "rds", Description: "postgres 16.4 | orders.rds.amazonaws.com:5432 | 20 GiB | available", IAMAuth: true, Engine: "postgres"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, %v; want %+v", got, err, want)
	}
}

func TestRDSPortIsTheEndpointPort(t *testing.T) {
	installRDS(t, rdsPageXML("", `<DBInstance><DBInstanceIdentifier>archive</DBInstanceIdentifier><Endpoint><Port>55432</Port></Endpoint></DBInstance>`))
	port, err := testRunner(t).resolvePreferredLocalPort(t.Context(), &Options{ResourceType: "rds", Name: "archive", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil || port != 55432 {
		t.Fatalf("port = %d, %v; want 55432", port, err)
	}
}

func TestRDSTargetsKeepUnreachableErrors(t *testing.T) {
	srv := awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	srv.Close()
	_, err := testRunner(t).listResources(t.Context(), awsConfig{Profile: "dev", Region: "eu-west-3"}, "rds")
	if err == nil || !strings.Contains(err.Error(), "failed to list RDS instances") || strings.Contains(err.Error(), "sign in") {
		t.Fatalf("err = %v, want the RDS listing failing without asking to sign in", err)
	}
}

func TestRDSTargetsReportAnUnreachableRoleSourceWithoutSigningIn(t *testing.T) {
	// The assume-role call of the credentials fails: the network cause shows, not "sign in again".
	srv := awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	appendAWSConfig(t, "[profile app]\nregion = eu-west-3\nrole_arn = arn:aws:iam::111111111111:role/app\nsource_profile = dev\n")
	srv.Close()
	_, err := testRunner(t).listResources(t.Context(), awsConfig{Profile: "app", Region: "eu-west-3"}, "rds")
	if err == nil || !strings.Contains(err.Error(), "failed to list RDS instances") || !strings.Contains(err.Error(), "dial tcp") ||
		strings.Contains(err.Error(), "sign in") {
		t.Fatalf("err = %v, want the RDS listing failing on the network, without asking to sign in", err)
	}
}

func TestEKSTargetsListsEveryPage(t *testing.T) {
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if op := awsapitest.Operation(r); op != "GET /clusters" {
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("nextToken") == "" {
			awsapitest.Respond(t, w, http.StatusOK, map[string]any{"clusters": []string{"web"}, "nextToken": "page2"})
			return
		}
		awsapitest.Respond(t, w, http.StatusOK, map[string]any{"clusters": []string{"api"}})
	})
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "eks", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	want := []ResourceSummary{{Name: "api", Type: "eks"}, {Name: "web", Type: "eks"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, %v; want %+v", got, err, want)
	}
}
