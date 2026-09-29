package ui

import tea "github.com/charmbracelet/bubbletea"

// RootSelection contains the command and global execution context selected in root TUI.
// The AWS region is not asked here: the jumphost wizard shows it and switches it with a shortcut.
type RootSelection struct {
	Command    string
	AWSProfile string
}

// RunCommandPicker opens the root TUI and returns the selected command and optional params.
func RunCommandPicker() (RootSelection, error) {
	m := newModel()
	p := tea.NewProgram(m, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		return RootSelection{}, err
	}

	result, ok := finalModel.(model)
	if !ok {
		return RootSelection{}, nil
	}

	return RootSelection{
		Command:    result.selectedCmd,
		AWSProfile: result.selectedProfile,
	}, nil
}
