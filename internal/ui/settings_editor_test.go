package ui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft/airlock/internal/aiclient"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// testSetting describes name as the store does, with its label and description from the catalog.
func testSetting(name, section string, kind SettingKind, value string) SettingView {
	return SettingView{
		Name: name, Section: section, Kind: kind, Value: value,
		Label: i18n.T("config.label." + name), Description: i18n.T("config.key." + name),
	}
}

// testOptions are the options of a choice, labelled from the catalog.
func testOptions(name string, values ...string) []SettingOption {
	options := make([]SettingOption, 0, len(values))
	for _, value := range values {
		prefix := "config.option." + name + "." + value
		options = append(options, SettingOption{Value: value, Label: i18n.T(prefix), Help: i18n.T(prefix + ".help")})
	}
	return options
}

// testSettings is a snapshot of the 22 keys, in the order of the file. Its sections are, by index:
// 0 general, 1 aws, 2 mcp, 3 agent, 4 daemon, 5 modules.sql, 6 k8s, 7 cache, 8 dynamodb, 9 s3.
// mcp.clients is overridden by a flag, aws.jumphostTag is locked and modules.s3 is off.
func testSettings() SettingsSnapshot {
	language := testSetting("language", "general", KindChoice, "auto")
	language.Options, language.NextLaunch = testOptions("language", "auto", "en", "fr"), true
	for idx := range language.Options {
		language.Options[idx].Help = ""
	}
	org := testSetting("organizationConfig", "general", KindText, "https://github.com/acme/airlock-config/blob/main/org.json")
	org.FromFile, org.Optional = true, true
	mode := testSetting("mode", "general", KindChoice, "cli")
	mode.Options, mode.NextLaunch = testOptions("mode", "cli", "daemon"), true
	tag := testSetting("aws.jumphostTag", "aws", KindTag, "Usage=jumphost")
	tag.Locked, tag.FromOrganization = true, true
	clients := testSetting("mcp.clients", "mcp", KindClients, "claude:user,vscode")
	clients.FromFile, clients.OverriddenBy = true, "--mcp-clients"
	gateway := testSetting("mcp.gateway.enabled", "mcp", KindToggle, "true")
	gateway.NextLaunch = true
	port := testSetting("mcp.gateway.port", "mcp", KindPort, "7821")
	port.NextLaunch = true
	autoConnect := testSetting("agent.autoConnect", "agent", KindChoice, "none")
	autoConnect.Options = testOptions("agent.autoConnect", "none", "non-prod", "all")
	daemonIdle := testSetting("daemon.idleTimeout", "daemon", KindDuration, "8h")
	daemonIdle.NextLaunch = true
	dbUser := testSetting("modules.sql.defaultDbUser", "modules.sql", KindText, "app_ro")
	dbUser.FromFile, dbUser.Optional = true, true
	secretTag := testSetting("modules.sql.readOnlySecretTag", "modules.sql", KindTag, "")
	secretTag.Optional = true
	s3 := testSetting("modules.s3.enabled", "modules.s3", KindToggle, "false")
	s3.FromFile = true
	return SettingsSnapshot{
		Path: "/home/me/.config/airlock/config.json",
		Settings: []SettingView{
			language, org, mode,
			testSetting("environments", "general", KindEnvironments, "prod!=*prod*"),
			testSetting("aws.defaultRegion", "aws", KindRegion, ""),
			tag,
			testSetting("aws.randomLocalPort", "aws", KindToggle, "true"),
			testSetting("mcp.serverPrefix", "mcp", KindText, "airlock"),
			clients, gateway, port, autoConnect,
			testSetting("agent.writes", "agent", KindToggle, "false"),
			testSetting("agent.idleTimeout", "agent", KindDuration, "30m"),
			daemonIdle,
			testSetting("modules.sql.enabled", "modules.sql", KindToggle, "true"),
			dbUser, secretTag,
			testSetting("modules.k8s.enabled", "modules.k8s", KindToggle, "true"),
			testSetting("modules.cache.enabled", "modules.cache", KindToggle, "true"),
			testSetting("modules.dynamodb.enabled", "modules.dynamodb", KindToggle, "true"),
			s3,
		},
		Organization:    "github.com/acme/airlock-config/org.json",
		Environments:    environment.FromProductionProfiles([]string{"*prod*"}),
		DetectedClients: []aiclient.Client{aiclient.Claude, aiclient.Cursor},
	}
}

func noValidation(string, string) error { return nil }

func loadedEditor() settingsEditor {
	return newSettingsEditor(noValidation).loaded(testSettings(), nil)
}

func editorPress(t *testing.T, e settingsEditor, msgs ...tea.Msg) (settingsEditor, settingsOutcome) {
	t.Helper()
	outcome := settingsStay
	for _, msg := range msgs {
		e, outcome, _ = e.Update(msg)
	}
	return e, outcome
}

// editorKeys presses the named keys.
func editorKeys(t *testing.T, e settingsEditor, names ...string) (settingsEditor, settingsOutcome) {
	t.Helper()
	msgs := make([]tea.Msg, 0, len(names))
	for _, name := range names {
		msgs = append(msgs, key(name))
	}
	return editorPress(t, e, msgs...)
}

// editorAt focuses row of the section at index in e.
func editorAt(t *testing.T, e settingsEditor, section, row int) settingsEditor {
	t.Helper()
	for range section {
		e, _ = editorKeys(t, e, "down")
	}
	e, _ = editorKeys(t, e, "right")
	for range row {
		e, _ = editorKeys(t, e, "down")
	}
	return e
}

func selectedName(e settingsEditor) string {
	setting, _ := e.selected()
	return setting.Name
}

func editorView(e settingsEditor) string {
	return ansi.Strip(e.View(NewStyles(true), 120, 30))
}

func TestSettingsPanesNavigation(t *testing.T) {
	e, _ := editorKeys(t, loadedEditor(), "down", "down")
	if e.focus != focusSections || selectedName(e) != "mcp.serverPrefix" {
		t.Fatalf("focus = %v selected = %q, want the MCP section previewed", e.focus, selectedName(e))
	}
	if view := editorView(e); !strings.Contains(view, "Server prefix") || !strings.Contains(view, "Gateway port") {
		t.Fatalf("the right pane does not preview the MCP settings:\n%s", view)
	}
	e, _ = editorKeys(t, e, "right", "down")
	if e.focus != focusSettings || selectedName(e) != "mcp.clients" {
		t.Fatalf("focus = %v selected = %q, want mcp.clients", e.focus, selectedName(e))
	}
	if e, _ = editorKeys(t, e, "up"); selectedName(e) != "mcp.serverPrefix" {
		t.Fatalf("selected = %q, want mcp.serverPrefix", selectedName(e))
	}
	e, outcome := editorKeys(t, e, "esc")
	if outcome != settingsStay || e.focus != focusSections || e.section != 2 {
		t.Fatalf("esc: outcome = %v focus = %v section = %d, want the left pane on MCP", outcome, e.focus, e.section)
	}
	if e, _ = editorKeys(t, e, "tab"); e.focus != focusSettings {
		t.Fatal("tab does not move to the right pane")
	}
	if e, _ = editorKeys(t, e, "left"); e.focus != focusSections {
		t.Fatal("left does not move to the left pane")
	}
	if e, _ = editorKeys(t, e, "enter"); e.focus != focusSettings {
		t.Fatal("enter on a section does not move to the right pane")
	}
	if _, outcome = editorKeys(t, e, "esc", "esc"); outcome != settingsBack {
		t.Fatalf("esc on the left pane: outcome = %v, want back", outcome)
	}
}

func TestSettingsSectionsOrder(t *testing.T) {
	settings := []SettingView{
		{Name: "x.y", Section: "x"}, {Name: "agent.writes", Section: "agent"},
		{Name: "language", Section: "general"}, {Name: "modules.s3.enabled", Section: "modules.s3"},
	}
	var ids []string
	for _, section := range settingsSections(settings) {
		ids = append(ids, section.id)
	}
	if got := strings.Join(ids, " "); got != "general agent modules.s3 x" {
		t.Fatalf("sections = %q, want the known ones in order, then the unknown one", got)
	}
	view := ansi.Strip(newSettingsEditor(noValidation).loaded(SettingsSnapshot{Settings: settings}, nil).View(NewStyles(true), 120, 20))
	for _, want := range []string{"General", "Agent", "Modules", "S3", "x"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Daemon") {
		t.Fatalf("a section absent from the snapshot shows:\n%s", view)
	}
}

func TestSettingsToggleSavesInPlace(t *testing.T) {
	e := editorAt(t, loadedEditor(), 3, 1)
	e, outcome := editorKeys(t, e, "space")
	if outcome != settingsSave || e.requestKey != "agent.writes" || e.requestValue != "true" || e.open != nil {
		t.Fatalf("outcome = %v request = %q=%q open = %v, want true saved in place", outcome, e.requestKey, e.requestValue, e.open != nil)
	}
	if _, outcome = editorKeys(t, editorAt(t, loadedEditor(), 3, 1), "enter"); outcome != settingsSave {
		t.Fatalf("enter on a toggle: outcome = %v, want a save", outcome)
	}
}

func TestChoiceEditorPicksAnOption(t *testing.T) {
	e, _ := editorKeys(t, editorAt(t, loadedEditor(), 3, 0), "enter")
	if e.open == nil {
		t.Fatal("enter on agent.autoConnect opens no editor")
	}
	if view := editorView(e); !strings.Contains(view, "Non-production") || !strings.Contains(view, "Open targets outside the production environment without asking") {
		t.Fatalf("the choice editor lacks the options and their help:\n%s", view)
	}
	e, outcome := editorKeys(t, e, "down", "enter")
	if outcome != settingsSave || e.requestKey != "agent.autoConnect" || e.requestValue != "non-prod" {
		t.Fatalf("outcome = %v request = %q=%q, want non-prod saved", outcome, e.requestKey, e.requestValue)
	}
	// Picking the current value writes nothing.
	e, outcome = editorKeys(t, editorAt(t, loadedEditor(), 3, 0), "enter", "enter")
	if outcome != settingsStay || e.open != nil {
		t.Fatalf("outcome = %v open = %v, want the editor closed without a save", outcome, e.open != nil)
	}
}

func TestTextEditorValidatesAsYouType(t *testing.T) {
	validate := func(key, value string) error {
		if n, err := strconv.Atoi(value); key == "mcp.gateway.port" && err == nil && n > 65535 {
			return errors.New("invalid port " + value + " (valid: 1024-65535)")
		}
		return nil
	}
	e := editorAt(t, newSettingsEditor(validate).loaded(testSettings(), nil), 2, 3)
	e, _ = editorKeys(t, e, "enter")
	if e.open == nil || e.open.Value() != "7821" {
		t.Fatal("enter on mcp.gateway.port opens no field with its value")
	}
	e, _ = editorKeys(t, e, "ctrl+u", "7", "0")
	if view := editorView(e); strings.Contains(view, "invalid port") {
		t.Fatalf("70 shows an error:\n%s", view)
	}
	e, _ = editorKeys(t, e, "0", "0", "0")
	if view := editorView(e); !strings.Contains(view, "invalid port 70000 (valid: 1024-65535)") {
		t.Fatalf("70000 shows no error:\n%s", view)
	}
	e, outcome := editorKeys(t, e, "enter")
	if outcome != settingsStay || e.open == nil {
		t.Fatalf("enter on an invalid value: outcome = %v open = %v, want nothing saved", outcome, e.open != nil)
	}
	e, outcome = editorKeys(t, e, "backspace", "enter")
	if outcome != settingsSave || e.requestValue != "7000" {
		t.Fatalf("outcome = %v value = %q, want 7000 saved", outcome, e.requestValue)
	}
}

func TestTextEditorOptionalSavesEmpty(t *testing.T) {
	e, outcome := editorKeys(t, editorAt(t, loadedEditor(), 5, 1), "enter", "ctrl+u", "enter")
	if outcome != settingsSave || e.requestKey != "modules.sql.defaultDbUser" || e.requestValue != "" {
		t.Fatalf("outcome = %v request = %q=%q, want the empty value saved", outcome, e.requestKey, e.requestValue)
	}
}

func TestEditorRefusedWriteKeepsTheEditorOpen(t *testing.T) {
	e, outcome := editorKeys(t, editorAt(t, loadedEditor(), 2, 0), "enter", "ctrl+u", "acme", "enter")
	if outcome != settingsSave || e.requestValue != "acme" {
		t.Fatalf("outcome = %v value = %q, want acme saved", outcome, e.requestValue)
	}
	e = e.refused(errors.New("mcp.serverPrefix: refused by the store"))
	if e.open == nil || e.open.Value() != "acme" {
		t.Fatal("the refused editor closed or lost its value")
	}
	if view := editorView(e); !strings.Contains(view, "mcp.serverPrefix: refused by the store") {
		t.Fatalf("view lacks the refusal:\n%s", view)
	}
	if e = e.closeEdit(); e.open != nil || e.fieldErr != "" {
		t.Fatal("closeEdit keeps the editor or its error")
	}
}

func TestSettingsRowKeepsTheFlagMarker(t *testing.T) {
	e := editorAt(t, loadedEditor(), 2, 1)
	if view := ansi.Strip(e.View(NewStyles(true), 76, 14)); !strings.Contains(view, "file  ⚑ --mcp-clients") {
		t.Fatalf("the row lacks the flag marker at 80 columns:\n%s", view)
	}
	if view := editorView(e); !strings.Contains(view, "⚑ --mcp-clients for this run") {
		t.Fatalf("the selected row lacks the flag sentence:\n%s", view)
	}
	// The grid opens on the file value: checking the Codex project cell adds codex to it.
	e, outcome := editorKeys(t, e, "enter", "down", "down", "down", "space", "enter")
	if outcome != settingsSave || e.requestKey != "mcp.clients" || e.requestValue != "claude:user,vscode,codex" {
		t.Fatalf("outcome = %v request = %q=%q, want the file value edited", outcome, e.requestKey, e.requestValue)
	}
}

func TestSettingsUnreadableSnapshotIsReadOnly(t *testing.T) {
	e := newSettingsEditor(noValidation).loaded(SettingsSnapshot{}, errors.New("invalid config /x/config.json: unknown mode"))
	view := editorView(e)
	for _, want := range []string{"invalid config /x/config.json: unknown mode", "airlock config show", "airlock config init --force"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "General") || strings.Contains(view, "│") {
		t.Fatalf("an unreadable snapshot shows panes:\n%s", view)
	}
	for _, name := range []string{"right", "enter", "space", "d", "down"} {
		if pressed, outcome := editorKeys(t, e, name); pressed.open != nil || pressed.confirming || outcome != settingsStay {
			t.Fatalf("%s: open = %v confirming = %v outcome = %v, want nothing", name, pressed.open != nil, pressed.confirming, outcome)
		}
	}
	if _, outcome := editorKeys(t, e, "esc"); outcome != settingsBack {
		t.Fatalf("esc: outcome = %v, want back", outcome)
	}
}

func TestSettingsNarrowTerminalCutsLines(t *testing.T) {
	snapshot := testSettings()
	snapshot.Settings[8].Value = "claude:user,vscode,codex,cursor,gemini:user"
	snapshot.Settings[8].Description = strings.Repeat("a long description ", 12)
	e := newSettingsEditor(noValidation).loaded(snapshot, nil)
	for _, state := range []settingsEditor{e, editorAt(t, e, 2, 1), editorAt(t, e, 4, 0)} {
		view := state.View(NewStyles(true), 60, 14)
		for _, line := range strings.Split(view, "\n") {
			if width := lipgloss.Width(line); width > 60 {
				t.Fatalf("line of %d columns in a 60-column body: %q", width, ansi.Strip(line))
			}
		}
		if plain := ansi.Strip(view); !strings.Contains(plain, "…") || strings.Index(plain, "General") > strings.Index(plain, "│") {
			t.Fatalf("want the values cut and the panes in order:\n%s", plain)
		}
	}
	opened, _ := editorKeys(t, editorAt(t, e, 2, 1), "enter")
	for _, line := range strings.Split(opened.View(NewStyles(true), 60, 14), "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("editor line of %d columns in a 60-column body: %q", width, ansi.Strip(line))
		}
	}
}

func TestSettingsRightPaneScrolls(t *testing.T) {
	e := editorAt(t, loadedEditor(), 0, 3)
	view := ansi.Strip(e.View(NewStyles(true), 76, 8))
	if lines := strings.Split(view, "\n"); len(lines) != 8 {
		t.Fatalf("view of %d lines, want 8:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "Environments") || !strings.Contains(view, "/home/me/.config/airlock/config.json") {
		t.Fatalf("the selected row or the path is scrolled out:\n%s", view)
	}
}

func TestSettingsLockedRowOpensReadOnly(t *testing.T) {
	snapshot := testSettings()
	snapshot.Settings[11].Locked, snapshot.Settings[12].Locked = true, true
	e := newSettingsEditor(noValidation).loaded(snapshot, nil)
	toggle := editorAt(t, e, 3, 1)
	for _, name := range []string{"space", "enter", "d"} {
		if pressed, outcome := editorKeys(t, toggle, name); outcome != settingsStay || pressed.open != nil || pressed.confirming {
			t.Fatalf("%s on a locked toggle: outcome = %v open = %v confirming = %v", name, outcome, pressed.open != nil, pressed.confirming)
		}
	}
	choice, _ := editorKeys(t, editorAt(t, e, 3, 0), "enter")
	if choice.open == nil {
		t.Fatal("a locked choice opens no editor")
	}
	choice, outcome := editorKeys(t, choice, "down", "enter")
	if outcome != settingsStay || choice.open == nil {
		t.Fatalf("enter in a locked choice: outcome = %v open = %v, want nothing saved", outcome, choice.open != nil)
	}
	if view := editorView(choice); !strings.Contains(view, "Imposed by the organization (github.com/acme/airlock-config/org.json):") {
		t.Fatalf("the locked editor does not tell the organization imposes it:\n%s", view)
	}
	if choice, _ = editorKeys(t, choice, "esc"); choice.open != nil {
		t.Fatal("esc does not close the locked editor")
	}
	text, _ := editorKeys(t, editorAt(t, e, 1, 1), "enter", "x", "enter")
	if text.open == nil || text.open.Value() != "Usage=jumphost" {
		t.Fatal("a locked text field took the typed key or closed")
	}
}

func TestSettingsModuleOffIsDimmed(t *testing.T) {
	e, _ := editorKeys(t, loadedEditor(), "up")
	view := editorView(e)
	if !strings.Contains(view, "S3 (off)") || strings.Contains(view, "SQL (off)") {
		t.Fatalf("want S3 alone marked off:\n%s", view)
	}
	styles := NewStyles(true)
	// Selected, the section is also bold.
	if raw := e.View(styles, 120, 30); !strings.Contains(raw, styles.Faint.Bold(true).Render("S3 (off)")) {
		t.Fatalf("S3 is not dimmed:\n%q", raw)
	}
	if e, _ = editorKeys(t, e, "right"); selectedName(e) != "modules.s3.enabled" {
		t.Fatalf("selected = %q, want the S3 toggle", selectedName(e))
	}
}

func TestSettingsView(t *testing.T) {
	view := editorView(loadedEditor())
	for _, want := range []string{
		"General", "AWS", "MCP", "Agent", "Daemon", "Modules", "SQL", "Kubernetes", "Cache", "DynamoDB",
		"Language", "Automatic", "default", "file", "Organization config",
		"/home/me/.config/airlock/config.json",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "next launch") || strings.Contains(view, "language\n") {
		t.Fatalf("the left pane focused shows the extras of a row:\n%s", view)
	}
	view = editorView(editorAt(t, loadedEditor(), 0, 0))
	for _, want := range []string{"language", "Applies at the next launch of airlock."} {
		if !strings.Contains(view, want) {
			t.Fatalf("the selected row lacks %q:\n%s", want, view)
		}
	}
	if view = editorView(editorAt(t, loadedEditor(), 1, 0)); !strings.Contains(view, "none") || !strings.Contains(view, "on") {
		t.Fatalf("the AWS rows lack none and on:\n%s", view)
	}
	if view = editorView(editorAt(t, loadedEditor(), 1, 1)); !strings.Contains(view, "locked") || !strings.Contains(view, "Imposed by the organization") {
		t.Fatalf("the locked row lacks its badge and help:\n%s", view)
	}
}

func TestSettingSummary(t *testing.T) {
	for _, tc := range []struct {
		want string
		view SettingView
	}{
		{"on", SettingView{Kind: KindToggle, Value: "true"}},
		{"off", SettingView{Kind: KindToggle, Value: "false"}},
		{"Per process", SettingView{Kind: KindChoice, Value: "cli", Options: []SettingOption{{Value: "cli", Label: "Per process"}}}},
		{"none", SettingView{Kind: KindText}},
		{"auto", SettingView{Kind: KindClients, Value: "auto"}},
	} {
		if got := settingSummary(tc.view); got != tc.want {
			t.Errorf("settingSummary(%+v) = %q, want %q", tc.view, got, tc.want)
		}
	}
}

func TestSettingsEditorReloadKeepsTheSelectedKey(t *testing.T) {
	e := editorAt(t, loadedEditor(), 2, 2).closeEdit().loaded(testSettings(), nil)
	if e.focus != focusSettings || selectedName(e) != "mcp.gateway.enabled" {
		t.Fatalf("focus = %v selected = %q, want mcp.gateway.enabled", e.focus, selectedName(e))
	}
}

func TestSettingsEditorResetAsksFirst(t *testing.T) {
	e, _ := editorKeys(t, editorAt(t, loadedEditor(), 0, 1), "d")
	if !e.confirming || !strings.Contains(editorView(e), "Reset organizationConfig to its default? y/n") {
		t.Fatalf("confirming = %v, want the question:\n%s", e.confirming, editorView(e))
	}
	kept, outcome := editorKeys(t, e, "n")
	if kept.confirming || outcome != settingsStay {
		t.Fatalf("n: confirming = %v outcome = %v, want nothing", kept.confirming, outcome)
	}
	e, outcome = editorKeys(t, e, "y")
	if outcome != settingsReset || e.requestKey != "organizationConfig" {
		t.Fatalf("y: outcome = %v key = %q, want the reset", outcome, e.requestKey)
	}
}

func TestSettingsEditorResetIgnoresADefaultKey(t *testing.T) {
	if e, _ := editorKeys(t, editorAt(t, loadedEditor(), 0, 0), "d"); e.confirming {
		t.Fatal("a key the file does not set has nothing to reset")
	}
	if e, _ := editorKeys(t, loadedEditor(), "d"); e.confirming {
		t.Fatal("d on the left pane asks to reset")
	}
}

func TestSettingsEditorRegionOpensThePicker(t *testing.T) {
	e, outcome := editorKeys(t, editorAt(t, loadedEditor(), 1, 0), "enter")
	if outcome != settingsPickRegion || e.requestKey != "aws.defaultRegion" || e.open != nil {
		t.Fatalf("outcome = %v key = %q open = %v, want the region picker", outcome, e.requestKey, e.open != nil)
	}
}

func TestTextEditorValidatesOnlyChanges(t *testing.T) {
	calls := 0
	validate := func(string, string) error { calls++; return errors.New("refused") }
	e, _ := editorKeys(t, editorAt(t, newSettingsEditor(validate).loaded(testSettings(), nil), 2, 0), "enter", "left", "home")
	if calls != 0 {
		t.Fatalf("validate ran %d times on cursor moves, want none", calls)
	}
	if e, _ = editorKeys(t, e, "x"); calls != 1 || !strings.Contains(editorView(e), "refused") {
		t.Fatalf("validate ran %d times on a change, want once with its error shown", calls)
	}
}

func TestOptionalEditorsTellAnEmptyValueOverridesTheOrganization(t *testing.T) {
	help := i18n.T("ui.settings.empty_overrides_organization")
	text := testSetting("modules.sql.defaultDbUser", "modules.sql", KindText, "app_ro")
	tag := tagView("modules.sql.readOnlySecretTag", "Access=read-only", true)
	text.Optional = true
	for _, view := range []SettingView{text, tag} {
		open := func(view SettingView) string {
			if view.Kind == KindTag {
				return ansi.Strip(newTagEditor(view, acceptAnyValue).View(NewStyles(true), 60, 20))
			}
			return ansi.Strip(newTextEditor(view, acceptAnyValue).View(NewStyles(true), 60, 20))
		}
		if strings.Contains(open(view), help) {
			t.Fatalf("%s: help shown without an organization value", view.Name)
		}
		view.FromOrganization = true
		if !strings.Contains(open(view), help) {
			t.Fatalf("%s: view lacks %q:\n%s", view.Name, help, open(view))
		}
	}
}
