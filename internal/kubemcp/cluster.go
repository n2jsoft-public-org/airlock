package kubemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListLimit = 100
	maxListLimit     = 500
	defaultTailLines = 200
	maxTailLines     = 2000
	// maxObjectBytes refuses single objects too large to be useful to an assistant.
	maxObjectBytes = 1 << 20
	// maxEventPages bounds the pages of maxListLimit events list_events sorts (5000 events).
	maxEventPages = 10
)

// moreEventsNote tells list_events callers the events were sorted among the first pages only.
const moreEventsNote = "more than 5000 events: newest first among the first 5000 returned by the API; narrow with namespace, kind or name"

type listResourcesInput struct {
	Resource      string `json:"resource" jsonschema:"resource type: plural (pods), short name (deploy) or kind (Deployment); custom resources included"`
	Group         string `json:"group,omitempty" jsonschema:"API group (core for the core group), only needed when the name exists in several groups"`
	Namespace     string `json:"namespace,omitempty" jsonschema:"namespace; empty lists every namespace"`
	LabelSelector string `json:"label_selector,omitempty" jsonschema:"label selector, e.g. app=web,tier!=db"`
	FieldSelector string `json:"field_selector,omitempty" jsonschema:"field selector, e.g. status.phase=Running"`
	Continue      string `json:"continue,omitempty" jsonschema:"continue token of a previous call, to get the next items"`
	Limit         int    `json:"limit,omitempty" jsonschema:"maximum number of items (default 100, max 500)"`
}

type listResourcesOutput struct {
	Kind      string        `json:"kind"`
	Continue  string        `json:"continue,omitempty"`
	Items     []itemSummary `json:"items"`
	Truncated bool          `json:"truncated"`
}

type getResourceInput struct {
	Resource  string `json:"resource" jsonschema:"resource type: plural, short name or kind"`
	Group     string `json:"group,omitempty" jsonschema:"API group, only needed when the name exists in several groups"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace of the object; required for namespaced types"`
	Name      string `json:"name" jsonschema:"object name"`
}

type podLogsInput struct {
	Namespace    string `json:"namespace" jsonschema:"namespace of the pod"`
	Pod          string `json:"pod" jsonschema:"pod name"`
	Container    string `json:"container,omitempty" jsonschema:"container name; required when the pod has several"`
	TailLines    int    `json:"tail_lines,omitempty" jsonschema:"number of last lines (default 200, max 2000)"`
	SinceSeconds int    `json:"since_seconds,omitempty" jsonschema:"only lines newer than this many seconds"`
	Previous     bool   `json:"previous,omitempty" jsonschema:"logs of the previous instance of the container, e.g. after a crash"`
}

type podLogsOutput struct {
	Log       string `json:"log"`
	Truncated bool   `json:"truncated"`
}

type listEventsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace; empty lists every namespace"`
	Kind      string `json:"kind,omitempty" jsonschema:"only events of objects of this kind, e.g. Pod"`
	Name      string `json:"name,omitempty" jsonschema:"only events of the object with this name"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of events, newest first (default 100, max 500)"`
}

type namespaceSummary struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
}

type eventSummary struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Object  string `json:"object"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

// apiCluster reads a cluster through its API.
type apiCluster struct {
	client    *apiClient
	discovery *discovery
	now       func() time.Time
}

func newAPICluster(client *apiClient) *apiCluster {
	return &apiCluster{client: client, discovery: &discovery{client: client}, now: time.Now}
}

func (c *apiCluster) Namespaces(ctx context.Context) ([]namespaceSummary, error) {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.client.getJSON(ctx, "/api/v1/namespaces", nil, &list); err != nil {
		return nil, err
	}
	namespaces := make([]namespaceSummary, 0, len(list.Items))
	for _, item := range list.Items {
		namespaces = append(namespaces, namespaceSummary{Name: str(item, "metadata", "name"), Phase: str(item, "status", "phase")})
	}
	return namespaces, nil
}

func (c *apiCluster) ResourceTypes(ctx context.Context) ([]resourceType, error) {
	return c.discovery.all(ctx)
}

func (c *apiCluster) List(ctx context.Context, in *listResourcesInput) (listResourcesOutput, error) {
	t, err := c.discovery.resolve(ctx, in.Resource, in.Group)
	if err != nil {
		return listResourcesOutput{}, err
	}
	if !t.Namespaced && in.Namespace != "" {
		return listResourcesOutput{}, fmt.Errorf("%s are not namespaced: leave namespace empty", t.Name)
	}
	query := url.Values{"limit": {strconv.Itoa(clamp(in.Limit, defaultListLimit, maxListLimit))}}
	setQuery(query, "labelSelector", in.LabelSelector)
	setQuery(query, "fieldSelector", in.FieldSelector)
	setQuery(query, "continue", in.Continue)

	var list map[string]any
	if err := c.client.getJSON(ctx, t.path(in.Namespace, ""), query, &list); err != nil {
		return listResourcesOutput{}, err
	}
	redactSecrets(list)
	items := as[[]any](list["items"])
	if isSecrets(&t) {
		// Defence in depth: the summaries never expose values, but redact even if the response omitted its kind.
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				redactSecret(item)
			}
		}
	}
	now := c.now()
	summaries := make([]itemSummary, 0, len(items))
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			summaries = append(summaries, summarize(t.Kind, item, now))
		}
	}
	kept, truncated := fitResult(summaries)
	out := listResourcesOutput{Kind: t.Kind, Items: kept, Truncated: truncated}
	// A truncated page has no valid continue token: the caller narrows the request instead.
	if !truncated {
		out.Continue = str(list, "metadata", "continue")
	}
	return out, nil
}

func (c *apiCluster) Get(ctx context.Context, in *getResourceInput) (map[string]any, error) {
	t, err := c.discovery.resolve(ctx, in.Resource, in.Group)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("name is required")
	}
	if t.Namespaced && strings.TrimSpace(in.Namespace) == "" {
		return nil, fmt.Errorf("namespace is required for %s", t.Name)
	}
	raw, err := c.client.get(ctx, t.path(in.Namespace, in.Name), nil)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxObjectBytes {
		return nil, fmt.Errorf("%s/%s is %d KiB, over the 1 MiB limit: use list_resources", t.Name, in.Name, len(raw)>>10)
	}
	var obj map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // keeps integers over 2^53 exact
	if err := decoder.Decode(&obj); err != nil {
		return nil, fmt.Errorf("unexpected answer from the Kubernetes API: %w", err)
	}
	if metadata, ok := obj["metadata"].(map[string]any); ok {
		delete(metadata, "managedFields")
	}
	redactSecrets(obj)
	if isSecrets(&t) {
		redactSecret(obj)
	}
	return obj, nil
}

func (c *apiCluster) Logs(ctx context.Context, in *podLogsInput) (podLogsOutput, error) {
	if strings.TrimSpace(in.Namespace) == "" || strings.TrimSpace(in.Pod) == "" {
		return podLogsOutput{}, errors.New("namespace and pod are required")
	}
	query := url.Values{
		"tailLines":  {strconv.Itoa(clamp(in.TailLines, defaultTailLines, maxTailLines))},
		"limitBytes": {strconv.Itoa(maxResultBytes)},
	}
	setQuery(query, "container", in.Container)
	if in.SinceSeconds > 0 {
		query.Set("sinceSeconds", strconv.Itoa(in.SinceSeconds))
	}
	if in.Previous {
		query.Set("previous", "true")
	}
	path := "/api/v1/namespaces/" + url.PathEscape(in.Namespace) + "/pods/" + url.PathEscape(in.Pod) + "/log"
	raw, err := c.client.get(ctx, path, query)
	if err != nil {
		return podLogsOutput{}, err
	}
	return podLogsOutput{Log: string(raw), Truncated: len(raw) >= maxResultBytes}, nil
}

// Events returns the newest events first, cut at maxResultBytes at an event boundary. The API does
// not sort events: up to maxEventPages pages are fetched, then sorted; more is true when pages were
// left, so the newest returned are only the newest of those fetched.
func (c *apiCluster) Events(ctx context.Context, in *listEventsInput) (events []eventSummary, more bool, err error) {
	path := "/api/v1/events"
	if in.Namespace != "" {
		path = "/api/v1/namespaces/" + url.PathEscape(in.Namespace) + "/events"
	}
	query := url.Values{"limit": {strconv.Itoa(maxListLimit)}}
	var selectors []string
	if in.Kind != "" {
		selectors = append(selectors, "involvedObject.kind="+in.Kind)
	}
	if in.Name != "" {
		selectors = append(selectors, "involvedObject.name="+in.Name)
	}
	setQuery(query, "fieldSelector", strings.Join(selectors, ","))

	for page := 0; page < maxEventPages; page++ {
		var list struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []map[string]any `json:"items"`
		}
		if err := c.client.getJSON(ctx, path, query, &list); err != nil {
			return nil, false, err
		}
		for _, item := range list.Items {
			events = append(events, eventSummary{
				Time:    eventTime(item),
				Type:    str(item, "type"),
				Reason:  str(item, "reason"),
				Object:  str(item, "involvedObject", "kind") + "/" + str(item, "involvedObject", "name"),
				Message: str(item, "message"),
				Count:   num(item, "count"),
			})
		}
		more = list.Metadata.Continue != ""
		if !more {
			break
		}
		query.Set("continue", list.Metadata.Continue)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time > events[j].Time })
	if limit := clamp(in.Limit, defaultListLimit, maxListLimit); len(events) > limit {
		events = events[:limit]
	}
	events, _ = fitResult(events)
	return events, more, nil
}

// eventTime is the latest time an event carries, as RFC 3339 in UTC so times sort as strings.
func eventTime(item map[string]any) string {
	for _, path := range [][]string{{"lastTimestamp"}, {"eventTime"}, {"firstTimestamp"}, {"metadata", "creationTimestamp"}} {
		if at, err := time.Parse(time.RFC3339Nano, str(item, path...)); err == nil {
			return at.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// isSecrets is true for the core Secret type, whose values are never returned.
func isSecrets(t *resourceType) bool {
	return t.Group == "" && t.Name == "secrets"
}

// clamp is requested, else fallback when unset, at most limit.
func clamp(requested, fallback, limit int) int {
	switch {
	case requested <= 0:
		return fallback
	case requested > limit:
		return limit
	default:
		return requested
	}
}

func setQuery(query url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		query.Set(key, value)
	}
}
