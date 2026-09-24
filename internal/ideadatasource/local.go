package ideadatasource

import (
	"fmt"
	"path/filepath"
)

const (
	localFileName  = "dataSources.local.xml"
	localComponent = "dataSourceStorageLocal"
)

// localChildOrder is the order in which the IDE writes per-user data source settings.
var localChildOrder = []string{
	"database-info",
	"auth-provider",
	"user-name",
	"schema-mapping",
}

// localSettingsPath returns the per-user settings file stored next to dataSources.xml.
func localSettingsPath(dataSourcesPath string) string {
	return filepath.Join(filepath.Dir(dataSourcesPath), localFileName)
}

// writeLocalSettings stores the per-user settings (authentication method, user name) of ds in
// dataSources.local.xml. It does nothing when ds carries no such setting.
func writeLocalSettings(path string, ds *DataSource) error {
	if ds.AuthProvider == "" && ds.User == "" {
		return nil
	}

	doc, err := loadDocument(path)
	if err != nil {
		return err
	}

	root := doc.Root()
	component := root.FindElement(fmt.Sprintf("./component[@name='%s']", localComponent))
	if component == nil {
		component = root.CreateElement("component")
		component.CreateAttr("name", localComponent)
	}

	uuid := stableUUID(ds.Key)
	element := component.FindElement(fmt.Sprintf("./data-source[@uuid='%s']", uuid))
	if element == nil {
		element = component.CreateElement("data-source")
		element.CreateAttr("name", ds.Name)
		element.CreateAttr("uuid", uuid)
	}
	element.CreateAttr("name", ds.Name)

	if ds.AuthProvider != "" {
		setChildText(element, "auth-provider", ds.AuthProvider)
	}
	if ds.User != "" {
		setChildText(element, "user-name", ds.User)
	}
	if element.SelectElement("schema-mapping") == nil {
		element.CreateElement("schema-mapping")
	}
	sortChildren(element, localChildOrder)

	return writeDocument(doc, path)
}
