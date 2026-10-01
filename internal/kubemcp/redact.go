package kubemcp

// redactedValue replaces every Secret value returned to AI assistants.
const redactedValue = "<redacted>"

// lastAppliedAnnotation holds a copy of the applied object, Secret data included.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactSecrets replaces, in place, the values of obj when it is a Secret, and of every Secret
// item when it is a list. The items of a SecretList carry no kind.
func redactSecrets(obj map[string]any) {
	kind := as[string](obj["kind"])
	if kind == "Secret" {
		redactSecret(obj)
		return
	}
	items, ok := obj["items"].([]any)
	if !ok {
		return
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemKind := as[string](item["kind"])
		if itemKind == "Secret" || (itemKind == "" && kind == "SecretList") {
			redactSecret(item)
		}
	}
}

func redactSecret(secret map[string]any) {
	for _, name := range []string{"data", "stringData"} {
		values, ok := secret[name].(map[string]any)
		if !ok {
			continue
		}
		for key := range values {
			values[key] = redactedValue
		}
	}
	annotations, ok := field(secret, "metadata", "annotations").(map[string]any)
	if !ok {
		return
	}
	if _, found := annotations[lastAppliedAnnotation]; found {
		annotations[lastAppliedAnnotation] = redactedValue
	}
}
