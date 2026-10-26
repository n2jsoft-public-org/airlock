package cli

import (
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/agentskill"
	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/ui"
	"github.com/spf13/cobra"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: i18n.T("cli.skills.short"),
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newSkillsInstallCmd())
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	var user bool
	validArgs := make([]string, 0, len(aiclient.Clients))
	for _, client := range aiclient.Clients {
		validArgs = append(validArgs, string(client))
	}
	cmd := &cobra.Command{
		Use:       "install [claude|vscode|codex|cursor|gemini...]",
		Short:     i18n.T("cli.skills_install.short"),
		Long:      i18n.T("cli.skills_install.long"),
		ValidArgs: validArgs,
		Args:      cobra.OnlyValidArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) == 0 && !user {
				return installConfiguredSkills(cmd, projectDir)
			}
			targets := make([]aiclient.Target, 0, len(aiclient.Clients))
			if len(args) == 0 {
				args = validArgs
			}
			scope := aiclient.ScopeProject
			if user {
				scope = aiclient.ScopeUser
			}
			for _, arg := range args {
				targets = append(targets, aiclient.Target{Client: aiclient.Client(arg), Scope: scope})
			}
			paths, err := agentskill.Paths(projectDir, targets)
			if err != nil {
				return err
			}
			return installSkills(cmd, projectDir, paths)
		},
	}
	cmd.Flags().BoolVar(&user, "user", false, i18n.T("cli.skills_install.flag.user"))
	return cmd
}

// installConfiguredSkills writes the skill files of the mcp.clients setting, which a terminal
// offers as the checked items of a checklist of every skill file.
func installConfiguredSkills(cmd *cobra.Command, projectDir string) error {
	cfg, _, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	paths, err := agentskill.Paths(projectDir, agentskill.Targets(projectDir, cfg.MCPClientSelection()))
	if err != nil {
		return err
	}
	files, err := agentskill.Files(projectDir)
	if err != nil {
		return err
	}
	choices := make([]ui.ExportChoice, len(files))
	for idx, file := range files {
		clients := make([]string, len(file.Clients))
		for pos, client := range file.Clients {
			clients[pos] = string(client)
		}
		choices[idx] = ui.ExportChoice{
			Name:      mcpserve.RelativeTo(projectDir, []string{file.Path}),
			Detail:    strings.Join(clients, ", "),
			Unchecked: !slices.Contains(paths, file.Path),
		}
	}
	indexes, asked, err := pickExports(cmd, i18n.T("cli.skills.pick_title"), i18n.T("ui.hint.write"), choices)
	switch {
	case errors.Is(err, ui.ErrExportCanceled):
		outf(cmd, "%s", i18n.T("cli.skills.nothing_written"))
		return nil
	case err != nil:
		return err
	case asked:
		paths = paths[:0]
		for _, idx := range indexes {
			paths = append(paths, files[idx].Path)
		}
	case len(paths) == 0:
		return i18n.NewError("cli.skills.no_client_selected", cfg.MCPClientSelection().String())
	}
	return installSkills(cmd, projectDir, paths)
}

func installSkills(cmd *cobra.Command, projectDir string, paths []string) error {
	written, err := agentskill.Install(paths)
	for _, path := range paths {
		key := "cli.skills.up_to_date"
		switch {
		case slices.Contains(written, path):
			key = "cli.skills.written"
		case err != nil:
			continue // Install reports it.
		}
		outf(cmd, i18n.T(key), mcpserve.RelativeTo(projectDir, []string{path}))
	}
	return err
}
