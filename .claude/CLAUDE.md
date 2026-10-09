# AGENTS

This repository hosts `airlock`, a CLI for SSM port forwarding to private AWS resources.

## Scope and goals

- Provide a stable command-line interface with Cobra.
- Provide a terminal UI foundation with Bubble Tea, Bubbles, and Lipgloss.
- Keep business logic out of command wiring when possible.

## Architecture guidelines

- Keep executable entrypoint in `cmd/airlock`.
- Keep command definitions in `internal/cli`.
- Keep TUI concerns in `internal/ui`.
- Favor small focused files and explicit function names.
- Only `internal/cli` reads the config file; other packages receive plain values.
- Keep company-specific conventions out of the code: they belong in `config.json`.

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
- golangci-lint has pre-existing findings; new code must not add any (`--new-from-rev`).
- Do not commit generated binaries.

## Releases and versioning

- Every push to `main` releases a semantic version `vX.Y.Z`, computed by semantic-release
  (`.releaserc.yaml`, run by `.github/workflows/release.yml`). Never create release tags by hand.
- Pull requests are squashed, so the PR title decides the version: it must be a conventional
  commit. Breaking change (`feat!:` or `BREAKING CHANGE:` in the body) → major, `feat` → minor,
  any other type (`fix`, `docs`, `chore`…) → patch. A non-conventional title releases nothing.
- `airlock version` reads the tag Go records in the binary (`internal/version`): do not inject
  the version with ldflags. Builds after a release carry a pseudo-version that orders before the
  next release.
- `airlock update` compares versions with `golang.org/x/mod/semver` (`internal/selfupdate`).
- Archives keep the `v` in their name (`airlock_<tag>_<os>_<arch>`, `.goreleaser.yaml`): binaries
  from before v1.0.0 (`YYYYMMDD-<sha>`), the install scripts and `airlock update` rely on it.
- Releases are created as drafts and published once the archives are attached: keep that order.

## Safety and change management

- Avoid destructive git operations.
- Preserve unrelated user changes.
- Prefer additive changes and clear migration paths.
