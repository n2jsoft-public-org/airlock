package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

type directListOutcome int

const (
	directListOpen directListOutcome = iota
	// directListPicked opens the review of the selected database.
	directListPicked
	// directListSwitch shows the running session of a connected database.
	directListSwitch
	// directListAdd opens the form of a new database.
	directListAdd
	// directListEdit opens the form of the selected database.
	directListEdit
	// directListDelete deletes the selected database, confirmed with y.
	directListDelete
	// directListBack leaves the list (esc without any filter to clear).
	directListBack
)

// directListChrome counts the lines under the list: the delete confirmation.
const directListChrome = 1

// directItem is one direct database of the list.
type directItem struct {
	db        directdb.Database
	connected bool
}

func (i directItem) isProduction() bool { return i.db.Production }

func (i directItem) Title() string {
	if i.connected {
		return i.db.Name + "  ● connected"
	}
	return i.db.Name
}

func (i directItem) Description() string {
	parts := []string{string(i.db.Engine), i.db.Address()}
	if i.db.Database != "" {
		parts = append(parts, i.db.Database)
	}
	return strings.Join(parts, " · ")
}

func (i directItem) FilterValue() string {
	return strings.Join(strings.Fields(strings.Join([]string{i.db.Name, string(i.db.Engine), i.db.Host, i.db.Database}, " ")), " ")
}

// directList is the target step of a direct database: the definitions of saved-connections.json, or why
// they cannot be read.
type directList struct {
	// err is the store's error, shown in place of the list.
	err  error
	list list.Model
	// confirming waits for y to delete the selected database.
	confirming bool
}

// newDirectList lists dbs; active holds the connectionKey of the running sessions (see
// directConnectionKey), shown as connected.
func newDirectList(styles Styles, dbs []directdb.Database, err error, active map[string]bool) directList {
	items := make([]list.Item, 0, len(dbs))
	for _, db := range dbs {
		items = append(items, directItem{db: db, connected: active[directConnectionKey(db.Name)]})
	}
	return directList{list: newWizardList(styles, items), err: err}
}

// SetSize fits the list and its confirmation line in width x height.
func (p *directList) SetSize(width, height int) {
	p.list.SetSize(width, maxInt(height-directListChrome, 3))
}

// typing reports whether printable keys belong to the list filter.
func (p directList) typing() bool {
	return p.list.SettingFilter()
}

func (p directList) confirmingDelete() bool {
	return p.confirming
}

// count is the number of databases listed.
func (p directList) count() int {
	return len(p.list.Items())
}

func (p directList) selected() (directdb.Database, bool) {
	item, ok := p.list.SelectedItem().(directItem)
	return item.db, ok
}

func (p directList) selectedItem() (directItem, bool) {
	item, ok := p.list.SelectedItem().(directItem)
	return item, ok
}

// selectName selects the database name, when listed.
func (p *directList) selectName(name string) {
	for idx, item := range p.list.Items() {
		if entry, ok := item.(directItem); ok && entry.db.Name == name {
			p.list.Select(idx)
			return
		}
	}
}

// Update handles one message and reports what the user chose, if anything.
func (p directList) Update(msg tea.Msg) (directList, directListOutcome, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if isKey && p.confirming {
		p.confirming = false
		if key.String() == "y" {
			return p, directListDelete, nil
		}
		return p, directListOpen, nil
	}
	if isKey && !p.list.SettingFilter() {
		switch key.String() {
		case "a":
			return p, directListAdd, nil
		case "esc":
			// An applied filter is cleared by the list first.
			if !p.list.IsFiltered() {
				return p, directListBack, nil
			}
		case "enter":
			item, ok := p.selectedItem()
			switch {
			case !ok:
				return p, directListOpen, nil
			case item.connected:
				// A connected database is not opened twice: its running session is shown instead.
				return p, directListSwitch, nil
			default:
				return p, directListPicked, nil
			}
		case "e":
			if _, ok := p.selected(); ok {
				return p, directListEdit, nil
			}
			return p, directListOpen, nil
		case "x":
			if _, ok := p.selected(); ok {
				p.confirming = true
			}
			return p, directListOpen, nil
		}
	}
	if p.err != nil {
		return p, directListOpen, nil
	}

	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, directListOpen, cmd
}

// View renders the store error, the empty state, or the list and its delete confirmation.
func (p directList) View(styles Styles, width int) string {
	switch {
	case p.err != nil:
		return styles.Danger.Width(width).Render(p.err.Error())
	case p.count() == 0:
		return styles.Muted.Render(truncate("No direct database yet — press a to add one", width))
	}
	confirm := ""
	if db, ok := p.selected(); ok && p.confirming {
		confirm = styles.Warning.Render(truncate("Delete "+db.Name+"? y/n", width))
	}
	return p.list.View() + "\n" + confirm
}

// directDeletedMsg reports the deletion of a direct database, its connection prefs and its
// keychain password.
type directDeletedMsg struct {
	err  error
	name string
}

// enterDirectList shows the direct databases of the store.
func (m jumphostWizardModel) enterDirectList() (tea.Model, tea.Cmd) {
	m.options.Name = ""
	m = m.reloadDirectList("")
	m.step = jumphostStepTarget
	return m, nil
}

// reloadDirectList reads the store again, keeping selectName selected when it is still listed.
func (m jumphostWizardModel) reloadDirectList(selectName string) jumphostWizardModel {
	dbs, err := m.databases.List()
	m.directList = newDirectList(m.styles, dbs, err, m.active)
	m = m.fitLists()
	if selectName != "" {
		m.directList.selectName(selectName)
	}
	return m
}

// updateDirectListStep handles the direct database list.
func (m jumphostWizardModel) updateDirectListStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.directList.Update(msg)
	m.directList = picker
	switch outcome {
	case directListPicked:
		db, _ := picker.selected()
		return m.enterDirectReview(db)
	case directListSwitch:
		db, _ := picker.selected()
		m.canceled = true
		m.switchTo = directConnectionKey(db.Name)
		return m, m.finish()
	case directListDelete:
		db, _ := picker.selected()
		// The list waits for the deletion: the deleted database must not be picked meanwhile.
		m.loading = true
		m.errorMsg = ""
		return m, tea.Batch(m.deleteDirectCmd(db.Name), m.loadingUI.start("Deleting "+db.Name+"...", []string{
			"Tearing the page out of the address book...",
		}))
	case directListAdd:
		return m.openDirectForm(nil)
	case directListEdit:
		db, _ := picker.selected()
		return m.openDirectForm(&db)
	case directListBack:
		return m.goBack()
	default:
		return m, cmd
	}
}

// deleteDirectCmd forgets the connection prefs and keychain password of name, then deletes its
// definition: a failure to forget deletes nothing, so the password is never orphaned.
func (m jumphostWizardModel) deleteDirectCmd(name string) tea.Cmd {
	store, runner, ctx := m.databases, m.runner, m.ctx
	return func() tea.Msg {
		if err := runner.ForgetConnection(ctx, jumphost.DirectTargetRef(name)); err != nil {
			return directDeletedMsg{name: name, err: fmt.Errorf("its saved settings were not forgotten: %w", err)}
		}
		return directDeletedMsg{name: name, err: store.Delete(name)}
	}
}

// handleDirectDeleted lists the databases again once one is deleted.
func (m jumphostWizardModel) handleDirectDeleted(msg directDeletedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	m = m.reloadDirectList("")
	if msg.err != nil {
		m.errorMsg = "Could not delete " + msg.name + ": " + msg.err.Error()
	}
	return m, nil
}
