package kubemcp

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
)

// resourceType is a kind of object the API serves, as discovery lists it.
type resourceType struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Group      string `json:"group,omitempty"`
	Version    string `json:"version"`
	singular   string
	ShortNames []string `json:"short_names,omitempty"`
	Namespaced bool     `json:"namespaced"`
}

// path is the API path of the type's objects in namespace (every namespace when empty, ignored for
// cluster-scoped types), or of the object name when set.
func (t *resourceType) path(namespace, name string) string {
	path := "/apis/" + t.Group + "/" + t.Version
	if t.Group == "" {
		path = "/api/" + t.Version
	}
	if t.Namespaced && namespace != "" {
		path += "/namespaces/" + url.PathEscape(namespace)
	}
	path += "/" + t.Name
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	return path
}

type apiResourceList struct {
	GroupVersion string `json:"groupVersion"`
	Resources    []struct {
		Name         string   `json:"name"`
		SingularName string   `json:"singularName"`
		Kind         string   `json:"kind"`
		ShortNames   []string `json:"shortNames"`
		Verbs        []string `json:"verbs"`
		Namespaced   bool     `json:"namespaced"`
	} `json:"resources"`
}

// discovery lists the resource types of the cluster once per session.
type discovery struct {
	client *apiClient
	types  []resourceType
	mu     sync.Mutex
}

// all lists the readable resource types (get or list verbs, no subresources), sorted by group then
// name. Groups whose discovery fails, such as an unavailable aggregated API, are skipped. A cancelled or expired ctx fails the call instead, and nothing is cached.
func (d *discovery) all(ctx context.Context) ([]resourceType, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.types != nil {
		return d.types, nil
	}

	var core struct {
		Versions []string `json:"versions"`
	}
	if err := d.client.getJSON(ctx, "/api", nil, &core); err != nil {
		return nil, err
	}
	var groups struct {
		Groups []struct {
			PreferredVersion struct {
				GroupVersion string `json:"groupVersion"`
			} `json:"preferredVersion"`
		} `json:"groups"`
	}
	if err := d.client.getJSON(ctx, "/apis", nil, &groups); err != nil {
		return nil, err
	}

	types := make([]resourceType, 0)
	for _, version := range core.Versions {
		var list apiResourceList
		if err := d.client.getJSON(ctx, "/api/"+version, nil, &list); err != nil {
			return nil, err
		}
		types = append(types, readableTypes(&list)...)
	}
	for _, group := range groups.Groups {
		var list apiResourceList
		if err := d.client.getJSON(ctx, "/apis/"+group.PreferredVersion.GroupVersion, nil, &list); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		types = append(types, readableTypes(&list)...)
	}
	sort.SliceStable(types, func(i, j int) bool {
		if types[i].Group != types[j].Group {
			return types[i].Group < types[j].Group
		}
		return types[i].Name < types[j].Name
	})
	d.types = types
	return types, nil
}

func readableTypes(list *apiResourceList) []resourceType {
	group, version, found := strings.Cut(list.GroupVersion, "/")
	if !found {
		group, version = "", list.GroupVersion
	}
	var types []resourceType
	for _, r := range list.Resources {
		if strings.Contains(r.Name, "/") || (!slices.Contains(r.Verbs, "get") && !slices.Contains(r.Verbs, "list")) {
			continue
		}
		types = append(types, resourceType{
			Name: r.Name, Kind: r.Kind, Group: group, Version: version,
			singular: r.SingularName, ShortNames: r.ShortNames, Namespaced: r.Namespaced,
		})
	}
	return types
}

// resolve finds the type called name: its plural, singular, a short name or its kind, in any case.
// group ("core" for the core group) narrows the match; across groups the core group wins, else the
// name is ambiguous.
func (d *discovery) resolve(ctx context.Context, name, group string) (resourceType, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return resourceType{}, fmt.Errorf("resource is required, e.g. pods or deployments")
	}
	types, err := d.all(ctx)
	if err != nil {
		return resourceType{}, err
	}
	group = strings.ToLower(strings.TrimSpace(group))
	filterGroup := group != ""
	if group == "core" {
		group = ""
	}

	var matches []resourceType
	for _, t := range types {
		if filterGroup && t.Group != group {
			continue
		}
		if t.Name == want || t.singular == want || strings.EqualFold(t.Kind, want) || slices.Contains(t.ShortNames, want) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return resourceType{}, fmt.Errorf("unknown resource type %q: list_resource_types shows the types of this cluster", name)
	case 1:
		return matches[0], nil
	}
	groupNames := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.Group == "" {
			return m, nil
		}
		groupNames = append(groupNames, m.Group)
	}
	return resourceType{}, fmt.Errorf("resource type %q exists in several API groups (%s): set group", name, strings.Join(groupNames, ", "))
}
