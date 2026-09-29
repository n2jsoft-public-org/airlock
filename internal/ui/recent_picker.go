package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

type recentPickerOutcome int

const (
	recentPickerOpen recentPickerOutcome = iota
	recentPickerPicked
	recentPickerNew
	recentPickerSearch
	recentPickerForget
)

// Focus positions of the action rows under the list; recentActionNone is the list itself.
const (
	recentActionNone = iota - 1
	recentActionNew
	recentActionSearch
)

// recentPickerChrome counts the lines under the list: rule, two action rows and a notice.
const recentPickerChrome = 4

// connectionKey identifies a connection by profile, region, type and name, as the dashboard
// knows its sessions.
func connectionKey(profile, region, resourceType, name string) string {
	return strings.Join([]string{profile, region, resourceType, name}, "/")
}

// recentItem is one remembered connection of the start screen.
type recentItem struct {
	conn      jumphost.SavedConnection
	age       string
	connected bool
}

func (i recentItem) Title() string {
	if i.connected {
		return i.conn.Ref.Name + "  ● connected"
	}
	return i.conn.Ref.Name
}

func (i recentItem) Description() string {
	profile := i.conn.Prefs.Profile
	if i.conn.ProfileMissing {
		profile += " (missing)"
	}
	parts := []string{i.conn.Ref.Type, profile, i.conn.Ref.Region}
	if i.age != "" {
		parts = append(parts, i.age)
	}
	return strings.Join(parts, " · ")
}

func (i recentItem) FilterValue() string {
	return strings.Join([]string{i.conn.Ref.Name, i.conn.Ref.Type, i.conn.Prefs.Profile, i.conn.Ref.Region}, " ")
}

// recentPicker is the start screen: the remembered connections, then the "New connection" and
// "Search a target" actions.
type recentPicker struct {
	notice string
	list   list.Model
	// action is the focused action row, or recentActionNone while the list has the focus.
	action int
	// confirming waits for y to forget the selected entry.
	confirming bool
}

func newRecentPicker(styles Styles, connections []jumphost.SavedConnection, active map[string]bool, now time.Time) recentPicker {
	items := make([]list.Item, 0, len(connections))
	for _, conn := range connections {
		items = append(items, recentItem{
			conn:      conn,
			age:       relativeAge(now, conn.Prefs.UpdatedAt),
			connected: active[connectionKey(conn.Prefs.Profile, conn.Ref.Region, conn.Ref.Type, conn.Ref.Name)],
		})
	}
	return recentPicker{list: newWizardList(styles, items), action: recentActionNone}
}

// SetSize fits the list and the action rows in width x height.
func (p *recentPicker) SetSize(width, height int) {
	p.list.SetSize(width, maxInt(height-recentPickerChrome, 3))
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
	key, isKey := msg.(tea.KeyMsg)
	if isKey && p.confirming {
		p.confirming = false
		if key.String() == "y" {
			return p, recentPickerForget, nil
		}
		return p, recentPickerOpen, nil
	}
	if isKey && !p.list.SettingFilter() {
		p.notice = ""
		switch key.String() {
		case "n":
			return p, recentPickerNew, nil
		case "s":
			return p, recentPickerSearch, nil
		}
		if p.action != recentActionNone {
			return p.updateActions(key)
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
			if item.connected {
				p.notice = item.conn.Ref.Name + " is already connected"
				return p, recentPickerOpen, nil
			}
			return p, recentPickerPicked, nil
		case "d":
			if _, ok := p.selected(); ok {
				p.confirming = true
			}
			return p, recentPickerOpen, nil
		}
	}

	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, recentPickerOpen, cmd
}

func (p recentPicker) updateActions(key tea.KeyMsg) (recentPicker, recentPickerOutcome, tea.Cmd) {
	switch key.String() {
	case "up", "k", "shift+tab":
		p.action--
	case "down", "j", "tab":
		p.action = min(p.action+1, recentActionSearch)
	case "esc":
		p.action = recentActionNone
	case "enter":
		if p.action == recentActionNew {
			return p, recentPickerNew, nil
		}
		return p, recentPickerSearch, nil
	}
	return p, recentPickerOpen, nil
}

// View renders the list, the action rows and the confirmation or notice line.
func (p recentPicker) View(styles Styles, width int) string {
	lines := []string{
		p.list.View(),
		styles.Rule.Render(strings.Repeat("─", min(width, resourceCardWidth))),
		p.actionRow(styles, recentActionNew, "+ New connection", "n", width),
		p.actionRow(styles, recentActionSearch, "⌕ Search a target", "s", width),
	}
	switch item, _ := p.selected(); {
	case p.confirming:
		lines = append(lines, styles.Warning.Render(truncate("Forget "+item.conn.Ref.Name+"? y/n", width)))
	case p.notice != "":
		lines = append(lines, styles.Muted.Render(truncate(p.notice, width)))
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
