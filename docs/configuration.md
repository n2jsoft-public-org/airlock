# Configuration

Settings live in `~/.config/airlock/config.json` (`$XDG_CONFIG_HOME/airlock` when set,
`%AppData%\airlock` on Windows). Every key is optional and flags take precedence over the file. An
unknown key or a malformed value is an error that names the file. Global settings sit at the top,
the others in one section per concern and one per module; commands name a setting by its dotted
path, e.g. `modules.sql.defaultDbUser`:

```json
{
  "language": "auto",
  "mode": "cli",
  "environments": "prod!=*prod*; staging=*stg*",
  "aws": { "defaultRegion": "eu-west-1", "jumphostTag": "Usage=jumphost", "randomLocalPort": true },
  "mcp": { "serverPrefix": "airlock", "clients": "auto", "gateway": { "enabled": true, "port": 7821 } },
  "agent": { "autoConnect": "none", "writes": false, "idleTimeout": "30m" },
  "daemon": { "idleTimeout": "10m" },
  "modules": {
    "sql": { "enabled": true, "defaultDbUser": "app_ro", "readOnlySecretTag": "" },
    "k8s": { "enabled": true },
    "cache": { "enabled": true },
    "dynamodb": { "enabled": false },
    "s3": { "enabled": true },
    "logs": { "enabled": true }
  }
}
```

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `language` | none | `auto` | Language of airlock: `auto` (the system's), `en` or `fr`; read at start, never shared |
| `mode` | `--mode` | `cli` | `cli`: sessions live in each `airlock` and close with it; `daemon`: a background daemon keeps them (see [Daemon mode](daemon.md)) |
| `environments` | none | `prod!=` | Environments badged in the TUI, separated by `;`, each `name=patterns` of AWS profiles (`*` wildcard, any case); `!` marks production, e.g. `prod!=*prod*,*-live; staging=*stg*; dev=*dev*` |
| `aws.defaultRegion` | `--region` | none (asked) | Region used after `AWS_REGION` and the profile's region |
| `aws.jumphostTag` | `--jumphost-tag` | `Usage=jumphost` | Tag (`Key=Value`) of the running SSM jumphost EC2 instance |
| `aws.randomLocalPort` | none | `true` | `true`: SSM tunnels listen on a free local port chosen by the OS, avoiding conflicts; `false`: on the target's port (or the next free one) |
| `mcp.serverPrefix` | `--mcp-server-prefix` | `airlock` | Start of the MCP server names |
| `mcp.clients` | `--mcp-clients` | `auto` | AI clients the MCP servers are registered in: `auto`, `none`, or e.g. `claude:user,codex` (see [AI access with MCP](mcp.md)) |
| `mcp.gateway.enabled` | none | `true` | `true`: the daemon serves the MCP gateway, through which AI agents list targets and open sessions; `false`: no gateway (see [AI access with MCP](mcp.md)) |
| `mcp.gateway.port` | none | `7821` | Port of the MCP gateway on `127.0.0.1` (1024-65535) |
| `agent.autoConnect` | none | `none` | Agent openings that skip the dashboard prompt: `none`, `non-prod` (targets outside the production environment) or `all` |
| `agent.writes` | none | `false` | `true`: agents may ask for MCP writes, always approved in a dashboard; `false`: agents only open read-only sessions |
| `agent.idleTimeout` | none | `30m` | Close a session opened by an agent after this long without a call through the gateway, e.g. `30m` or `2h` (at least `1m`) |
| `daemon.idleTimeout` | none | `10m` | Stop the daemon after this long without session, dashboard nor call through the gateway, e.g. `10m` or `8h` (at least `1m`); read at the daemon's start |
| `modules.<name>.enabled` | none | `true` | `false`: airlock neither lists nor opens the targets of that module: `sql` (RDS, direct and Docker databases), `k8s` (EKS), `cache` (ElastiCache and Docker caches), `dynamodb`, `s3`, `logs` (CloudWatch Logs) |
| `modules.sql.defaultDbUser` | `--db-user` | none (asked) | Database user prefilled on RDS: a user signing in with AWS IAM on PostgreSQL and MySQL, and/or `engine=user` pairs (`postgres`, `mysql`, `sqlserver`), e.g. `app_ro,sqlserver=mcp_ro` (see below) |
| `modules.sql.readOnlySecretTag` | `--read-only-secret-tag` | none (no **Auto** sign-in on SQL Server without MCP writes) | Tag (`Key=Value`) of the per-database read-only Secrets Manager secrets, used by the **Auto** sign-in of SQL Server |
| `organizationConfig` | none | none | Source of the [organization config](organization-config.md): an absolute path, an `https://` URL or a `github.com` link; personal, never exported |

Booleans and the port are JSON booleans and numbers; a string (`"true"`, `"7821"`) is accepted too.
Files written by older versions, with flat keys such as `jumphostTag`, are still read: see
[Upgrading](upgrading.md).

`modules.sql.defaultDbUser` takes comma-separated entries. A bare user (`app_ro`) is the AWS IAM user of
PostgreSQL and MySQL; an `engine=user` pair sets the user of one engine and wins over the bare user.
SQL Server has no AWS IAM sign-in, so a bare user never applies to it: `sqlserver=mcp_ro` prefills
the user of its password and Secrets Manager sign-in instead.

`environments` names the environments of your AWS profiles. Each one gets a badge wherever a
profile, a connection or a session is shown: the wizard header, profile list, recent connections
and review step, and the dashboard's session cards, pills, details and approval dialog. The
production environment (marked `!`) keeps the orange `PROD` style and turns borders orange; the
others take their own colors in the order they are written. A profile matching several
environments takes production first, then the first one written. `prod` always names the
production environment, so a direct database shared as `prod` stays production wherever your team
calls it `live`. Naming conventions differ between teams, so no profile is flagged until you set
the patterns, typically in your team's shared settings.

## Commands

```sh
airlock config show               # every setting, its value and where it comes from (--json for effective values)
airlock config path               # print the file location
airlock config init               # write every key the organization does not set with its default, explain each one (--force keeps a .bak, language and organizationConfig)
airlock config set <key> <value>  # validate and write one key, e.g. aws.jumphostTag Env=bastion
airlock config unset <key>        # remove a key so its default applies again
airlock config sync               # read the organization config now, drop local copies of its values (--yes skips the question)
airlock config organization-token # store a GitHub token for a private organization config (--delete removes it)
```

The file is rewritten indented, with keys in a fixed order. Errors caused by a setting name it. For
example, a jumphost not found with the configured tag points to `aws.jumphostTag`, `airlock config show`
and `--jumphost-tag`.

## From the TUI

Press `,` on the start screen (or on the Type step when there is no saved connection) or in the
dashboard to open the Settings screen. Two panes: the sections on the left (`general`, `aws`, `mcp`,
`agent`, `daemon` and one per module), the keys of the selected section on the right, each with its
value, where it comes from and what it does. `←` `→` or `tab` switch panes, `↑` `↓` move, `enter`
changes the selected key, `space` toggles a yes/no key, `d` resets it to its default and `esc` goes
back.

Each key opens the editor that fits it. `mode` and `agent.autoConnect` are a list of their choices,
each with what it does, and `language` a list of the languages; `aws.defaultRegion` opens the
region picker; a duration offers `15m`, `30m`, `1h` and `4h`, then "other…" to type another one; a
tag has two fields, Key and Value; a port and the other keys are a text field. They check what you
type as `airlock config set` does and write once, when you confirm; confirming the current value
writes nothing. `mcp.clients` is a grid: an Auto-detect row for the clients found in the project,
then one row per AI client with the scope of its configuration (project or user); `space` ticks a
box, `n` selects none and `enter` saves. `environments` has its own editor: the list of
environments, one marked as production, and for each the profile patterns it matches, with the
local AWS profiles each one receives; `enter` opens an environment, `a` adds one, `x` deletes it and
`s` saves the list. A key the organization locks opens read-only, with a line under it saying the
organization imposes it. Saving an optional key empty writes it empty, which overrides the value of
the organization: its editor says so when the organization sets that key.

The next connections use the new values, in CLI mode as in daemon mode; `mode` applies at the next
launch of airlock. A flag of the run, such as `--jumphost-tag`, keeps winning over the file and is
shown next to its key.

## Language

airlock speaks English and French. By default it follows the system: `LC_ALL`, `LC_MESSAGES`,
then `LANG`, else the macOS or Windows display language; any other language gives English.

```sh
airlock config set language fr     # always French
airlock config set language auto   # back to the system's
LANG=en_US.UTF-8 airlock           # English for one run (when language is auto)
```

The setting is personal: `airlock config export` never shares it. The daemon reads it when it
starts: run `airlock daemon restart` after a change. MCP tools, their answers and the errors AI
agents read stay in English; logs too.

## Sharing settings with your team

A team usually sets the same few keys, for example:

```json
{
  "environments": "prod!=*prod*; staging=*stg*",
  "aws": { "defaultRegion": "eu-west-1" },
  "mcp": { "serverPrefix": "team" },
  "modules": { "sql": { "defaultDbUser": "app_ro,sqlserver=mcp_ro", "readOnlySecretTag": "Role=read-only" } }
}
```

```sh
airlock config export            # prints the keys set in your file as one string: airlock2:…
airlock config import <string>   # shows what changes, then asks before writing
```

In a terminal, `export` first lists the keys set in your file, all selected: `space` unchecks the ones
to leave out, `a` toggles them all, `enter` prints the string. The list is drawn on stderr, so
`airlock config export | pbcopy` still works; `--all`, or no terminal, exports every key without asking.

`import` lists every key it adds (`+`), changes (`~`) or keeps (`=`), with what each key does. `--yes`
skips the question. Keys in your file that the string does not set are kept, unless you pass
`--replace`, which removes them (`-`).

> [!WARNING]
> The string is deliberately not human-readable, so review the list before you answer `y`. Its
> checksum only catches a damaged copy and paste; it does not prove who made the string. Keep your
> real preset internal: set it once, then share it with `airlock config export` rather than in a
> public document.
