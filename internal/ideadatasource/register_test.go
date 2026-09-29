package ideadatasource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beevik/etree"
)

// useIsolatedLeases gives the test its own lease directory and lets it pick the session PID.
func useIsolatedLeases(t *testing.T) (setPID func(int)) {
	t.Helper()
	root := t.TempDir()
	previousRoot, previousPID := leaseRoot, currentPID
	leaseRoot = func() (string, error) { return root, nil }
	pid := os.Getpid()
	currentPID = func() int { return pid }
	t.Cleanup(func() {
		leaseRoot, currentPID = previousRoot, previousPID
	})
	return func(value int) { pid = value }
}

func samplePostgres(port int) *DataSource {
	return &DataSource{
		Key:      "rds|prod|eu-west-3|my-db",
		Name:     "acme prod my-db",
		Engine:   "postgres",
		Host:     "localhost",
		Port:     port,
		Database: "app",
	}
}

func TestRegisterSkipsWithoutIdeaDir(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()

	result, err := Register(dir, samplePostgres(5432))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Registered {
		t.Fatal("expected no registration without .idea directory")
	}
	if _, err := os.Stat(filepath.Join(dir, ideaDirName)); !os.IsNotExist(err) {
		t.Fatal(".idea directory must not be created")
	}
}

func TestRegisterSkipsUnsupportedEngine(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	ds := samplePostgres(5432)
	ds.Engine = "docdb"
	result, err := Register(dir, ds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Registered {
		t.Fatal("expected no registration for unsupported engine")
	}
}

func TestRegisterCreatesFile(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	result, err := Register(dir, samplePostgres(5432))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered {
		t.Fatal("expected registration")
	}

	doc := readDoc(t, result.Paths[0])
	sources := doc.FindElements("//component[@name='DataSourceManagerImpl']/data-source")
	if len(sources) != 1 {
		t.Fatalf("expected 1 data source, got %d", len(sources))
	}
	source := sources[0]
	assertChild(t, source, "driver-ref", "postgresql")
	assertChild(t, source, "jdbc-driver", "org.postgresql.Driver")
	assertChild(t, source, "jdbc-url", "jdbc:postgresql://localhost:5432/app")
	assertChild(t, source, "synchronize", "true")
	if got := source.SelectAttrValue("name", ""); got != "acme prod my-db" {
		t.Fatalf("unexpected name %q", got)
	}
}

func TestRegisterUpdatesExistingEntryAndPreservesOthers(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))
	path := filepath.Join(dir, ideaDirName, dataSourcesFileName)
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="DataSourceManagerImpl" format="xml" multifile-model="true">
    <data-source source="LOCAL" name="local-db" uuid="11111111-2222-3333-4444-555555555555">
      <driver-ref>mysql.8</driver-ref>
      <jdbc-url>jdbc:mysql://localhost:3306</jdbc-url>
      <custom-setting>keep-me</custom-setting>
    </data-source>
  </component>
  <component name="OtherComponent" />
</project>
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Register(dir, samplePostgres(5432)); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := Register(dir, samplePostgres(15432)); err != nil {
		t.Fatalf("second register: %v", err)
	}

	doc := readDoc(t, path)
	sources := doc.FindElements("//component[@name='DataSourceManagerImpl']/data-source")
	if len(sources) != 2 {
		t.Fatalf("expected 2 data sources, got %d", len(sources))
	}
	local := doc.FindElement("//data-source[@name='local-db']")
	if local == nil {
		t.Fatal("existing data source was removed")
	}
	assertChild(t, local, "custom-setting", "keep-me")
	if doc.FindElement("//component[@name='OtherComponent']") == nil {
		t.Fatal("unrelated component was removed")
	}
	updated := doc.FindElement("//data-source[@name='acme prod my-db']")
	assertChild(t, updated, "jdbc-url", "jdbc:postgresql://localhost:15432/app")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode changed to %v", info.Mode().Perm())
	}
}

func TestStableUUIDIsDeterministicV5(t *testing.T) {
	first := stableUUID("a")
	if first != stableUUID("a") {
		t.Fatal("uuid must be deterministic")
	}
	if first == stableUUID("b") {
		t.Fatal("different keys must produce different uuids")
	}
	if len(first) != 36 || first[14] != '5' {
		t.Fatalf("expected version 5 uuid, got %q", first)
	}
	if !strings.ContainsRune("89ab", rune(first[19])) {
		t.Fatalf("expected RFC 4122 variant, got %q", first)
	}
}

func TestDriverForEngine(t *testing.T) {
	cases := map[string]string{
		"postgres":          "jdbc:postgresql://localhost:1/db",
		"aurora-postgresql": "jdbc:postgresql://localhost:1/db",
		"mysql":             "jdbc:mysql://localhost:1/db",
		"aurora-mysql":      "jdbc:mysql://localhost:1/db",
		"mariadb":           "jdbc:mariadb://localhost:1/db",
		"sqlserver-se":      "jdbc:sqlserver://localhost:1;databaseName=db",
		"oracle-ee":         "jdbc:oracle:thin:@localhost:1:db",
		"redis":             "jdbc:redis://localhost:1/0",
		"valkey":            "jdbc:redis://localhost:1/0",
	}
	for engine, want := range cases {
		driver, ok := DriverForEngine(engine)
		if !ok {
			t.Fatalf("engine %q should be supported", engine)
		}
		if got := driver.URL("localhost", 1, "db"); got != want {
			t.Fatalf("engine %q: got %q, want %q", engine, got, want)
		}
	}
	if _, ok := DriverForEngine("docdb"); ok {
		t.Fatal("docdb should not be supported")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func readDoc(t *testing.T, path string) *etree.Document {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromFile(path); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return doc
}

func assertChild(t *testing.T, parent *etree.Element, tag, want string) {
	t.Helper()
	if parent == nil {
		t.Fatalf("parent element is nil when looking for %s", tag)
	}
	child := parent.SelectElement(tag)
	if child == nil {
		t.Fatalf("missing <%s>", tag)
	}
	if got := child.Text(); got != want {
		t.Fatalf("<%s>: got %q, want %q", tag, got, want)
	}
}

func TestUnregisterRemovesOnlyOwnEntry(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))
	path := filepath.Join(dir, ideaDirName, dataSourcesFileName)
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="DataSourceManagerImpl" format="xml" multifile-model="true">
    <data-source source="LOCAL" name="local-db" uuid="11111111-2222-3333-4444-555555555555">
      <driver-ref>mysql.8</driver-ref>
    </data-source>
  </component>
</project>
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	ds := samplePostgres(5432)
	if _, err := Register(dir, ds); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := Unregister(dir, ds.Key)
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if !result.Removed {
		t.Fatal("expected entry to be removed")
	}

	doc := readDoc(t, path)
	if doc.FindElement("//data-source[@name='acme prod my-db']") != nil {
		t.Fatal("registered entry is still present")
	}
	if doc.FindElement("//data-source[@name='local-db']") == nil {
		t.Fatal("unrelated entry was removed")
	}

	result, err = Unregister(dir, ds.Key)
	if err != nil {
		t.Fatalf("second unregister: %v", err)
	}
	if result.Removed {
		t.Fatal("second unregister should be a no-op")
	}
}

func TestUnregisterWithoutFile(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	result, err := Unregister(dir, "any")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Removed {
		t.Fatal("nothing should be removed")
	}
}

func TestUnregisterKeepsEntryWhileAnotherSessionIsActive(t *testing.T) {
	setPID := useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))
	path := filepath.Join(dir, ideaDirName, dataSourcesFileName)

	// Two live processes: this test and its parent.
	first, second := os.Getpid(), os.Getppid()

	setPID(first)
	if _, err := Register(dir, samplePostgres(5432)); err != nil {
		t.Fatalf("first register: %v", err)
	}
	setPID(second)
	if _, err := Register(dir, samplePostgres(15432)); err != nil {
		t.Fatalf("second register: %v", err)
	}

	// The second session closes first: the entry must point back at the first session's port.
	result, err := Unregister(dir, samplePostgres(0).Key)
	if err != nil {
		t.Fatalf("second unregister: %v", err)
	}
	if result.Removed || result.ActiveSessions != 1 {
		t.Fatalf("expected entry kept for 1 active session, got %+v", result)
	}
	entry := readDoc(t, path).FindElement("//data-source[@name='acme prod my-db']")
	assertChild(t, entry, "jdbc-url", "jdbc:postgresql://localhost:5432/app")

	setPID(first)
	result, err = Unregister(dir, samplePostgres(0).Key)
	if err != nil {
		t.Fatalf("first unregister: %v", err)
	}
	if !result.Removed {
		t.Fatalf("expected entry removed after last session, got %+v", result)
	}
	if readDoc(t, path).FindElement("//data-source[@name='acme prod my-db']") != nil {
		t.Fatal("entry still present after last session closed")
	}
}

func TestUnregisterIgnoresStaleLeases(t *testing.T) {
	setPID := useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	// A crashed session left a lease behind.
	setPID(deadPID(t))
	if _, err := Register(dir, samplePostgres(5432)); err != nil {
		t.Fatalf("crashed session register: %v", err)
	}

	setPID(os.Getpid())
	if _, err := Register(dir, samplePostgres(5432)); err != nil {
		t.Fatalf("register: %v", err)
	}
	result, err := Unregister(dir, samplePostgres(0).Key)
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if !result.Removed {
		t.Fatalf("stale lease must not keep the entry, got %+v", result)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 90000; pid < 99999; pid++ {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Skip("no free pid found")
	return 0
}

func TestRegisterUsesRiderNestedLayout(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	riderDir := filepath.Join(dir, ideaDirName, ".idea.My.Solution", ideaDirName)
	mustMkdir(t, riderDir)

	ds := samplePostgres(5432)
	result, err := Register(dir, ds)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	want := filepath.Join(riderDir, dataSourcesFileName)
	if len(result.Paths) != 1 || result.Paths[0] != want {
		t.Fatalf("expected Rider path %q, got %v", want, result.Paths)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ideaDirName, dataSourcesFileName)); !os.IsNotExist(statErr) {
		t.Fatal("top-level .idea/dataSources.xml must not be written for Rider projects")
	}
	if readDoc(t, want).FindElement("//data-source[@name='acme prod my-db']") == nil {
		t.Fatal("entry missing from Rider data sources file")
	}

	unregistered, err := Unregister(dir, ds.Key)
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if !unregistered.Removed {
		t.Fatal("expected entry removed from Rider data sources file")
	}
}

func TestRegisterWritesPropertiesAndKeepsOthers(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	ds := samplePostgres(5432)
	ds.Properties = map[string]string{"AWS.RegionId": "eu-west-3", "AWS.CredentialId": "profile:prod"}
	result, err := Register(dir, ds)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Simulate the IDE adding its own property to our entry.
	path := result.Paths[0]
	doc := readDoc(t, path)
	container := doc.FindElement("//data-source[@name='acme prod my-db']/jdbc-additional-properties")
	if container == nil {
		t.Fatal("missing jdbc-additional-properties")
	}
	ideProperty := container.CreateElement("property")
	ideProperty.CreateAttr("name", "ide.setting")
	ideProperty.CreateAttr("value", "keep")
	if writeErr := doc.WriteToFile(path); writeErr != nil {
		t.Fatal(writeErr)
	}

	ds.Properties["AWS.RegionId"] = "eu-west-1"
	if _, err := Register(dir, ds); err != nil {
		t.Fatalf("second register: %v", err)
	}

	entry := readDoc(t, path).FindElement("//data-source[@name='acme prod my-db']")
	assertProperty(t, entry, "AWS.RegionId", "eu-west-1")
	assertProperty(t, entry, "AWS.CredentialId", "profile:prod")
	assertProperty(t, entry, "ide.setting", "keep")
	if len(entry.FindElements("./jdbc-additional-properties/property[@name='AWS.RegionId']")) != 1 {
		t.Fatal("property must not be duplicated")
	}
}

func assertProperty(t *testing.T, entry *etree.Element, name, want string) {
	t.Helper()
	property := entry.FindElement("./jdbc-additional-properties/property[@name='" + name + "']")
	if property == nil {
		t.Fatalf("missing property %s", name)
	}
	if got := property.SelectAttrValue("value", ""); got != want {
		t.Fatalf("property %s: got %q, want %q", name, got, want)
	}
}

func TestRegisterWritesChildrenInIDEOrder(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	ds := samplePostgres(5432)
	ds.Properties = map[string]string{"AWS.RdsSigningHost": "db.example.com"}
	result, err := Register(dir, ds)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Re-registering an existing entry must keep the order too.
	if _, err = Register(dir, ds); err != nil {
		t.Fatalf("second register: %v", err)
	}

	entry := readDoc(t, result.Paths[0]).FindElement("//data-source[@name='acme prod my-db']")
	var tags []string
	for _, child := range entry.ChildElements() {
		tags = append(tags, child.Tag)
	}
	want := strings.Join(dataSourceChildOrder, ",")
	if got := strings.Join(tags, ","); got != want {
		t.Fatalf("child order:\n got  %s\n want %s", got, want)
	}
	if entry.SelectElement("configured-by-url") != nil {
		t.Fatal("configured-by-url must not be written: it switches the IDE to URL-only mode")
	}
}

func TestRegisterRemovesLegacyConfiguredByURL(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	ds := samplePostgres(5432)
	result, err := Register(dir, ds)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	path := result.Paths[0]
	doc := readDoc(t, path)
	legacy := doc.FindElement("//data-source[@name='acme prod my-db']").CreateElement("configured-by-url")
	legacy.SetText("true")
	if writeErr := doc.WriteToFile(path); writeErr != nil {
		t.Fatal(writeErr)
	}

	if _, err = Register(dir, ds); err != nil {
		t.Fatalf("second register: %v", err)
	}
	if readDoc(t, path).FindElement("//data-source[@name='acme prod my-db']/configured-by-url") != nil {
		t.Fatal("legacy configured-by-url was not removed")
	}
}

func TestRegisterWritesLocalAuthSettings(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))
	localPath := filepath.Join(dir, ideaDirName, localFileName)
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="dataSourceStorageLocal" created-in="RD-262.10315.191">
    <data-source name="other" uuid="11111111-2222-3333-4444-555555555555">
      <schema-mapping />
    </data-source>
  </component>
</project>
`
	if err := os.WriteFile(localPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	ds := samplePostgres(5432)
	ds.AuthProvider = "aws.rds.iam"
	ds.User = "acme_ro"
	if _, err := Register(dir, ds); err != nil {
		t.Fatalf("register: %v", err)
	}

	doc := readDoc(t, localPath)
	entry := doc.FindElement("//component[@name='dataSourceStorageLocal']/data-source[@uuid='" + stableUUID(ds.Key) + "']")
	if entry == nil {
		t.Fatal("missing local settings entry")
	}
	assertChild(t, entry, "auth-provider", "aws.rds.iam")
	assertChild(t, entry, "user-name", "acme_ro")
	if entry.SelectElement("schema-mapping") == nil {
		t.Fatal("missing schema-mapping")
	}
	if doc.FindElement("//data-source[@name='other']") == nil {
		t.Fatal("unrelated local entry was removed")
	}
	if got := doc.FindElement("//component[@name='dataSourceStorageLocal']").SelectAttrValue("created-in", ""); got != "RD-262.10315.191" {
		t.Fatalf("component attributes changed: created-in=%q", got)
	}
}

func TestRegisterSkipsLocalSettingsWhenEmpty(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	if _, err := Register(dir, samplePostgres(5432)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ideaDirName, localFileName)); !os.IsNotExist(err) {
		t.Fatal("dataSources.local.xml must not be created without auth settings")
	}
}

func TestRegisterWithPasswordDropsEarlierAuthProvider(t *testing.T) {
	useIsolatedLeases(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ideaDirName))

	ds := samplePostgres(5432)
	ds.AuthProvider = "aws.rds.iam"
	ds.User = "acme_ro"
	if _, err := Register(dir, ds); err != nil {
		t.Fatalf("register: %v", err)
	}

	ds.AuthProvider = ""
	ds.User = "app_user"
	if _, err := Register(dir, ds); err != nil {
		t.Fatalf("register again: %v", err)
	}

	entry := readDoc(t, filepath.Join(dir, ideaDirName, localFileName)).
		FindElement("//component[@name='dataSourceStorageLocal']/data-source[@uuid='" + stableUUID(ds.Key) + "']")
	if entry == nil {
		t.Fatal("missing local settings entry")
	}
	if entry.SelectElement("auth-provider") != nil {
		t.Fatal("a password session must not keep the AWS IAM authentication method")
	}
	assertChild(t, entry, "user-name", "app_user")
}
