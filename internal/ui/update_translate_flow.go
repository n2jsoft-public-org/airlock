package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	updatetranslate "github.com/example/acme-cli/internal/update_translate"
)

type updateTranslateStep int

const (
	updateTranslateStepDestination updateTranslateStep = iota
	updateTranslateStepOverwrite
	updateTranslateStepConfirm
)

//nolint:govet // Keep field order readable for Bubble Tea state handling.
type updateTranslateWizardModel struct {
	destination textinput.Model
	styles      Styles
	options     updatetranslate.Options
	errorMsg    string
	step        updateTranslateStep
	canceled    bool
}

// RunUpdateTranslateWizard collects update-translate options in a dedicated TUI flow.
func RunUpdateTranslateWizard(base updatetranslate.Options) (updatetranslate.Options, bool, error) {
	model := newUpdateTranslateWizardModel(base)
	program := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := program.Run()
	if err != nil {
		return updatetranslate.Options{}, false, err
	}

	result, ok := finalModel.(*updateTranslateWizardModel)
	if !ok {
		return updatetranslate.Options{}, true, nil
	}

	return result.options, result.canceled, nil
}

func newUpdateTranslateWizardModel(base updatetranslate.Options) *updateTranslateWizardModel {
	destinationValue := strings.TrimSpace(base.Destination)
	if destinationValue == "" {
		if cwd, err := os.Getwd(); err == nil {
			destinationValue = cwd
		}
	}

	input := textinput.New()
	input.Placeholder = "Destination folder"
	input.SetValue(destinationValue)
	input.CharLimit = 512
	input.Focus()

	if !base.OverwriteExisting {
		base.OverwriteExisting = true
	}

	return &updateTranslateWizardModel{
		styles:      NewStyles(),
		step:        updateTranslateStepDestination,
		options:     base,
		destination: input,
	}
}

func (m *updateTranslateWizardModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *updateTranslateWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "q":
			m.canceled = true
			return m, tea.Quit
		}
	}

	switch m.step {
	case updateTranslateStepDestination:
		return m.updateDestination(msg)
	case updateTranslateStepOverwrite:
		return m.updateOverwrite(msg)
	case updateTranslateStepConfirm:
		return m.updateConfirm(msg)
	default:
		return m, nil
	}
}

func (m *updateTranslateWizardModel) updateDestination(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		value := strings.TrimSpace(m.destination.Value())
		if value == "" {
			m.errorMsg = "Destination cannot be empty"
			return m, nil
		}

		m.options.Destination = value
		m.errorMsg = ""
		m.destination.Blur()
		m.step = updateTranslateStepOverwrite
		return m, nil
	}

	var cmd tea.Cmd
	m.destination, cmd = m.destination.Update(msg)
	return m, cmd
}

func (m *updateTranslateWizardModel) updateOverwrite(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m.step = updateTranslateStepDestination
			m.destination.Focus()
			return m, nil
		case "left", "right", "tab", "y", "n", "space":
			m.options.OverwriteExisting = !m.options.OverwriteExisting
			return m, nil
		case "enter":
			m.step = updateTranslateStepConfirm
			return m, nil
		}
	}

	return m, nil
}

func (m *updateTranslateWizardModel) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m.step = updateTranslateStepOverwrite
			return m, nil
		case "enter":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m *updateTranslateWizardModel) View() string {
	title := m.styles.Title.Render("acme update-translate")
	errorLine := ""
	if m.errorMsg != "" {
		errorLine = "\n" + m.styles.Subtitle.Render(m.errorMsg)
	}

	switch m.step {
	case updateTranslateStepDestination:
		content := title + "\n" +
			m.styles.Subtitle.Render("Destination folder") + "\n\n" +
			m.destination.View() + errorLine + "\n\n" +
			m.styles.Muted.Render("Enter: continue   q: quit")
		return m.styles.Container.Render(content)
	case updateTranslateStepOverwrite:
		choice := "No"
		if m.options.OverwriteExisting {
			choice = "Yes"
		}
		content := title + "\n" +
			m.styles.Subtitle.Render(fmt.Sprintf("Destination: %s", m.options.Destination)) + "\n\n" +
			m.styles.Subtitle.Render(fmt.Sprintf("Overwrite existing files: %s", choice)) + "\n\n" +
			m.styles.Muted.Render("Left/Right/Space: toggle   Enter: continue   esc: back   q: quit")
		return m.styles.Container.Render(content)
	case updateTranslateStepConfirm:
		overwriteValue := "No"
		if m.options.OverwriteExisting {
			overwriteValue = "Yes"
		}
		content := title + "\n" +
			m.styles.Subtitle.Render("Ready to update translations") + "\n\n" +
			fmt.Sprintf("Destination: %s\n", m.options.Destination) +
			fmt.Sprintf("Overwrite existing files: %s\n\n", overwriteValue) +
			m.styles.Muted.Render("Enter: run update   esc: back   q: quit")
		return m.styles.Container.Render(content)
	default:
		return m.styles.Container.Render(title)
	}
}
