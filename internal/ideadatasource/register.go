// Package ideadatasource registers port-forwarded connections as IntelliJ data sources.
package ideadatasource

import (
	"crypto/sha1" // #nosec G505 -- SHA-1 is mandated by RFC 4122 UUIDv5, not used for security.
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/beevik/etree"
)

const (
	ideaDirName         = ".idea"
	dataSourcesFileName = "dataSources.xml"
	managerComponent    = "DataSourceManagerImpl"
)

// uuidNamespace is the RFC 4122 URL namespace, used to derive stable data source UUIDs.
var uuidNamespace = [16]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// DataSource describes a connection to register in the IDE project.
type DataSource struct {
	// Properties are written as JDBC additional properties (e.g. AWS IAM settings).
	Properties map[string]string
	// AuthProvider (e.g. "aws.rds.iam") and User are per-user settings stored in dataSources.local.xml.
	// A User without AuthProvider signs in with the IDE default, user & password.
	AuthProvider string
	User         string
	// Key uniquely identifies the target; the same key always maps to the same IDE entry.
	Key      string
	Name     string
	Engine   string
	Host     string
	Database string
	Port     int
}

// Result reports what Register did.
type Result struct {
	Name       string
	Paths      []string
	Registered bool
}

// UnregisterResult reports what Unregister did.
type UnregisterResult struct {
	// ActiveSessions is the number of other running sessions still using the data source.
	ActiveSessions int
	Removed        bool
}

// Register adds or updates ds in <projectDir>/.idea/dataSources.xml and records a lease for the
// current process. It does nothing when projectDir has no .idea directory or the engine is not supported.
func Register(projectDir string, ds *DataSource) (Result, error) {
	paths := dataSourcesPaths(projectDir)
	if len(paths) == 0 {
		return Result{}, nil
	}
	driver, ok := DriverForEngine(ds.Engine)
	if !ok {
		return Result{}, nil
	}
	if strings.TrimSpace(ds.Key) == "" {
		return Result{}, errors.New("data source key is required")
	}

	store, err := openLeaseStore(projectDir, ds.Key)
	if err != nil {
		return Result{}, err
	}
	unlock, err := store.lock()
	if err != nil {
		return Result{}, err
	}

	result, err := registerLocked(paths, ds, driver, store)
	return result, errors.Join(err, unlock())
}

func registerLocked(paths []string, ds *DataSource, driver Driver, store leaseStore) (Result, error) {
	for _, path := range paths {
		if err := updateDocument(path, func(component *etree.Element) {
			upsertDataSource(component, ds, driver)
		}); err != nil {
			return Result{}, err
		}
		if err := writeLocalSettings(localSettingsPath(path), ds); err != nil {
			return Result{}, err
		}
	}
	if err := store.acquire(ds); err != nil {
		return Result{}, err
	}

	return Result{Registered: true, Paths: paths, Name: ds.Name}, nil
}

// Unregister releases the current process lease on the data source registered with key.
// The entry is removed from <projectDir>/.idea/dataSources.xml only when no other running session
// uses it; otherwise it is pointed back at the most recent remaining session.
// Per-user settings in dataSources.local.xml are kept so that changes made in the IDE (schemas, ...)
// are restored when the same key is registered again.
func Unregister(projectDir, key string) (UnregisterResult, error) {
	paths := dataSourcesPaths(projectDir)
	if len(paths) == 0 {
		return UnregisterResult{}, nil
	}

	store, err := openLeaseStore(projectDir, key)
	if err != nil {
		return UnregisterResult{}, err
	}
	unlock, err := store.lock()
	if err != nil {
		return UnregisterResult{}, err
	}

	result, err := unregisterLocked(paths, key, store)
	return result, errors.Join(err, unlock())
}

func unregisterLocked(paths []string, key string, store leaseStore) (UnregisterResult, error) {
	if err := store.release(); err != nil {
		return UnregisterResult{}, err
	}
	remaining, err := store.active()
	if err != nil {
		return UnregisterResult{}, err
	}

	if len(remaining) > 0 {
		for _, path := range paths {
			if restoreErr := restoreDataSource(path, &remaining[0].DataSource); restoreErr != nil {
				return UnregisterResult{}, restoreErr
			}
		}
		return UnregisterResult{ActiveSessions: len(remaining)}, nil
	}

	result := UnregisterResult{}
	for _, path := range paths {
		removed, removeErr := removeDataSource(path, key)
		if removeErr != nil {
			return UnregisterResult{}, removeErr
		}
		result.Removed = result.Removed || removed
	}
	return result, nil
}

// restoreDataSource points the entry back at a session that is still running.
func restoreDataSource(path string, ds *DataSource) error {
	driver, ok := DriverForEngine(ds.Engine)
	if !ok {
		return nil
	}
	return updateDocument(path, func(component *etree.Element) {
		upsertDataSource(component, ds, driver)
	})
}

// dataSourcesPaths returns the dataSources.xml files of the IDE project in projectDir.
// Rider nests its project configuration in .idea/.idea.<SolutionName>/.idea; other
// IntelliJ-based IDEs use .idea directly.
func dataSourcesPaths(projectDir string) []string {
	ideaDir := filepath.Join(projectDir, ideaDirName)
	info, err := os.Stat(ideaDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	riderDirs, err := filepath.Glob(filepath.Join(ideaDir, ideaDirName+".*", ideaDirName))
	if err != nil {
		return nil
	}
	var paths []string
	for _, dir := range riderDirs {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			paths = append(paths, filepath.Join(dir, dataSourcesFileName))
		}
	}
	if len(paths) > 0 {
		return paths
	}

	return []string{filepath.Join(ideaDir, dataSourcesFileName)}
}

func updateDocument(path string, update func(component *etree.Element)) error {
	doc, err := loadDocument(path)
	if err != nil {
		return err
	}
	update(managerElement(doc))
	return writeDocument(doc, path)
}

func removeDataSource(path, key string) (bool, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if doc.Root() == nil {
		return false, nil
	}

	query := fmt.Sprintf("./component[@name='%s']/data-source[@uuid='%s']", managerComponent, stableUUID(key))
	element := doc.Root().FindElement(query)
	if element == nil {
		return false, nil
	}
	element.Parent().RemoveChild(element)

	if err := writeDocument(doc, path); err != nil {
		return false, err
	}
	return true, nil
}

func loadDocument(path string) (*etree.Document, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromFile(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to read %s: %w", path, err)
		}
		doc = etree.NewDocument()
		doc.CreateProcInst("xml", `version="1.0" encoding="UTF-8"`)
	}

	if doc.Root() == nil {
		project := doc.CreateElement("project")
		project.CreateAttr("version", "4")
	}

	return doc, nil
}

func managerElement(doc *etree.Document) *etree.Element {
	root := doc.Root()
	component := root.FindElement(fmt.Sprintf("./component[@name='%s']", managerComponent))
	if component == nil {
		component = root.CreateElement("component")
		component.CreateAttr("name", managerComponent)
		component.CreateAttr("format", "xml")
		component.CreateAttr("multifile-model", "true")
	}

	return component
}

func upsertDataSource(component *etree.Element, ds *DataSource, driver Driver) {
	uuid := stableUUID(ds.Key)
	element := component.FindElement(fmt.Sprintf("./data-source[@uuid='%s']", uuid))
	if element == nil {
		element = component.CreateElement("data-source")
		element.CreateAttr("source", "LOCAL")
		element.CreateAttr("name", ds.Name)
		element.CreateAttr("uuid", uuid)
		setChildText(element, "synchronize", "true")
		setChildText(element, "working-dir", "$ProjectFileDir$")
	}

	element.CreateAttr("name", ds.Name)
	setChildText(element, "driver-ref", driver.Ref)
	// Earlier versions set configured-by-url, which switches the IDE to "URL only" mode.
	if configuredByURL := element.SelectElement("configured-by-url"); configuredByURL != nil {
		element.RemoveChild(configuredByURL)
	}
	setChildText(element, "jdbc-driver", driver.ClassName)
	setChildText(element, "jdbc-url", driver.URL(ds.Host, ds.Port, ds.Database))
	setProperties(element, ds.Properties)
	sortChildren(element, dataSourceChildOrder)
}

// dataSourceChildOrder is the order in which the IDE writes data source settings. The IDE reads them
// in document order and derives some properties (e.g. the AWS RDS signing host) from the JDBC URL,
// so properties placed before <jdbc-url> are reset when the entry is loaded.
var dataSourceChildOrder = []string{
	"driver-ref",
	"synchronize",
	"jdbc-driver",
	"jdbc-url",
	"jdbc-additional-properties",
	"working-dir",
}

// sortChildren moves the known settings to the front in IDE order and keeps any other child after them.
func sortChildren(element *etree.Element, order []string) {
	ordered := make([]*etree.Element, 0, len(order))
	for _, tag := range order {
		if child := element.SelectElement(tag); child != nil {
			element.RemoveChild(child)
			ordered = append(ordered, child)
		}
	}
	for index, child := range ordered {
		element.InsertChildAt(index, child)
	}
}

// setProperties sets the given JDBC additional properties, leaving other properties untouched.
func setProperties(element *etree.Element, properties map[string]string) {
	if len(properties) == 0 {
		return
	}

	container := element.SelectElement("jdbc-additional-properties")
	if container == nil {
		container = element.CreateElement("jdbc-additional-properties")
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		property := container.FindElement(fmt.Sprintf("./property[@name='%s']", name))
		if property == nil {
			property = container.CreateElement("property")
			property.CreateAttr("name", name)
		}
		property.CreateAttr("value", properties[name])
	}
}

func setChildText(parent *etree.Element, tag, value string) {
	child := parent.SelectElement(tag)
	if child == nil {
		child = parent.CreateElement(tag)
	}
	child.SetText(value)
}

func writeDocument(doc *etree.Document, path string) error {
	doc.Indent(2)

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmp.Name()

	if err := writeAndReplace(doc, tmp, path, mode); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return err
	}

	return nil
}

func writeAndReplace(doc *etree.Document, tmp *os.File, path string, mode os.FileMode) error {
	if _, err := doc.WriteTo(tmp); err != nil {
		return errors.Join(fmt.Errorf("failed to write %s: %w", path, err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to replace %s: %w", path, err)
	}

	return nil
}

// stableUUID derives an RFC 4122 version 5 UUID from key.
func stableUUID(key string) string {
	hash := sha1.New() // #nosec G401 -- see import comment.
	hash.Write(uuidNamespace[:])
	hash.Write([]byte(key))
	sum := hash.Sum(nil)

	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
