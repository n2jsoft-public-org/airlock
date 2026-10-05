package ui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// Fields of the direct database form, in focus order.
const (
	formFieldName = iota
	formFieldEngine
	formFieldHost
	formFieldPort
	formFieldDatabase
	formFieldUser
	formFieldTLS
	formFieldProduction
	// formFieldCount is the number of fields.
	formFieldCount
)

// formFieldNone marks a form without invalid field.
const formFieldNone = -1

// formLabelWidth fits the longest label, "Production".
const formLabelWidth = 12

// formFieldsByName maps the fields named by a directdb.FieldError to the form's.
var formFieldsByName = map[string]int{
	"name": formFieldName, "engine": formFieldEngine, "host": formFieldHost, "port": formFieldPort,
	"database": formFieldDatabase, "user": formFieldUser, "tls": formFieldTLS, "production": formFieldProduction,
}

// directTLSModes are the TLS modes in the order the form cycles them.
var directTLSModes = []directdb.TLSMode{directdb.TLSRequire, directdb.TLSVerify, directdb.TLSDisable}

type directFormOutcome int

const (
	directFormEditing directFormOutcome = iota
	// directFormSubmit asks to validate and save the form.
	directFormSubmit
	// directFormCancel leaves the form without saving.
	directFormCancel
)

// directForm adds a direct database, or edits the one named previousName.
type directForm struct {
	// previous is the edited database, previousName its name; zero and "" for a new one.
	previous     directdb.Database
	previousName string
	engine       directdb.Engine
	// tls is the mode as the definition writes it: "" leaves the default (require) out of the file.
	tls directdb.TLSMode
	// errMsg explains why errField is invalid. With formFieldNone, a non-empty errMsg is a store
	// failure, which the wizard shows above the form.
	errMsg     string
	name       textinput.Model
	host       textinput.Model
	port       textinput.Model
	dbName     textinput.Model
	user       textinput.Model
	focus      int
	errField   int
	production bool
}

// newDirectForm opens the form on previous, or on a new PostgreSQL database when nil.
func newDirectForm(previous *directdb.Database) directForm {
	f := directForm{
		engine:   directdb.EnginePostgres,
		errField: formFieldNone,
		name:     newFormInput("legacy-billing", 63),
		host:     newFormInput("db.internal", 253),
		port:     newFormInput("", 5),
		dbName:   newFormInput("", 128),
		user:     newFormInput("app_ro", 128),
	}
	if previous != nil {
		f.previous, f.previousName = *previous, previous.Name
		f.engine, f.tls, f.production = previous.Engine, previous.TLS, previous.Production
		setFormValue(&f.name, previous.Name)
		setFormValue(&f.host, previous.Host)
		if previous.Port != 0 {
			setFormValue(&f.port, strconv.Itoa(previous.Port))
		}
		setFormValue(&f.dbName, previous.Database)
		setFormValue(&f.user, previous.User)
	}
	return f.withPlaceholders().focusField(formFieldName)
}

func newFormInput(placeholder string, limit int) textinput.Model {
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Placeholder = placeholder
	input.Prompt = ""
	input.CharLimit = limit
	return input
}

// setFormValue fills input with value, the cursor at its end.
func setFormValue(input *textinput.Model, value string) {
	input.SetValue(value)
	input.CursorEnd()
}

// withPlaceholders shows the engine's defaults in the empty port and database fields.
func (f directForm) withPlaceholders() directForm {
	f.port.Placeholder = fmt.Sprintf("%d (default)", directdb.DefaultPort(f.engine))
	f.dbName.Placeholder = "login default"
	if f.engine == directdb.EnginePostgres {
		f.dbName.Placeholder = "postgres (default)"
	}
	return f
}

// effectiveTLS is the mode connections use: require when the definition leaves it out.
func (f directForm) effectiveTLS() directdb.TLSMode {
	if f.tls == "" {
		return directdb.TLSRequire
	}
	return f.tls
}

// toggleFocused reports whether the focused field is a choice, changed with space or the arrows.
func (f directForm) toggleFocused() bool {
	return f.focus == formFieldEngine || f.focus == formFieldTLS || f.focus == formFieldProduction
}

// input returns the text input of field; nil for the choices.
func (f *directForm) input(field int) *textinput.Model {
	switch field {
	case formFieldName:
		return &f.name
	case formFieldHost:
		return &f.host
	case formFieldPort:
		return &f.port
	case formFieldDatabase:
		return &f.dbName
	case formFieldUser:
		return &f.user
	default:
		return nil
	}
}

// focusField moves the cursor to field.
func (f directForm) focusField(field int) directForm {
	for _, input := range []*textinput.Model{&f.name, &f.host, &f.port, &f.dbName, &f.user} {
		input.Blur()
	}
	f.focus = field
	if input := f.input(field); input != nil {
		input.Focus()
	}
	return f
}

// toggle changes the focused choice: delta steps through the TLS modes; the engine and the
// production flag have two values, any delta swaps them.
func (f directForm) toggle(delta int) directForm {
	switch f.focus {
	case formFieldEngine:
		if f.engine == directdb.EngineSQLServer {
			f.engine = directdb.EnginePostgres
		} else {
			f.engine = directdb.EngineSQLServer
		}
		f = f.withPlaceholders()
	case formFieldTLS:
		count := len(directTLSModes)
		current := slices.Index(directTLSModes, f.effectiveTLS())
		f.tls = directTLSModes[((current+delta)%count+count)%count]
	case formFieldProduction:
		f.production = !f.production
	default:
		// The text fields have no choice to toggle.
		return f
	}
	if f.errField == f.focus {
		f.errField, f.errMsg = formFieldNone, ""
	}
	return f
}

// Update handles one message and reports whether the user submitted or canceled the form.
func (f directForm) Update(msg tea.Msg) (directForm, directFormOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return f, directFormCancel, nil
		case "ctrl+s":
			return f, directFormSubmit, nil
		case "enter":
			if f.focus == formFieldProduction {
				return f, directFormSubmit, nil
			}
			return f.focusField(f.focus + 1), directFormEditing, nil
		case "tab", "down":
			return f.focusField((f.focus + 1) % formFieldCount), directFormEditing, nil
		case "shift+tab", "up":
			return f.focusField((f.focus - 1 + formFieldCount) % formFieldCount), directFormEditing, nil
		}
		if f.toggleFocused() {
			switch key.String() {
			case "space", "right", "l":
				f = f.toggle(1)
			case "left", "h":
				f = f.toggle(-1)
			}
			return f, directFormEditing, nil
		}
	}

	input := f.input(f.focus)
	if input == nil {
		return f, directFormEditing, nil
	}
	before := input.Value()
	var cmd tea.Cmd
	*input, cmd = input.Update(msg)
	// Editing the invalid field clears its error.
	if input.Value() != before && f.errField == f.focus {
		f.errField, f.errMsg = formFieldNone, ""
	}
	return f, directFormEditing, cmd
}

// database is the definition the form describes, normalized and validated; a *directdb.FieldError
// names the invalid field.
func (f directForm) database() (directdb.Database, error) {
	db := directdb.Database{
		Name: f.name.Value(), Engine: f.engine, Host: f.host.Value(), Database: f.dbName.Value(),
		User: f.user.Value(), TLS: f.tls, Production: f.production,
	}
	if port := strings.TrimSpace(f.port.Value()); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil {
			return directdb.Database{}, &directdb.FieldError{Field: "port", Message: "must be a number"}
		}
		db.Port = value
	}
	db.Normalize()
	if err := db.Validate(); err != nil {
		return directdb.Database{}, err
	}
	return db, nil
}

// withError shows err under its field, focused, for an invalid field or a name already taken;
// any other error is a store failure (errField formFieldNone) the wizard shows above the form.
func (f directForm) withError(err error) directForm {
	f.errField, f.errMsg = formFieldNone, err.Error()
	var fieldErr *directdb.FieldError
	switch {
	case errors.As(err, &fieldErr):
		if field, ok := formFieldsByName[fieldErr.Field]; ok {
			f.errField, f.errMsg = field, fieldErr.Error()
		}
	case errors.Is(err, directdb.ErrExists):
		f.errField = formFieldName
		f.errMsg = "name: a database named " + strings.TrimSpace(f.name.Value()) + " already exists"
	}
	if f.errField != formFieldNone {
		f = f.focusField(f.errField)
	}
	return f
}

// View renders the fields: the focused one with its help line, the invalid one with its error.
func (f directForm) View(styles Styles, width int) string {
	panelWidth := min(width, reviewPanelWidth)
	// Border (2) and padding (2) around the panel content.
	inner := panelWidth - 4
	tlsModes := make([]string, 0, len(directTLSModes))
	for _, mode := range directTLSModes {
		tlsModes = append(tlsModes, string(mode))
	}

	var rows []string
	add := func(field int, label, value, help string) {
		rows = append(rows, f.row(styles, field, label, value, help, inner)...)
	}
	add(formFieldName, "Name", inputView(f.name), "Names the MCP server, the IDE data source and the keychain entry.")
	add(formFieldEngine, "Engine", choiceSelector(styles, []string{string(directdb.EnginePostgres), string(directdb.EngineSQLServer)}, string(f.engine)), "←/→ or space change the engine.")
	add(formFieldHost, "Host", inputView(f.host), "Host name or IP address, without scheme or port.")
	add(formFieldPort, "Port", inputView(f.port), "Leave empty for the engine's default.")
	add(formFieldDatabase, "Database", inputView(f.dbName), "Leave empty for the default database.")
	add(formFieldUser, "User", inputView(f.user), "Its password is asked when connecting, never saved here.")
	add(formFieldTLS, "TLS", choiceSelector(styles, tlsModes, string(f.effectiveTLS())), "require encrypts; verify also checks the certificate; disable is for local databases.")
	add(formFieldProduction, "Production", f.productionToggle(styles), "Space toggles. On, the wizard and the dashboard flag it PROD.")
	return styles.Card.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// row renders one field: the accent marker and help line when focused, its error when invalid.
func (f directForm) row(styles Styles, field int, label, value, help string, width int) []string {
	labelStyle := styles.FieldLabel.Width(formLabelWidth)
	var rows []string
	if field == f.focus {
		rows = append(rows,
			styles.CardMarker.Render("▌ ")+labelStyle.Foreground(styles.AccentColor).Render(label)+value,
			"  "+styles.Faint.Render(truncate(help, width-2)),
		)
	} else {
		rows = append(rows, "  "+labelStyle.Render(label)+value)
	}
	if field == f.errField && f.errMsg != "" {
		rows = append(rows, "  "+styles.Danger.Render(truncate("✗ "+f.errMsg, width-2)))
	}
	return rows
}

// productionToggle renders whether the database is flagged as production.
func (f directForm) productionToggle(styles Styles) string {
	if f.production {
		return styles.Warning.Render("● production") + styles.Faint.Render("  flagged PROD")
	}
	return styles.Muted.Render("○ no")
}

// choiceSelector renders options, the chosen one highlighted.
func choiceSelector(styles Styles, options []string, chosen string) string {
	parts := make([]string, 0, len(options))
	for _, option := range options {
		if option == chosen {
			parts = append(parts, styles.Success.Render("● "+option))
			continue
		}
		parts = append(parts, styles.Muted.Render(option))
	}
	return strings.Join(parts, styles.Faint.Render(" │ "))
}

// directSavedMsg reports the save of the form's database.
type directSavedMsg struct {
	err  error
	name string
}

// openDirectForm opens the form on previous, or on a new database when nil.
func (m jumphostWizardModel) openDirectForm(previous *directdb.Database) (tea.Model, tea.Cmd) {
	m.directForm = newDirectForm(previous)
	m.step = jumphostStepDirectForm
	m.errorMsg = ""
	return m, textinput.Blink
}

// updateDirectFormStep handles the form: esc returns to the list, a submit validates then saves.
func (m jumphostWizardModel) updateDirectFormStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	form, outcome, cmd := m.directForm.Update(msg)
	m.directForm = form
	switch outcome {
	case directFormCancel:
		m.step = jumphostStepTarget
		m.errorMsg = ""
		return m, nil
	case directFormSubmit:
		return m.submitDirectForm()
	default:
		return m, cmd
	}
}

// submitDirectForm saves the form's database once it is valid; an invalid field gets the focus
// and its error, and nothing is saved.
func (m jumphostWizardModel) submitDirectForm() (tea.Model, tea.Cmd) {
	db, err := m.directForm.database()
	if err != nil {
		m.directForm = m.directForm.withError(err)
		return m, nil
	}
	m.errorMsg = ""
	// The form waits for the save: a second submit must not write it twice.
	m.loading = true
	return m, tea.Batch(m.saveDirectCmd(m.directForm.previousName, m.directForm.previous, db), m.loadingUI.start("Saving "+db.Name+"...", []string{
		"Writing it in the address book...",
	}))
}

// saveDirectCmd writes db in place of previousName, previous its former definition. A change of
// any connection field (all but production) forgets the connection prefs and keychain password of
// the former name first: a rename leaves them unreachable, another user or server must not get
// them. A failure to forget saves nothing, and neither does a rename onto an existing name, which
// must leave the source's remembered choices alone.
func (m jumphostWizardModel) saveDirectCmd(previousName string, previous, db directdb.Database) tea.Cmd {
	store, runner, ctx := m.databases, m.runner, m.ctx
	return func() tea.Msg {
		if previousName != "" && db.Name != previousName {
			_, found, err := store.Get(db.Name)
			if err != nil {
				return directSavedMsg{name: db.Name, err: err}
			}
			if found {
				return directSavedMsg{name: db.Name, err: fmt.Errorf("database %q %w", db.Name, directdb.ErrExists)}
			}
		}
		if previousName != "" && directdb.ConnectionChanged(&previous, &db) {
			if err := runner.ForgetConnection(ctx, jumphost.DirectTargetRef(previousName)); err != nil {
				return directSavedMsg{name: db.Name, err: fmt.Errorf("the choices remembered for %s were not forgotten: %w", previousName, err)}
			}
		}
		return directSavedMsg{name: db.Name, err: store.Put(previousName, db)}
	}
}

// handleDirectSaved returns to the list on the saved database, or to the form on a failure.
func (m jumphostWizardModel) handleDirectSaved(msg directSavedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	if msg.err != nil {
		m.directForm = m.directForm.withError(msg.err)
		if m.directForm.errField == formFieldNone {
			m.errorMsg = "Could not save " + msg.name + ": " + msg.err.Error()
		}
		return m, nil
	}
	m = m.reloadDirectList(msg.name)
	m.step = jumphostStepTarget
	return m, nil
}
