# airlock

Guided SSM port forwarding to private AWS resources (EKS, RDS, ElastiCache) through a jumphost
instance, with IntelliJ data sources and a guarded MCP server so AI assistants can query tunneled
databases.

## Prerequisites

- Go 1.26+
- AWS CLI v2
- AWS Session Manager Plugin (required by `aws ssm start-session`)
- AWS credentials and profiles (for example with `aws configure` or AWS SSO), with IAM permissions for the AWS API calls used by this tool and `ssm:StartSession`
- A running EC2 instance tagged `Usage=jumphost` (configurable, see [Configuration](#configuration)) in each account and region you connect to

## Install

- From a checkout: `make install` (to `GOBIN`, or `GOPATH/bin`)
- Or: `GOPRIVATE=github.com/n2jsoft go install github.com/n2jsoft/airlock/cmd/airlock@latest`

## Usage

Examples:

- `airlock` (wizard: recent connections, or a new connection by resource type, AWS profile, target and review, or a search of every target of a profile)
- The wizard opens on your recent connections, most recently used first (`/` filters by name, type, profile or region; `d` forgets one). Picking one checks the target still exists and opens its review step prefilled. `n` starts a new connection, `s` searches a target: choose a profile, then pick among its RDS, EKS and ElastiCache targets in one list (types that cannot be listed are named in a warning). `--profile` and `--region` filter the recent connections and skip the profile question. Without any recent connection the wizard opens on the resource type step, where `s` starts the search.
- A recent connection whose profile no longer exists asks for one; a profile reaching another AWS account than the one the connection was saved in is refused, since same-named targets often exist in several accounts.
- `airlock rds my-db 5432`
- `airlock eks my-cluster 15432`
- `airlock rds my-db 5432 --db-user app_ro`

AWS region:

- The wizard does not ask for a region: the header shows the current one (`--region`, then `AWS_REGION`, then the profile's configured region, then `defaultRegion`). Without any, the header shows `region none` and the wizard opens the region list before looking for targets; non-interactive runs fail and name the settings to add.
- Press `r` on the resource type, target or search step to switch region from a searchable list (by code, city or country, e.g. `paris`, `eu-west`, `japan`). Switching on the target step reloads the targets (or the search) of the new region.

Database user (RDS with AWS IAM authentication):

- The wizard ends on a review step summarizing the connection, where the local port and the database user can be edited before connecting (`tab` switches field, `enter` connects, `esc` goes back one step).
- The user is prefilled with `--db-user`, else the user saved for that database, else `defaultDbUser`; it is required for AWS IAM sign-in.
- The user is used by the connection hints, the IntelliJ data source, the MCP server and the restart command. Instances without IAM authentication (SQL Server) only show the port.
- The review step also shows an `IAM auth` toggle (`space` switches it), on by default: airlock signs in passwordless, generating short-lived AWS IAM tokens.
- Turn it off to sign in with a password instead: the review then asks for the user and its password. The password is kept in memory only: the MCP server uses it, the IntelliJ data source switches to user & password (the IDE asks for the password), and the restart command falls back to IAM sign-in.
- Connecting from the wizard remembers every connection per target (AWS account, region, resource type and name, so several profiles reaching one account share them) in `~/.config/airlock/connections.json` (`$XDG_CONFIG_HOME/airlock` when set) with its last profile and local port, plus for databases: user, IAM auth on or off and MCP write access. The next connection to that database starts from them; a saved port already in use falls back to a free one, and `--db-user` / `--mcp-allow-writes` still win. Passwords are never saved: with IAM auth off, the review asks for it again.

Parallel sessions:

- While a session runs, press `n` to open another connection (any profile, any resource); running sessions stay active. The start screen marks recent connections already open as connected.
- `tab` / `←` `→` / `1`-`9` switch between sessions, `x` stops the selected one (or closes it once ended), `ctrl+c` stops all and quits.
- Local ports never collide: the suggested port skips ports used by other sessions or other local processes (e.g. a second `5432` RDS gets `5433`). An explicitly requested busy port is rejected.
- Connecting twice to the same target (same type, name, profile and region) is refused.

Database MCP server (AI access):

- For RDS PostgreSQL and SQL Server sessions, airlock serves an MCP server on `127.0.0.1` for as long as the session runs, so AI assistants can explore and query the database through the tunnel.
- It is registered as `<prefix>-<profile>-<instance>` (`mcpServerPrefix`, `airlock` by default; the prefix is not doubled for profiles that already start with it, e.g. prefix `team` and profile `team-prod` give `team-prod-<instance>`) in `.mcp.json` (Claude Code; written when `.mcp.json` or `.claude/` exists) and `.vscode/mcp.json` (VS Code; written when `.vscode/` exists), and removed when the session stops. The dashboard's `MCP` row shows `read-only` (or `WRITES ENABLED`), the server name and its URL; without those files it also shows the bearer token to configure a client by hand.
- Tools: `list_databases`, `list_tables`, `describe_table`, `query` (single read-only `SELECT`/`WITH`, max 1000 rows, 30 s timeout).
- Credentials: PostgreSQL uses an IAM token for the chosen database user (read-only mode relies on that user being read-only in the database, on top of the query guard); SQL Server uses the read-only secrets of the instance tagged with `readOnlySecretTag` (one per database; SQL Server read-only access is off without it). Nothing is written to disk.
- `--mcp-allow-writes` (or the `MCP` toggle of the Connect step) adds a destructive `execute` tool. PostgreSQL still signs in as the chosen database user (IAM token or entered password), so pick a user allowed to write: its grants decide what can be modified. SQL Server has no IAM sign-in and switches to the RDS master user secret (administrators only). `--no-mcp` disables the server.
- Claude Code reads `.mcp.json` at startup and asks to approve new project servers: after opening a session in a running Claude Code, use `/mcp` to reconnect.
- Every MCP tool call is recorded. In the dashboard, press `m` on a database session to switch the bottom panel between its logs and its MCP queries (time, tool, database, rows, duration, full SQL and errors, including statements rejected by the read-only guard; the last 500 per session).
- The calls of all sessions are also appended to an audit log, one JSON object per line and one file per day: `~/.config/airlock/logs/mcp-queries-YYYY-MM-DD.jsonl` (`$XDG_CONFIG_HOME/airlock/logs` when set). Result rows are never logged; the files are readable by your user only, as statements may contain sensitive values. For example: `jq -r 'select(.sql) | "\(.time) \(.server) \(.sql)"' ~/.config/airlock/logs/mcp-queries-*.jsonl`.

## Configuration

`~/.config/airlock/config.json` (`$XDG_CONFIG_HOME/airlock` when set, `%AppData%\airlock` on Windows).
Every key is optional and flags win over the file. An unknown key or a malformed value is an error naming the file.

- `airlock config show` lists every setting, its value and whether it comes from the file or the defaults (`--json` for the effective values only); `airlock config path` prints the file location.
- `airlock config init` writes the file with every key and its default, and explains each key. An existing file is kept unless `--force`, which saves it as `config.json.bak` first.
- `airlock config set <key> <value>` validates the value and writes it, keeping the other keys (for example `airlock config set jumphostTag Env=bastion`); `airlock config unset <key>` removes a key so its default applies again. The file is rewritten indented, in a fixed key order.
- To share the team's settings, `airlock config export` prints the keys set in your file as one string (`airlock1:…`). A teammate runs `airlock config import <string>`: airlock lists every key it adds (`+`), changes (`~`) or keeps (`=`) with what the key does, then asks before writing (`--yes` skips the question). Keys of their file that the string does not set are kept, unless `--replace`, which removes them (`-`). The string is not readable on purpose, so review the list before answering `y`: its checksum only catches a damaged copy and paste, it does not prove who made the string.
- Errors caused by a setting name it, e.g. a jumphost not found with the configured tag points to `jumphostTag`, `airlock config show` and `--jumphost-tag`.

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `jumphostTag` | `--jumphost-tag` | `Usage=jumphost` | Tag (`Key=Value`) of the running SSM jumphost EC2 instance |
| `defaultDbUser` | `--db-user` | none (asked) | Database user signing in with AWS IAM |
| `readOnlySecretTag` | `--read-only-secret-tag` | none (SQL Server read-only MCP off) | Tag (`Key=Value`) of the per-database read-only Secrets Manager secrets |
| `mcpServerPrefix` | `--mcp-server-prefix` | `airlock` | Start of the MCP server names |
| `defaultRegion` | `--region` | none (asked) | Region used after `AWS_REGION` and the profile's region |

### acme preset

```json
{
  "defaultRegion": "eu-west-3",
  "defaultDbUser": "acme_ro",
  "readOnlySecretTag": "AccessRole=ro-access",
  "mcpServerPrefix": "acme"
}
```

## Migrating from `acme jumphost`

- `acme jumphost <args>` becomes `airlock <args>`, with the same flags.
- On its first run, airlock copies `connections.json` and `logs/` from `~/.config/jumphost` into `~/.config/airlock`; the old directory is left untouched.
- With the acme preset above, MCP server names keep their `acme-` prefix, so AI client configurations keep working.

## Development

- Run: `make run`
- Build: `make build` (to `bin/airlock`)
- Install / uninstall: `make install`, `make uninstall`
- Format, vet, lint, test: `make fmt`, `make vet`, `make lint`, `make test`
