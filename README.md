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
- acme jumphost rds my-db 5432 --db-user app_ro

AWS region:

- The wizard does not ask for a region: the header shows the current one (`--region`, then `AWS_REGION`, then the profile's configured region, then `eu-west-3`).
- Press `r` on the resource type or target step to switch region from a searchable list (by code, city or country, e.g. `paris`, `eu-west`, `japan`). Switching on the target step reloads the targets of the new region.

Database user (RDS with AWS IAM authentication):

- The wizard ends on a review step summarizing the connection, where the local port and the database user can be edited before connecting (`tab` switches field, `enter` connects, `esc` goes back to the target list).
- The user is prefilled with `acme_ro` (or `--db-user`); leave it empty to keep `acme_ro`.
- The user is used by the connection hints, the IntelliJ data source, the MCP server and the restart command. Instances without IAM authentication (SQL Server) only show the port.
- The review step also shows an `IAM auth` toggle (`space` switches it), on by default: acme signs in passwordless, generating short-lived AWS IAM tokens.
- Turn it off to sign in with a password instead: the review then asks for the user and its password. The password is kept in memory only: the MCP server uses it, the IntelliJ data source switches to user & password (the IDE asks for the password), and the restart command falls back to IAM sign-in.
- Connecting from the wizard remembers the review choices per database (AWS account, region and instance name, so several profiles reaching one account share them) in `~/.config/jumphost/connections.json` (`$XDG_CONFIG_HOME/jumphost` when set): user, IAM auth on or off, local port and MCP write access. The next connection to that database starts from them; a saved port already in use falls back to a free one, and `--db-user` / `--mcp-allow-writes` still win. Passwords are never saved: with IAM auth off, the review asks for it again.

Parallel sessions:

- While a session runs, press `n` to open another connection (any profile, any resource); running sessions stay active.
- `tab` / `←` `→` / `1`-`9` switch between sessions, `x` stops the selected one (or closes it once ended), `ctrl+c` stops all and quits.
- Local ports never collide: the suggested port skips ports used by other sessions or other local processes (e.g. a second `5432` RDS gets `5433`). An explicitly requested busy port is rejected.
- Connecting twice to the same target (same type, name, profile and region) is refused.

Database MCP server (AI access):

- For RDS PostgreSQL and SQL Server sessions, acme serves an MCP server on `127.0.0.1` for as long as the session runs, so AI assistants can explore and query the database through the tunnel.
- It is registered as `acme-<profile>-<instance>` (the `acme-` prefix is not doubled for profiles that already start with it, e.g. `acme-prod-eu-<instance>`) in `.mcp.json` (Claude Code; written when `.mcp.json` or `.claude/` exists) and `.vscode/mcp.json` (VS Code; written when `.vscode/` exists), and removed when the session stops. The dashboard's `MCP` row shows `read-only` (or `WRITES ENABLED`), the server name and its URL; without those files it also shows the bearer token to configure a client by hand.
- Tools: `list_databases`, `list_tables`, `describe_table`, `query` (single read-only `SELECT`/`WITH`, max 1000 rows, 30 s timeout).
- Credentials: PostgreSQL uses an IAM token for `acme_ro` (or the chosen database user; read-only mode relies on that user being read-only in the database, on top of the query guard); SQL Server uses the `ro-access` secrets of the instance (one per database). Nothing is written to disk.
- `--mcp-allow-writes` (or the `MCP` toggle of the Connect step) adds a destructive `execute` tool. PostgreSQL still signs in as the chosen database user (IAM token or entered password), so pick a user allowed to write: its grants decide what can be modified. SQL Server has no IAM sign-in and switches to the RDS master user secret (administrators only). `--no-mcp` disables the server.
- Claude Code reads `.mcp.json` at startup and asks to approve new project servers: after opening a session in a running Claude Code, use `/mcp` to reconnect.
- Every MCP tool call is recorded. In the dashboard, press `m` on a database session to switch the bottom panel between its logs and its MCP queries (time, tool, database, rows, duration, full SQL and errors, including statements rejected by the read-only guard; the last 500 per session).
- The calls of all sessions are also appended to an audit log, one JSON object per line and one file per day: `~/.config/jumphost/logs/mcp-queries-YYYY-MM-DD.jsonl` (`$XDG_CONFIG_HOME/jumphost/logs` when set). Result rows are never logged; the files are readable by your user only, as statements may contain sensitive values. For example: `jq -r 'select(.sql) | "\(.time) \(.server) \(.sql)"' ~/.config/jumphost/logs/mcp-queries-*.jsonl`.
