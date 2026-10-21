# AI access with MCP

For **RDS PostgreSQL**, **MySQL**, **MariaDB** (Aurora included) and **SQL Server** sessions, **direct databases** (PostgreSQL, SQL Server, MySQL, MariaDB), local **Docker** databases and caches, **EKS** clusters and
**Redis / Valkey ElastiCache** caches, **DynamoDB** tables, **S3** buckets and **CloudWatch Logs**, airlock serves an MCP server on
`127.0.0.1` for as long as the session runs, so AI assistants can explore them: through the tunnel,
straight at the server for direct databases, or through the AWS API for DynamoDB, S3 and CloudWatch Logs.

## Database tools

| Tool | Description |
|---|---|
| `list_databases` | List the databases of the instance |
| `list_tables` | List the tables of a database |
| `describe_table` | Show the columns of a table |
| `query` | Run a single read-only `SELECT` / `WITH` (on MySQL also `SHOW`, `DESCRIBE`, `EXPLAIN`; 200 rows by default, `max_rows` up to 1000; 30 s timeout) |
| `query_plan` | Execution plan of a single read-only `SELECT` / `WITH`, summarized: operators with estimated and actual rows, costs, predicates and warnings; on SQL Server also seeks, missing indexes, parameter values and the `STATISTICS IO` and `TIME` output; on PostgreSQL timings, buffers and changed settings; on MySQL and MariaDB access types, keys, filtered rows and timings. See below. |
| `execute` | Run a statement that modifies data. **Only with `--mcp-allow-writes`.** |

`query_plan` has two modes on every engine: `actual` (default) runs the statement in a transaction
always rolled back, `estimated` only plans it.

On **SQL Server**:

- `actual` runs the statement with `SET STATISTICS XML, IO, TIME ON`.
  A parameterized statement is passed as applications send it: `parameters` takes the declaration
  (`@p0 int, @p1 nvarchar(6)`) and `values` one value per parameter, and the statement runs through
  `sp_executesql`, so the plan is compiled for the same types and sniffed values as the application's.
- `estimated` compiles the statement (`SET SHOWPLAN_XML ON`). It takes no parameters: SQL Server does
  not compile the statement of `sp_executesql` without running it.

`include_xml` also returns the raw showplan, to save as a `.sqlplan` file and open in SSMS. The login
needs the `SHOWPLAN` permission in the database.

On **PostgreSQL** (12 or later):

- `actual` runs `EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON)` in a read-only
  transaction, so a write hidden in a function fails instead of running.
- `estimated` runs `EXPLAIN (VERBOSE, FORMAT JSON)`.

A parameterized statement uses `$1`, `$2`… with `values`, one per parameter. PostgreSQL infers their
types: there is no `parameters` declaration, cast in the statement where the type cannot be inferred
(`$1::int`). The warnings point out big estimate misses, sequential scans reading many rows, and
sorts or hashes spilling to disk. `include_json` also returns the raw `EXPLAIN` output, for plan
visualizers such as [explain.dalibo.com](https://explain.dalibo.com).

On **MySQL** and **MariaDB**:

- `actual` runs `EXPLAIN ANALYZE FORMAT=JSON` (MySQL 8.3 or later), `EXPLAIN ANALYZE` (MySQL 8.0.18
  or later, Aurora MySQL 3) or `ANALYZE FORMAT=JSON` (MariaDB) in a read-only transaction always rolled
  back. On older MySQL versions, use `estimated`.
- `estimated` runs `EXPLAIN FORMAT=JSON`.

A parameterized statement uses `?` with `values`, one per parameter. The warnings point out large
full scans and big estimate misses. `include_json` also returns the raw `EXPLAIN FORMAT=JSON`
output (with MySQL's `actual` mode, from MySQL 8.3 only: before, its plan is a text tree).

MySQL schemas are databases: `list_tables` and `describe_table` default to the session's database,
and `list_tables` lists every readable database when the session has none. Reads run in a
`READ ONLY` transaction, with a 30 s server-side timeout (`max_execution_time`, or
`max_statement_time` on MariaDB). Each connection drops the `sql_mode` flags that change where a
string ends (`ANSI_QUOTES`, `NO_BACKSLASH_ESCAPES` and the modes that include them, such as `ANSI`),
so the read-only guard reads statements as the server does. An IAM sign-in needs a user created with
`IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'`.

## Kubernetes tools (EKS, always read-only)

| Tool | Description |
|---|---|
| `list_namespaces` | List the namespaces and their phase |
| `list_resource_types` | List the resource types of the cluster, custom resources included |
| `list_resources` | List objects of one type, summarized (status, ready, restarts, age…), with label and field selectors (max 500) |
| `get_resource` | Show one object as JSON |
| `pod_logs` | Read the last lines of a container's logs (max 2000 lines) |
| `list_events` | List recent events, newest first, optionally of one object (max 500) |

Lists and logs are cut at 256 KiB, with a `truncated` flag. `list_events` sorts the first 5000
events the API returns (it does not sort them); on busier clusters its reply carries a `note`
saying so: narrow with a namespace, kind or name.

The server signs in with an EKS token presigned by the AWS SDK, as your IAM identity: what it can read is what the
cluster's RBAC grants you (EKS access entry or `aws-auth`). **Secret values are never returned**:
names, types and keys are, every value reads `<redacted>`. `--mcp-allow-writes` does not apply.

## Cache tools (Redis OSS and Valkey ElastiCache)

| Tool | Description |
|---|---|
| `info` | Show `INFO` sections (server, memory, keyspace, stats, replication by default) and the number of keys |
| `scan_keys` | Find keys with `SCAN` (never `KEYS`), with their type and TTL (max 1000 per call) |
| `get_key` | Read a key of any type (string, hash, list, set, sorted set, stream), with TTL and memory usage |
| `command` | Run one read command (`GET`, `HGET`, `LRANGE`, `ZRANGE`, `TTL`, `XRANGE`, `SLOWLOG GET`…). Reads that may return more than 1000 items are refused: `LRANGE` and `ZRANGE` need a range such as `0 99`, `XRANGE` a `COUNT`, `ZRANGEBYSCORE` a `LIMIT`, `SRANDMEMBER` and `HRANDFIELD` a count from 1 to 1000; `get_key` reads whole keys. |
| `execute` | Run one command that modifies data. **Only with `--mcp-allow-writes`.** `FLUSHALL`, `FLUSHDB`, `KEYS`, `CONFIG`, scripts, blocking, Pub/Sub, replication and connection-state commands stay refused. |

Replies of `get_key`, `command` and `execute` are capped: strings and the items of collections are
cut at 64 KiB, arrays at 1000 items, and items stop being added once a reply reaches 256 KiB, with
a `truncated` flag. A value that is not valid UTF-8 (serialized or compressed data) comes back
base64-encoded as `{"base64": "…"}`, and a hash or stream field name as `"base64:…"`, rather than
with its bytes replaced.

The server signs in the way the cache asks, detected from the ElastiCache API:

| Cache | Sign-in |
|---|---|
| No AUTH token, no user group | none |
| AUTH token | password (user `default`), typed in the wizard or read from Secrets Manager (`--db-secret`) |
| RBAC user group | AWS IAM (`--db-user <user id>`, the wizard suggests the IAM users), a password, or Secrets Manager |

AWS IAM tokens are signed by airlock with your profile's credentials (it needs
`elasticache:Connect` on the cache and the user; the wizard's suggestions also use
`elasticache:DescribeUserGroups` and `elasticache:DescribeUsers`). TLS is used when the cache
encrypts in transit, always for serverless caches. Memcached sessions stay plain tunnels.

### Cluster mode

The server reads `INFO cluster` on first use; with `cluster_enabled:1` (cluster
mode enabled, serverless caches) it switches to a cluster client. When the cache user's ACL refuses
`INFO`, an `EXISTS` on two keys of different slots tells (a cluster answers `CROSSSLOT`); when that
is refused too, the cache is used as standalone and every tool result carries a warning. The nodes announce private
addresses only the jumphost reaches, so airlock opens one more SSM port forward to each node the
first time it is needed (`aws ssm start-session` through the same jumphost, on a free local port),
and keeps it until the session ends. A serverless cache announces its own endpoint, served by the
session's tunnel. Then:

- `get_key`, `command` and `execute` go to the shard of the key; commands over keys of several
  slots answer `CROSSSLOT`. `DBSIZE` adds up the keys of every primary; other commands without key
  (`SLOWLOG GET`, `CLIENT LIST`, `MEMORY STATS`…) run on the first primary by address, and the
  reply's `note` names it;
- `scan_keys` scans the primaries one after the other; its cursor is opaque (it names the node being
  scanned, as `<node address>@<cursor>`): pass it back as is. It fails when that node is no longer a
  primary (failover, resharding): start the scan again;
- `info` reads the sections of every primary (8 at a time), named `<node address> <section>`, and
  adds up their keys;
- only database 0 exists.

The read-only guard does not change: `CLUSTER`, `READONLY` and `ASKING` stay refused, airlock's
client sends what cluster discovery needs.

## DynamoDB tools

| Tool | Description |
|---|---|
| `list_tables` | List the tables of the region |
| `describe_table` | Keys, indexes, billing mode, item count and TTL of a table |
| `get_item`, `query`, `scan` | Read items (replies are capped, with a `truncated` flag) |
| `put_item`, `update_item`, `delete_item` | Modify items. **Only with `--mcp-allow-writes`.** |

A DynamoDB session is the region of a profile: no tunnel, no jumphost, no port. The tools run with
your profile's credentials, so the profile needs `dynamodb:ListTables`, `dynamodb:DescribeTable`,
`dynamodb:DescribeTimeToLive`, `dynamodb:GetItem`, `dynamodb:Query` and `dynamodb:Scan`, plus
`dynamodb:PutItem`, `dynamodb:UpdateItem` and `dynamodb:DeleteItem` for `--mcp-allow-writes`. No SSM
permission is needed, and `--no-mcp` is refused for DynamoDB (the MCP server is all it does).

## S3 tools

| Tool | Description |
|---|---|
| `list_buckets` | List the buckets of the account, with their region |
| `list_objects` | List one level of a bucket under a prefix, or every key with `recursive` |
| `head_object` | Size, type, date, ETag, storage class and user metadata of an object |
| `get_object` | Read an object: text in 256 KB slices, PNG, JPEG, GIF, WebP and PDF up to 3.75 MB, the metadata only for other binaries |
| `put_object`, `delete_object` | Write a text object (with `if_none_match` / `if_match`) or delete one. **Only with `--mcp-allow-writes`.** |

An S3 session is every bucket of a profile, in every region: no tunnel, no jumphost, no port. The
tools run with your profile's credentials, so the profile needs `s3:ListAllMyBuckets` (to open the
session), `s3:ListBucket` and `s3:GetObject` (`s3:GetObjectVersion` with `version_id`, and
`kms:Decrypt` for SSE-KMS objects), plus `s3:PutObject` and `s3:DeleteObject` for
`--mcp-allow-writes`. Compressed objects (`Content-Encoding: gzip`) and SSE-C objects are not read.
`--no-mcp` is refused for S3.

## CloudWatch Logs tools (always read-only)

| Tool | Description |
|---|---|
| `list_log_groups` | List the log groups of the region, by name `prefix` or `pattern`, with retention, stored bytes and class |
| `list_log_streams` | List the streams of a group, the most recently written first |
| `query` | Run a Logs Insights query on one or more groups over a time range (default the last 15 minutes): up to 1000 rows, with the records and bytes scanned. Waits 60 s, then stops the query |
| `filter_events` | Tail a group: its newest events matching a filter pattern over a time range, optionally only some streams (replies are capped, with a `truncated` flag) |

A CloudWatch Logs session is the region of a profile, like DynamoDB: no tunnel, no jumphost, no
port. Times are durations before now (`15m`, `1h`, `7d`) or RFC 3339. Logs Insights bills every GB
it scans: the tools default to a short range and report the bytes scanned. The profile needs
`logs:DescribeLogGroups` (to open the session), `logs:DescribeLogStreams`, `logs:StartQuery`,
`logs:GetQueryResults`, `logs:StopQuery` and `logs:FilterLogEvents`. `--no-mcp` and
`--mcp-allow-writes` are refused.

## Registering the servers in AI clients

The server is registered as `<prefix>-<profile>-<instance>` for a database,
`<prefix>-<profile>-<cluster>-eks` for an EKS cluster and `<prefix>-<profile>-<cache>-cache` for a
cache, `<prefix>-<profile>-dynamodb-<region>` for DynamoDB, `<prefix>-<profile>-s3` for S3 and `<prefix>-<profile>-logs-<region>` for CloudWatch Logs (e.g. `airlock-dev-platform-eks`, `airlock-dev-sessions-cache`), so resources of the same
name never share one, in the configuration of the AI clients chosen by `mcp.clients` (or
`--mcp-clients`):

| Client | Name | Project file (`<client>`) | User file (`<client>:user`) | Detected by `auto` when |
|---|---|---|---|---|
| **Claude Code** | `claude` | `.mcp.json` | `~/.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json` when set), and Claude Desktop's `claude_desktop_config.json` | `.mcp.json` exists (`.claude/` alone is not enough) |
| **VS Code** | `vscode` | `.vscode/mcp.json` | `mcp.json` of the VS Code user settings directory | `.vscode/` exists |
| **Codex** | `codex` | `.codex/config.toml` | `~/.codex/config.toml` (`$CODEX_HOME/config.toml` when set) | `.codex/` exists |
| **Cursor** | `cursor` | `.cursor/mcp.json` | `~/.cursor/mcp.json` | `.cursor/` exists |
| **Gemini CLI** | `gemini` | `.gemini/settings.json` | `~/.gemini/settings.json` | `.gemini/` exists |

`mcp.clients` is `auto` by default: the project files of the clients the working directory uses.
For Claude Code, that means `.mcp.json` is only updated when it already exists: create it (`{}`)
or select `claude` to have airlock write it.
Otherwise it lists clients, comma-separated, each written in its project file, or in its user file
with `:user`. The user file serves every project, wherever airlock runs. `auto` can be combined with
the list, and `none` registers nothing:

```sh
airlock config set mcp.clients claude:user,codex   # Claude Code everywhere, Codex in the current project
airlock config set mcp.clients auto,codex:user     # detected project clients, plus Codex everywhere
airlock --mcp-clients none                        # this run only: register nowhere
```

`claude:user` also registers the servers in **Claude Desktop** when it is installed:
`claude_desktop_config.json` in `~/Library/Application Support/Claude` (macOS) or `%APPDATA%\Claude`
(Windows). Claude Desktop only launches stdio servers, so its entry runs `airlock mcp-proxy <url>`,
which relays the server with the token in `AIRLOCK_MCP_TOKEN`. Claude Desktop reads that file at
startup: restart it to see a server added while it runs.

A client chosen explicitly gets its project file even when its directory does not exist yet. Project
files are never written when the working directory is your home directory: use `:user` there. Each
file is edited in place: the other servers and settings are kept. JSON files must be plain JSON,
without comments. Codex's TOML keeps its comments.

The entry is removed when the session stops. The prefix comes from `mcp.serverPrefix` (default `airlock`)
and is not repeated for profiles that already start with it: prefix `team` with profile `team-prod`
gives `team-prod-<instance>`. A red `WRITES` badge flags the sessions whose server can modify
data, in the sidebar and the compact pills. The dashboard's `MCP` row shows `read-only` or
`WRITES ENABLED`, the server name, its URL and the files it was written to. When no file was
written, it also shows the bearer token so you can configure a client by hand.

> [!IMPORTANT]
> Claude Code reads `.mcp.json` at startup and asks you to approve new project servers. If you open
> a session while Claude Code is already running, use `/mcp` to reconnect. Codex only loads a
> project's `.codex/config.toml` once you trust the project, and reads its servers at startup.

> [!WARNING]
> Claude Code rewrites `~/.claude.json` while it runs, so an entry written in it by `claude:user`
> can be lost if Claude Code saves that file at the same moment. Prefer the project file when
> Claude Code is open.

## Gateway (daemon mode)

With `"mode": "daemon"`, the daemon also serves the **gateway**: one MCP
server on `http://127.0.0.1:7821/mcp` (`mcp.gateway.port`), registered as `airlock` (the
`mcp.serverPrefix`) in the **user-level** files of the clients of `mcp.clients` (`claude:user`,
`codex:user`…; project files never get it). Its URL and token stay the same across daemon restarts,
so an open Claude Code keeps it without `/mcp`. The entry stays when the daemon stops on its own
(idle stop, a signal): it is still valid at the next start, and agents get connection errors until
`airlock` starts the daemon again. `airlock daemon stop` (and `Q`, or `airlock daemon restart`) and
`airlock uninstall` remove it. Through it an agent:

| Tool | Description |
|---|---|
| `list_targets` | The saved connections, direct databases and local Docker databases and caches, or a live search of an AWS profile, each telling whether it opens at once or after your approval |
| `list_sessions` | The running sessions, yours included, with their id and the tools that apply |
| `open_session` | Open a target, with a reason you read |
| `close_session` | Close a session an agent opened |
| `db_*`, `k8s_*`, `cache_*`, `dynamo_*`, `s3_*`, `logs_*` | The tools of the sessions above, with a `session` argument |

An opening asked by an agent shows an **approval dialog** in every open dashboard (`y` allow, `n`
deny, 2 minutes to answer); with no dashboard open it is refused at once. `agent.autoConnect` lets
agents open without asking: `non-prod` (targets outside the production environment) or `all`. Agents
never get writes unless `agent.writes` is `true`, not even on your own sessions opened with writes,
and a write request is always approved in a dashboard, also on a running session whose writes you,
a stack or another agent turned on. A session an agent opened carries `AGENT ·
<name>`, gets no IntelliJ data source nor entry of its own, signs in with the choices saved for that
connection, and closes after `agent.idleTimeout` (`30m`) without a call through the gateway. An agent
opening never signs in to AWS in the background: an expired SSO session is refused (or, when
approved, signs in in the approving dashboard's terminal).

```sh
airlock config set mcp.clients auto,claude:user # register the gateway for Claude Code
airlock config set agent.autoConnect non-prod   # let agents open non-production targets alone
airlock daemon status                            # the gateway line: URL, files, or why it is off
```

## Agent skill

`airlock skills install` writes an `airlock` skill telling agents how to use these
tools (reuse sessions, read-only first, close what they opened): `.claude/skills/airlock/SKILL.md`
for Claude Code and `.agents/skills/airlock/SKILL.md` for VS Code, Codex, Cursor and Gemini CLI.
Without arguments it writes the files of the `mcp.clients` clients (`auto`: those the project uses,
from `.claude`, `.mcp.json`, `.vscode`, `.codex`, `.cursor` or `.gemini`), which a terminal offers
as a checklist of every skill file. Commit them to share the skill with the team, and run the
command again after `airlock update`. `airlock uninstall` removes the `--user` ones.

```sh
airlock skills install               # the mcp.clients files, checklist in a terminal
airlock skills install codex cursor  # only .agents/skills
airlock skills install --user        # ~/.claude/skills and ~/.agents/skills, for every project
```

## Credentials and write access

The MCP server of a database signs in with the session's [sign-in method](connecting.md#database-sign-in):

- **IAM token** (PostgreSQL, MySQL, MariaDB): short-lived tokens generated for the chosen database
  user, never stored.
- **Password**: kept in memory for the session; stored in the OS keychain only when you turn
  **Remember** on.
- **Secrets Manager**: the secret is read when the server signs in, and again after the database
  rejects the password (rotation); only its ARN is remembered.
- **Auto** (SQL Server): the read-only secrets of the instance tagged with
  `modules.sql.readOnlySecretTag`, one per database, or the RDS master user secret with MCP writes.
  Auto read-only needs that tag; **Password** and **Secrets Manager** work without it.

No password is ever written to a file: `saved-connections.json` holds the user, the sign-in method
and the secret ARN only. Read-only mode rests on the query guard, on a read-only transaction
(PostgreSQL, MySQL, MariaDB) and on the user itself: pick a user that is read-only in the database.

**Write access.** `--mcp-allow-writes`, or the `MCP` toggle on the review step, adds the destructive
`execute` tool (for DynamoDB, the `put_item`, `update_item` and `delete_item` tools; for S3,
`put_object` and `delete_object`: see their tables). The server still signs in as the chosen user,
so pick a user that is allowed to write: its grants decide what can change. With **Auto**, SQL
Server switches to the RDS master user secret (administrators only). `--no-mcp` disables the server
entirely.

## Audit trail

Every MCP tool call is recorded. In the dashboard, press `m` on a session with an MCP server to see its MCP
calls: time, tool, database, namespace or cache db, items, duration, the SQL, Kubernetes request or cache command, and errors, including statements the read-only
guard rejected (the last 500 per session).

The calls of every session are also appended to a daily audit log, one JSON object per line:

```
~/.config/airlock/logs/mcp-queries-YYYY-MM-DD.jsonl    # $XDG_CONFIG_HOME/airlock/logs when set
```

```sh
jq -r 'select(.sql) | "\(.time) \(.server) \(.sql)"' ~/.config/airlock/logs/mcp-queries-*.jsonl
jq -r 'select(.command) | "\(.time) \(.server) db\(.db) \(.command)"' ~/.config/airlock/logs/mcp-queries-*.jsonl
```

In daemon mode the MCP gateway adds its own lines: each `open_session` and `close_session` with its
`decision`, and the calls it refuses before they reach a session (a write while `agent.writes` is
off, a read-only session, the wrong kind of tool) as `refused: <reason>`, with the SQL tried:

```sh
jq -r 'select(.decision) | "\(.time) \(.tool) \(.target) \(.decision)"' ~/.config/airlock/logs/mcp-queries-*.jsonl
```

Result rows are never logged. The files are readable by your user only, since statements may
contain sensitive values.
