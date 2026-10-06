package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

type recentPickerOutcome int

const (
	recentPickerOpen recentPickerOutcome = iota
	recentPickerPicked
	recentPickerNew
	recentPickerSearch
	recentPickerForget
	// recentPickerBack leaves the start screen (esc without any filter to clear).
	recentPickerBack
	// recentPickerSwitch shows the running session of a connected entry.
	recentPickerSwitch
	// recentPickerSettings opens the Settings screen.
	recentPickerSettings
)

// Focus positions of the action rows under the list; recentActionNone is the list itself.
const (
	recentActionNone = iota - 1
	recentActionNew
	recentActionSearch
	recentActionSettings
)

// recentPickerChrome counts the lines under the list: rule, two action rows and a notice.
const recentPickerChrome = 4

// connectionKey identifies a connection by profile, region, type and name, as the dashboard
// knows its sessions.
func connectionKey(profile, region, resourceType, name string) string {
	return strings.Join([]string{profile, region, resourceType, name}, "/")
}

// directConnectionKey identifies a direct database connection, which has no profile or region.
func directConnectionKey(name string) string {
	return connectionKey("", "", sessions.DirectResourceType, name)
}

// recentItem is one remembered connection of the start screen.
type recentItem struct {
	age string
	// db is the definition of a direct entry, when its store could be read.
	db        directdb.Database
	conn      jumphost.SavedConnection
	connected bool
	// production reports that the entry's profile, or its direct database, is flagged as production.
	production bool
}

func (i recentItem) isProduction() bool { return i.production }

// direct reports whether the entry is a direct database, which has no profile nor region.
func (i recentItem) direct() bool { return i.conn.Ref.Type == sessions.DirectResourceType }

// isDimmed fades a direct entry whose definition was removed.
func (i recentItem) isDimmed() bool { return i.conn.TargetMissing }

func (i recentItem) Title() string {
	if i.connected {
		return i.conn.Ref.Name + "  ● connected"
	}
	return i.conn.Ref.Name
}

func (i recentItem) Description() string {
	var parts []string
	switch {
	case i.direct() && i.conn.TargetMissing:
		parts = []string{sessions.DirectResourceType, "definition removed"}
	case i.direct() && i.db.Host != "":
		parts = []string{sessions.DirectResourceType, i.db.Address()}
	case i.direct():
		parts = []string{sessions.DirectResourceType}
	default:
		profile := i.conn.Prefs.Profile
		if i.conn.ProfileMissing {
			profile += " (missing)"
		}
		parts = []string{profile, i.conn.Ref.Region}
	}
	if i.age != "" {
		parts = append(parts, i.age)
	}
	return strings.Join(parts, " · ")
}

// itemType is the resource type of the entry, shown as a badge.
func (i recentItem) itemType() string { return i.conn.Ref.Type }

// key identifies the entry as the dashboard knows its sessions (see connectionKey).
func (i recentItem) key() string {
	if i.direct() {
		return directConnectionKey(i.conn.Ref.Name)
	}
	return connectionKey(i.conn.Prefs.Profile, i.conn.Ref.Region, i.conn.Ref.Type, i.conn.Ref.Name)
}

func (i recentItem) FilterValue() string {
	if i.direct() {
		return strings.Join(strings.Fields(strings.Join([]string{i.conn.Ref.Name, i.conn.Ref.Type, i.db.Host}, " ")), " ")
	}
	return strings.Join([]string{i.conn.Ref.Name, i.conn.Ref.Type, i.conn.Prefs.Profile, i.conn.Ref.Region}, " ")
}

// recentPicker is the start screen: the remembered connections, then the "New connection" and
// "Search a target" actions.
type recentPicker struct {
	list list.Model
	// action is the focused action row, or recentActionNone while the list has the focus.
	action int
	// confirming waits for y to forget the selected entry.
	confirming bool
	// settings offers the Settings row and its shortcut.
	settings bool
}

// newRecentPicker lists connections; direct holds the definitions of the direct databases by name
// (nil when unknown), which give their address and production flag.
func newRecentPicker(styles Styles, connections []jumphost.SavedConnection, active map[string]bool, production productionMatcher, direct map[string]directdb.Database, now time.Time) recentPicker {
	items := make([]list.Item, 0, len(connections))
	for idx := range connections {
		item := recentItem{conn: connections[idx], age: relativeAge(now, connections[idx].Prefs.UpdatedAt)}
		item.connected = active[item.key()]
		if item.direct() {
			// Profile patterns never apply to a database outside AWS.
			item.db = direct[item.conn.Ref.Name]
			item.production = item.db.Production
		} else {
			item.production = production.matches(item.conn.Prefs.Profile)
		}
		items = append(items, item)
	}
	l := newWizardList(styles, items)
	l.Title = "⌕ type to filter"
	return recentPicker{list: l, action: recentActionNone}
}

// recentShortcuts are the keys of the start screen and its list, which never start the filter.
const recentShortcuts = "nsdqjkhlgG?/,"

// startsFilter reports whether key is a printable character that is not a shortcut.
func startsFilter(key tea.KeyPressMsg) bool {
	runes := []rune(key.Text)
	if len(runes) != 1 || key.Mod.Contains(tea.ModAlt) || key.Mod.Contains(tea.ModCtrl) {
		return false
	}
	r := runes[0]
	return unicode.IsPrint(r) && !unicode.IsSpace(r) && !strings.ContainsRune(recentShortcuts, r)
}

// SetSize fits the list and the action rows in width x height.
func (p *recentPicker) SetSize(width, height int) {
	chrome := recentPickerChrome
	if p.settings {
		chrome++
	}
	p.list.SetSize(width, maxInt(height-chrome, 3))
}

// typing reports whether printable keys belong to the list filter.
func (p recentPicker) typing() bool {
	return p.list.SettingFilter()
}

func (p recentPicker) confirmingForget() bool {
	return p.confirming
}

func (p recentPicker) selected() (recentItem, bool) {
	item, ok := p.list.SelectedItem().(recentItem)
	return item, ok
}

// Update handles one message and reports what the user chose, if anything.
func (p recentPicker) Update(msg tea.Msg) (recentPicker, recentPickerOutcome, tea.Cmd) {
	key, isKey := msg.(tea.KeyPressMsg)
	if isKey && p.confirming {
		p.confirming = false
		if key.String() == "y" {
			return p, recentPickerForget, nil
		}
		return p, recentPickerOpen, nil
	}
	if isKey && !p.list.SettingFilter() {
		switch key.String() {
		case "n":
			return p, recentPickerNew, nil
		case "s":
			return p, recentPickerSearch, nil
		case ",":
			if p.settings {
				return p, recentPickerSettings, nil
			}
			return p, recentPickerOpen, nil
		}
		if p.action != recentActionNone {
			return p.updateActions(key)
		}
		// A letter that is not a shortcut starts the filter with it.
		if startsFilter(key) {
			p.list, _ = p.list.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
			var cmd tea.Cmd
			p.list, cmd = p.list.Update(msg)
			return p, recentPickerOpen, cmd
		}
		switch key.String() {
		case "down", "j":
			if p.list.Index() >= len(p.list.VisibleItems())-1 {
				p.action = recentActionNew
				return p, recentPickerOpen, nil
			}
		case "enter":
			item, ok := p.selected()
			if !ok {
				return p, recentPickerOpen, nil
			}
			// A connected entry is not opened twice: its running session is shown instead.
			if item.connected {
				return p, recentPickerSwitch, nil
			}
			return p, recentPickerPicked, nil
		case "d":
			if _, ok := p.selected(); ok {
				p.confirming = true
			}
			return p, recentPickerOpen, nil
		case "esc":
			// An applied filter is cleared by the list first.
			if !p.list.IsFiltered() {
				return p, recentPickerBack, nil
			}
		}
	}

	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, recentPickerOpen, cmd
}

func (p recentPicker) updateActions(key tea.KeyPressMsg) (recentPicker, recentPickerOutcome, tea.Cmd) {
	last := recentActionSearch
	if p.settings {
		last = recentActionSettings
	}
	switch key.String() {
	case "up", "k", "shift+tab":
		p.action--
	case "down", "j", "tab":
		p.action = min(p.action+1, last)
	case "esc":
		p.action = recentActionNone
	case "enter":
		switch p.action {
		case recentActionNew:
			return p, recentPickerNew, nil
		case recentActionSettings:
			return p, recentPickerSettings, nil
		default:
			return p, recentPickerSearch, nil
		}
	}
	return p, recentPickerOpen, nil
}

// View renders the list, the action rows and the forget confirmation line.
func (p recentPicker) View(styles Styles, width int) string {
	lines := []string{
		p.list.View(),
		styles.Rule.Render(strings.Repeat("─", min(width, resourceCardWidth))),
		p.actionRow(styles, recentActionNew, "+ New connection", "n", width),
		p.actionRow(styles, recentActionSearch, "⌕ Search a target", "s", width),
	}
	if p.settings {
		lines = append(lines, p.actionRow(styles, recentActionSettings, "⚙ Settings", ",", width))
	}
	switch item, _ := p.selected(); {
	case p.confirming:
		lines = append(lines, styles.Warning.Render(truncate("Forget "+item.conn.Ref.Name+"? y/n", width)))
	default:
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (p recentPicker) actionRow(styles Styles, action int, label, shortcut string, width int) string {
	if p.action == action {
		return truncate(styles.CardMarker.Render("▌ ")+styles.Text.Bold(true).Render(label)+styles.Faint.Render("  "+shortcut), width)
	}
	return truncate("  "+styles.Text.Render(label)+styles.Faint.Render("  "+shortcut), width)
}

// relativeAge renders how long ago at was, e.g. "3h ago"; "" for an unknown time.
func relativeAge(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	age := now.Sub(at)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}
