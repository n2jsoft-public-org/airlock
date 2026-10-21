# Contributing to airlock

Thank you for helping. This page explains how to build airlock, check a change and name your pull
request so that it releases the right version.

## Build and run

airlock needs Go 1.27 or later. From a checkout:

```sh
make run                     # run the CLI
make build                   # build to bin/airlock
make install                 # build and install to GOBIN (make uninstall to remove)
make dist                    # build macOS, Linux and Windows archives to dist/ (needs GoReleaser)
```

On Windows, without `make`, run `go build ./cmd/airlock` or `go install ./cmd/airlock`.

## Check your change

Before you open a pull request, run:

```sh
make fmt vet lint test       # format, vet, lint and test
```

All four must pass; CI lints and tests every pull request too. Keep `go.mod` and `go.sum` tidy, and do not commit built
binaries.

## Code layout

- `cmd/airlock`: the entry point.
- `internal/cli`: the Cobra commands. Command names are kebab-case, each built by a
  `new<Command>Cmd()` function; handlers stay thin and call reusable code. Only `internal/cli`
  reads the config file: other packages receive plain values.
- `internal/ui`: the terminal UI (Bubble Tea, Bubbles, Lipgloss), with its styles in one module.
- User-facing text goes through `internal/i18n`: add every new message to both `catalog_en.go` and
  `catalog_fr.go`.
- Company-specific conventions (tags, names, environments) belong in `config.json`, never in the
  code.

## Pull requests and releases

Every push to `main` releases a version, computed by
[semantic-release](https://semantic-release.gitbook.io/) from the commits. Pull requests are
squashed, so **the pull request title decides the version**. It must be a
[conventional commit](https://www.conventionalcommits.org/):

| Title | Release |
|---|---|
| `feat!: …`, or `BREAKING CHANGE:` in the body | major |
| `feat: …` | minor |
| any other type: `fix: …`, `docs: …`, `chore: …`, `test: …`… | patch |
| not a conventional commit | nothing |

Do not create version tags by hand: the release workflow does it. Dependency updates come from
Renovate (`chore(deps): …`), configured in [`renovate.json`](renovate.json).
