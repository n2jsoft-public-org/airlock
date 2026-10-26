# Contributing to airlock

Thank you for helping. This page explains how to build airlock, check a change and name your pull
request so that it releases the right version. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Bugs**: open a [bug report](https://github.com/n2jsoft-public-org/airlock/issues/new/choose)
  with the output of `airlock version` and `airlock doctor`.
- **Features and larger changes**: open a feature request first, so that we agree on the design
  before you write the code. Small fixes and docs can go straight to a pull request.
- **Vulnerabilities**: never in a public issue. See [SECURITY.md](SECURITY.md).

## Build and run

airlock needs the Go version named in [`go.mod`](go.mod) or later, and
[golangci-lint](https://golangci-lint.run/) v2 for `make lint`. From a checkout:

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

The same rules, written for AI coding assistants, are in [`.claude/CLAUDE.md`](.claude/CLAUDE.md).

## Design notes

There is no separate design folder. User-facing behaviour is documented in [`docs/`](docs): update
the page your change affects in the same pull request. The reasons behind a change go in its
issue and pull request description, and in a short comment next to the code when the code cannot
show them.

## Sign your commits (DCO)

Every commit must carry a `Signed-off-by` line, certifying that you wrote it or have the right to
submit it under the project's license, as the [Developer Certificate of Origin](https://developercertificate.org/)
describes. `git commit -s` adds it from your Git name and email:

```sh
git commit -s -m "fix(rds): keep the local port when the tunnel restarts"
```

```text
Signed-off-by: Jane Doe <jane@example.com>
```

Forgot it? `git commit --amend -s` fixes the last commit, `git rebase --signoff main` every commit
of your branch; then force-push the branch.

## Pull requests and releases

Every push to `main` releases a version, computed by
[semantic-release](https://semantic-release.gitbook.io/) from the commits. Pull requests are
squash-merged into one commit named after the pull request, so **the pull request title decides
the version**, whatever your branch's commits are called. It must be a
[conventional commit](https://www.conventionalcommits.org/):

| Title | Release |
|---|---|
| `feat!: …`, or `BREAKING CHANGE:` in the body | major |
| `feat: …` | minor |
| any other type: `fix: …`, `docs: …`, `chore: …`, `test: …`… | patch |
| not a conventional commit | nothing |

A pull request needs the `pr-ruleset` check to pass and a maintainer's approval before it is
merged. Do not create version tags by hand: the release workflow does it. Dependency updates come from
Renovate (`chore(deps): …`), configured in [`renovate.json`](renovate.json).
