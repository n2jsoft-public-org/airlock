# AGENTS

This repository hosts the `acme` CLI helper for internal company workflows.

## Scope and goals

- Provide a stable command-line interface with Cobra.
- Provide a terminal UI foundation with Bubble Tea, Bubbles, and Lipgloss.
- Keep business logic out of command wiring when possible.

## Architecture guidelines

- Keep executable entrypoint in `cmd/cli`.
- Keep command definitions in `internal/cli`.
- Keep TUI concerns in `internal/ui`.
- Favor small focused files and explicit function names.

## Command conventions

- Command names must be kebab-case.
- New commands should expose `new<Command>Cmd()` constructor functions.
- Command handlers should be thin and delegate to reusable functions/services.
- Placeholder behavior for unfinished commands should be explicit and non-failing.

## TUI conventions

- Root command with no subcommand should launch interactive selection.
- TUI state should remain deterministic and testable.
- Styles should be centralized in a single styles module.

## Build and quality

- Use `make fmt`, `make vet`, `make lint`, and `make test` before submitting changes.
- Keep `go.mod` and `go.sum` tidy.
- Do not commit generated binaries.

## Safety and change management

- Avoid destructive git operations.
- Preserve unrelated user changes.
- Prefer additive changes and clear migration paths.
