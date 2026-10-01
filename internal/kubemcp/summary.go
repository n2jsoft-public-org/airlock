package kubemcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxResultBytes caps the JSON of the items a list tool returns.
const maxResultBytes = 256 << 10

// itemSummary is one object of a list, reduced to what tells its state.
type itemSummary struct {
	Status    map[string]any `json:"status,omitempty"`
	Name      string         `json:"name"`
	Namespace string         `json:"namespace,omitempty"`
	Age       string         `json:"age,omitempty"`
}

func summarize(kind string, item map[string]any, now time.Time) itemSummary {
	summary := itemSummary{
		Name:      str(item, "metadata", "name"),
		Namespace: str(item, "metadata", "namespace"),
		Status:    statusSummary(kind, item),
	}
	if created, err := time.Parse(time.RFC3339, str(item, "metadata", "creationTimestamp")); err == nil {
		summary.Age = humanAge(now.Sub(created))
	}
	return summary
}

func statusSummary(kind string, item map[string]any) map[string]any {
	switch kind {
	case "Pod":
		return podStatus(item)
	case "Deployment", "StatefulSet", "ReplicaSet":
		return map[string]any{"ready": fmt.Sprintf("%d/%d", num(item, "status", "readyReplicas"), num(item, "spec", "replicas"))}
	case "DaemonSet":
		return map[string]any{"ready": fmt.Sprintf("%d/%d", num(item, "status", "numberReady"), num(item, "status", "desiredNumberScheduled"))}
	case "Node":
		return nodeStatus(item)
	case "Service":
		return map[string]any{"type": str(item, "spec", "type"), "cluster_ip": str(item, "spec", "clusterIP"), "ports": servicePorts(item)}
	case "Job":
		return map[string]any{"succeeded": num(item, "status", "succeeded"), "failed": num(item, "status", "failed"), "active": num(item, "status", "active")}
	case "Secret":
		data := as[map[string]any](item["data"])
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return map[string]any{"type": str(item, "type"), "keys": keys}
	case "Namespace":
		return map[string]any{"phase": str(item, "status", "phase")}
	default:
		return nil
	}
}

func podStatus(pod map[string]any) map[string]any {
	statuses := as[[]any](field(pod, "status", "containerStatuses"))
	ready, restarts, reason := 0, int64(0), ""
	for _, raw := range statuses {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if as[bool](container["ready"]) {
			ready++
		}
		restarts += num(container, "restartCount")
		if waiting := str(container, "state", "waiting", "reason"); waiting != "" && reason == "" {
			reason = waiting
		}
	}
	status := map[string]any{
		"phase":    str(pod, "status", "phase"),
		"ready":    fmt.Sprintf("%d/%d", ready, len(statuses)),
		"restarts": restarts,
		"node":     str(pod, "spec", "nodeName"),
	}
	if reason != "" {
		status["reason"] = reason
	}
	return status
}

func nodeStatus(node map[string]any) map[string]any {
	ready := false
	conditions := as[[]any](field(node, "status", "conditions"))
	for _, raw := range conditions {
		if condition, ok := raw.(map[string]any); ok && str(condition, "type") == "Ready" {
			ready = str(condition, "status") == "True"
		}
	}
	roles := make([]string, 0)
	labels := as[map[string]any](field(node, "metadata", "labels"))
	for label := range labels {
		if role, found := strings.CutPrefix(label, "node-role.kubernetes.io/"); found && role != "" {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return map[string]any{"ready": ready, "roles": roles}
}

func servicePorts(service map[string]any) []string {
	ports := as[[]any](field(service, "spec", "ports"))
	out := make([]string, 0, len(ports))
	for _, raw := range ports {
		if port, ok := raw.(map[string]any); ok {
			out = append(out, fmt.Sprintf("%d/%s", num(port, "port"), str(port, "protocol")))
		}
	}
	return out
}

// humanAge is a kubectl-like age: 42s, 5m, 3h (up to 47h), then days.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// fitResult keeps the longest prefix of items whose JSON array fits in maxResultBytes.
func fitResult[T any](items []T) (kept []T, truncated bool) {
	size := 2
	for idx := range items {
		raw, err := json.Marshal(items[idx])
		if err != nil {
			return items[:idx], true
		}
		size += len(raw) + 1
		if size > maxResultBytes {
			return items[:idx], true
		}
	}
	return items, false
}

// field returns the value at path in obj, nil when any step is missing.
func field(obj map[string]any, path ...string) any {
	var current any = obj
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

func str(obj map[string]any, path ...string) string {
	value := as[string](field(obj, path...))
	return value
}

// num reads a JSON number (decoded as float64) at path; 0 when missing.
func num(obj map[string]any, path ...string) int64 {
	value := as[float64](field(obj, path...))
	return int64(value)
}

// as returns v as a T, the zero value when v holds another type.
func as[T any](v any) T {
	if typed, ok := v.(T); ok {
		return typed
	}
	var zero T
	return zero
}
