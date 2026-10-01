package uninstall

import "strings"

// removePathEntry returns the Windows PATH list without dir, and whether dir was in it. Entries
// compare as Windows does: ignoring case and a trailing separator.
func removePathEntry(list, dir string) (string, bool) {
	wanted := strings.TrimRight(dir, `\/`)
	entries := strings.Split(list, ";")
	kept := make([]string, 0, len(entries))
	found := false
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		if strings.EqualFold(strings.TrimRight(entry, `\/`), wanted) {
			found = true
			continue
		}
		kept = append(kept, entry)
	}
	if !found {
		return list, false
	}
	return strings.Join(kept, ";"), true
}
