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

Guided EKS or RDS SSM port-forwarding flow.

Examples:

- acme jumphost
- acme jumphost rds my-db 5432
- acme jumphost eks my-cluster 15432
