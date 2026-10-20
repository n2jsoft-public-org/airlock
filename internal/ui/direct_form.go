package ui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
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
	formFieldEnvironment
	// formFieldCount is the number of fields.
	formFieldCount
)

// formFieldNone marks a form without invalid field.
const formFieldNone = -1

// formLabelKeys are the labels of the direct database form.
var formLabelKeys = []string{
	"ui.direct_form.name", "ui.direct_form.engine", "ui.direct_form.host", "ui.jumphost_flow_view.port",
	"ui.direct_form.database", "ui.jumphost_flow_view.user", "ui.direct_form.tls", "ui.direct_form.environment",
}

// formLabelWidth fits the longest form label of the language with two spaces, at least the 13
// columns of English ("Environment").
func formLabelWidth() int {
	return labelWidth(13, 2, formLabelKeys)
}

// formFieldsByName maps the fields named by a directdb.FieldError to the form's.
var formFieldsByName = map[string]int{
	"name": formFieldName, "engine": formFieldEngine, "host": formFieldHost, "port": formFieldPort,
	"database": formFieldDatabase, "user": formFieldUser, "tls": formFieldTLS, "environment": formFieldEnvironment,
}

// directEngines are the engines in the order the form cycles them.
var directEngines = []directdb.Engine{directdb.EnginePostgres, directdb.EngineSQLServer, directdb.EngineMySQL, directdb.EngineMariaDB}

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
	errMsg      string
	name        textinput.Model
	host        textinput.Model
	port        textinput.Model
	dbName      textinput.Model
	user        textinput.Model
	focus       int
	errField    int
	environment string
	envs        environment.Set
}

// newDirectForm opens the form on previous, or on a new PostgreSQL database when nil.
func newDirectForm(previous *directdb.Database, envs environment.Set) directForm {
	f := directForm{
		envs:     envs,
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
		f.engine, f.tls, f.environment = previous.Engine, previous.TLS, previous.Environment
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
	f.port.Placeholder = i18n.Format("ui.direct_form.default", directdb.DefaultPort(f.engine))
	f.dbName.Placeholder = i18n.T("ui.hint.login_default")
	if f.engine == directdb.EnginePostgres {
		f.dbName.Placeholder = i18n.T("ui.direct_form.postgres_default")
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
	return f.focus == formFieldEngine || f.focus == formFieldTLS || f.focus == formFieldEnvironment
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

// toggle changes the focused choice: delta steps through the engines, the TLS modes and the environments.
func (f directForm) toggle(delta int) directForm {
	switch f.focus {
	case formFieldEngine:
		count := len(directEngines)
		current := slices.Index(directEngines, f.engine)
		f.engine = directEngines[((current+delta)%count+count)%count]
		f = f.withPlaceholders()
	case formFieldTLS:
		count := len(directTLSModes)
		current := slices.Index(directTLSModes, f.effectiveTLS())
		f.tls = directTLSModes[((current+delta)%count+count)%count]
	case formFieldEnvironment:
		// The cycle is none, the production under its alias (a teammate's production may be named
		// otherwise), then the other configured environments; an unknown name leaves for none.
		choices := append([]string{"", environment.ProductionAlias}, f.envs.Names()[1:]...)
		current := f.environment
		if f.envs.Lookup(current).Production {
			current = environment.ProductionAlias
		}
		next := 0
		if at := slices.Index(choices, current); at >= 0 {
			count := len(choices)
			next = ((at+delta)%count + count) % count
		}
		f.environment = choices[next]
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
			if f.focus == formFieldEnvironment {
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
		User: f.user.Value(), TLS: f.tls, Environment: f.environment,
	}
	if port := strings.TrimSpace(f.port.Value()); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil {
			return directdb.Database{}, &directdb.FieldError{Field: "port", Message: i18n.T("ui.direct_form.must_be_number")}
		}
		db.Port = value
	}
	if f.envs.Lookup(f.environment).Production {
		// The production environment is always saved under its alias, whatever its name.
		db.Environment = environment.ProductionAlias
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
	f.errField, f.errMsg = formFieldNone, i18n.Localize(err)
	var fieldErr *directdb.FieldError
	switch {
	case errors.As(err, &fieldErr):
		if field, ok := formFieldsByName[fieldErr.Field]; ok {
			f.errField, f.errMsg = field, i18n.Localize(fieldErr)
		}
	case errors.Is(err, directdb.ErrExists):
		f.errField = formFieldName
		f.errMsg = i18n.Format("ui.direct_form.name_exists", strings.TrimSpace(f.name.Value()))
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
	engines := make([]string, 0, len(directEngines))
	for _, engine := range directEngines {
		engines = append(engines, string(engine))
	}
	tlsModes := make([]string, 0, len(directTLSModes))
	for _, mode := range directTLSModes {
		tlsModes = append(tlsModes, string(mode))
	}

	var rows []string
	add := func(field int, label, value, help string) {
		rows = append(rows, f.row(styles, field, label, value, help, inner)...)
	}
	add(formFieldName, i18n.T("ui.direct_form.name"), inputView(f.name), i18n.T("ui.direct_form.names_mcp_server_ide"))
	add(formFieldEngine, i18n.T("ui.direct_form.engine"), choiceSelector(styles, engines, string(f.engine)), i18n.T("ui.direct_form.or_space_change_engine"))
	add(formFieldHost, i18n.T("ui.direct_form.host"), inputView(f.host), i18n.T("ui.direct_form.host_name_or_ip"))
	add(formFieldPort, i18n.T("ui.jumphost_flow_view.port"), inputView(f.port), i18n.T("ui.direct_form.leave_empty_for_engine"))
	add(formFieldDatabase, i18n.T("ui.direct_form.database"), inputView(f.dbName), i18n.T("ui.direct_form.leave_empty_for_default"))
	add(formFieldUser, i18n.T("ui.jumphost_flow_view.user"), inputView(f.user), i18n.T("ui.direct_form.its_password_is_asked"))
	add(formFieldTLS, i18n.T("ui.direct_form.tls"), choiceSelector(styles, tlsModes, string(f.effectiveTLS())), i18n.T("ui.direct_form.require_encrypts_verify_also"))
	add(formFieldEnvironment, i18n.T("ui.direct_form.environment"), f.environmentValue(styles), i18n.T("ui.direct_form.or_space_change_it"))
	return styles.Card.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// row renders one field: the accent marker and help line when focused, its error when invalid.
func (f directForm) row(styles Styles, field int, label, value, help string, width int) []string {
	labelStyle := styles.FieldLabel.Width(formLabelWidth())
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

// environmentValue renders the database's environment as its badge, or none.
func (f directForm) environmentValue(styles Styles) string {
	env := f.envs.Lookup(f.environment)
	if env.IsZero() {
		return styles.Muted.Render(i18n.T("ui.direct_form.none"))
	}
	value := styles.EnvBadge(env)
	if !env.Configured {
		value += styles.Faint.Render(i18n.T("ui.direct_form.not_in_your_environments"))
	}
	return value
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
	m.directForm = newDirectForm(previous, m.environments)
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
	return m, tea.Batch(m.saveDirectCmd(m.directForm.previousName, m.directForm.previous, db), m.loadingUI.start(i18n.Format("ui.saving", db.Name), []string{
		i18n.T("ui.direct_form.writing_it_in_address"),
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
				return directSavedMsg{name: db.Name, err: i18n.NewError("ui.direct_form.database_2", db.Name, directdb.ErrExists)}
			}
		}
		if previousName != "" && directdb.ConnectionChanged(&previous, &db) {
			if err := runner.ForgetConnection(ctx, jumphost.DirectTargetRef(previousName)); err != nil {
				return directSavedMsg{name: db.Name, err: i18n.NewError("ui.direct_form.choices_remembered_for_were", previousName, err)}
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
			m.errorMsg = i18n.Format("ui.direct_form.could_not_save", msg.name, msg.err)
		}
		return m, nil
	}
	m = m.reloadDirectList(msg.name)
	m.step = jumphostStepTarget
	return m, nil
}
