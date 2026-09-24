# acme CLI

Internal helper CLI for company workflows.

## Prerequisites

Install these CLIs before running commands:

- Go 1.26+
- GitHub CLI (gh)
- AWS CLI v2
- AWS Session Manager Plugin (required by aws ssm start-session used by jumphost)

### Authentication and access

- For update-translate:
  - Run gh auth login
  - Ensure your GitHub account can access the private repository example/translate
- For jumphost:
  - Configure AWS credentials and profiles (for example with aws configure)
  - Ensure your IAM permissions allow the AWS API calls used by this tool and SSM session start

## Build and run

- Run app: make run
- Build binary: make build
- Install to your Go bin (GOBIN, or GOPATH/bin): make install
- Uninstall: make uninstall
- Format: make fmt
- Vet: make vet
- Lint: make lint
- Test: make test

## Commands

### update-translate

Downloads the latest release asset i18n_acme.zip from example/translate using gh and extracts it to a destination folder.

Examples:

- Interactive wizard:
  - acme update-translate
- Non-interactive:
  - acme update-translate --dest ./i18n --no-tui
- Explicit overwrite behavior:
  - acme update-translate --dest ./i18n --overwrite=true --no-tui

Notes:

- Default destination is the current working directory.
- Overwrite is enabled by default.

### jumphost

Guided EKS, RDS or ElastiCache SSM port-forwarding flow.

Examples:

- acme jumphost
- acme jumphost rds my-db 5432
- acme jumphost eks my-cluster 15432

Parallel sessions:

- While a session runs, press `n` to open another connection (any profile, any resource); running sessions stay active.
- `tab` / `←` `→` / `1`-`9` switch between sessions, `x` stops the selected one (or closes it once ended), `ctrl+c` stops all and quits.
- Local ports never collide: the suggested port skips ports used by other sessions or other local processes (e.g. a second `5432` RDS gets `5433`). An explicitly requested busy port is rejected.
- Connecting twice to the same target (same type, name, profile and region) is refused.
