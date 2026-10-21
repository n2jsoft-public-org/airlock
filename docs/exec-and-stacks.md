# Running commands and project stacks

## Running a command with the tunnel open

`airlock exec` opens a target, runs one command with its connection settings in the environment,
then closes the session and exits with the command's exit status: for scripts, migrations and CI.

```sh
airlock exec rds my-db -- npm run migrate
airlock exec eks my-cluster -- kubectl get pods
airlock exec direct legacy-billing -- ./report.sh
airlock exec rds my-db 15432 --profile prod --db-user app_ro -- psql -c 'select 1'
```

It opens `rds`, `eks`, `elasticache`, `direct` and `docker` targets the way `airlock <type> <name>`
does: the choices saved for the target (profile, region, local port, user, sign-in method, secret),
overridden by `--profile`, `--region`, `--db-user`, `--db-secret` and the local port. It serves no
MCP server and registers no IDE data source, and always runs in its own process, whatever the mode.
It waits, two minutes at most, for the tunnel to listen and the database or cache to accept the
sign-in, then runs the command; any failure closes the tunnel first. The command gets airlock's
standard input and output, and the signals airlock receives (`SIGTERM` from a script or CI;
`Ctrl+C` in a terminal reaches it directly). airlock's own progress goes to stderr.

| Target | Variables |
|---|---|
| every target | `AIRLOCK_HOST`, `AIRLOCK_PORT` (the local end of the tunnel, or the direct database) |
| databases, caches | `AIRLOCK_USER`, `AIRLOCK_PASSWORD`, `AIRLOCK_DATABASE`, when known |
| PostgreSQL | `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE` (`require` through a tunnel), `DATABASE_URL=postgres://…` |
| SQL Server | `SQLCMDSERVER` (`host,port`), `SQLCMDUSER`, `SQLCMDPASSWORD`, `SQLCMDDBNAME`, `DATABASE_URL=sqlserver://…` |
| MySQL, MariaDB | `MYSQL_HOST`, `MYSQL_TCP_PORT`, `MYSQL_PWD`, `DATABASE_URL=mysql://…` (an IAM token needs TLS and the cleartext plugin: `mysql --ssl-mode=REQUIRED --enable-cleartext-plugin`) |
| Redis, Valkey | `REDIS_URL` (`rediss://` with TLS), `AIRLOCK_TLS_SERVER_NAME` (the host the certificate names) |
| EKS | `KUBECONFIG`, the session's own kubeconfig |

The password is the AWS IAM token, the secret's password or the password remembered in the OS
keychain: airlock never prints it, and URLs escape it. Other engines (Oracle, Memcached) only get
`AIRLOCK_HOST` and `AIRLOCK_PORT`.

- **An AWS IAM token is valid 15 minutes.** A long command keeps working if it connects early, as
  connections stay open; one that reconnects later needs a fresh token (`airlock rds-token`).
- **Through a tunnel, certificates name the real host, not `127.0.0.1`.** `sslmode=require` and
  `TrustServerCertificate=true` encrypt without checking the host name; a TLS cache client checks
  it against `AIRLOCK_TLS_SERVER_NAME` (`redis-cli --tls --sni "$AIRLOCK_TLS_SERVER_NAME"`) or skips
  the check.
- **Without a terminal nothing is asked.** An expired AWS sign-in fails (in a terminal it signs in
  as the connect commands do), and a password must come from the keychain or `--db-secret`. A
  target with several read-only secrets fails too: pick one with `--db-secret`.

## Project stacks

An application's repository can commit a `.airlock.json` listing the targets it needs, by stack.
After a clone, `airlock up` opens them all: sessions of the dashboard in CLI mode, of the daemon in
daemon mode, exactly as `airlock <type> <name>` would open each one.

```json
{
  "default": "dev",
  "stacks": {
    "dev": [
      { "type": "rds", "name": "orders-db", "port": 5432, "dbUser": "app_ro" },
      { "type": "eks", "name": "platform" },
      { "type": "elasticache", "name": "sessions", "port": 6379 },
      { "type": "forward", "host": "search.internal", "remotePort": 443, "port": 9200 },
      { "type": "direct", "name": "legacy-billing" }
    ],
    "staging": { "profile": "staging", "region": "eu-west-1", "targets": [
      { "type": "rds", "name": "orders-db" }
    ] }
  }
}
```

```sh
airlock up            # the default stack (or the only one)
airlock up staging
airlock up --list     # the stacks and their targets
airlock up --detach   # (-d) daemon mode: open them, print their addresses, return
airlock down staging  # daemon mode: close the sessions up opened for the stack
airlock exec --stack dev -- npm test   # run a command with the whole stack open
```

airlock reads the file of the working directory, else of the nearest parent: the search stops at
the repository root (the directory holding `.git`), and never reads `$HOME`. A stack is a list of
targets, or an object whose `profile` and `region` apply to the targets that give none.

| Key | Targets | Meaning |
|---|---|---|
| `type` | all | `rds`, `eks`, `elasticache`, `dynamodb`, `s3`, `logs`, `forward`, `direct`, `docker` |
| `name` | all but `forward` | The target, as `airlock <type> <name>` takes it |
| `host`, `remotePort` | `forward` | The host and port to forward to |
| `port` | `rds`, `eks`, `elasticache`, `forward` | The local port; left out, the usual choice |
| `dbUser`, `dbSecret` | `rds`, `elasticache` | As `--db-user` and `--db-secret`: a user, and the name or ARN of a Secrets Manager secret |
| `mcpAllowWrites` | `rds`, `elasticache`, `dynamodb`, `s3`, `direct`, `docker` | As `--mcp-allow-writes` |
| `profile`, `region` | AWS targets | The AWS profile and region |

- **Targets only.** Any other key is an error naming the file, the stack and the target; so are a
  target listed twice and a local port used twice. Settings (mode, tags, MCP clients,
  environments) stay in `config.json` and the organization config, whose locks still apply: a
  disabled module fails its target, a locked `agent.writes` ignores `mcpAllowWrites`.
- **No secrets.** The file is committed: keys that look like a password or a token are refused.
  RDS signs in with IAM or the secret `dbSecret` names; direct and Docker databases use the
  password remembered in the keychain (open them once from the dashboard with "Remember in
  keychain").
- **One by one, in order.** The first target of an AWS profile signs it in, the next ones find the
  sign-in valid. A target whose session is already running (or waiting to reconnect) is reused; one
  an agent opened becomes the stack's, so the gateway no longer closes it when the agent goes idle.
  A reused session keeps its local port: when the stack fixes another `port`, airlock warns and
  leaves the session open; close it and run `airlock up` again to get that port.
  A target that fails does not stop the others: each line says what happened, and airlock waits for Enter before the
  dashboard when one failed.
- **Production is confirmed.** `airlock up` and `airlock exec --stack` list the targets with their
  environment, and `[MCP writes]` when `mcpAllowWrites` turns them on, then ask before opening
  production ones. `airlock up` needs a terminal; without one, `airlock exec --stack` refuses a
  stack with production targets.
- **`airlock up --detach`** (daemon mode) opens the targets in the daemon, starting it when
  needed, prints `<type> <name> → <address>` for each on stdout, and returns without the
  dashboard. A failed target makes it exit with status 1 once the others are open.
- **`airlock down`** closes the daemon sessions `airlock up` opened for that stack of that file
  (each session records it; a symbolic link to the directory gives the same file), even targets
  since removed from the file and sessions waiting to reconnect. A session of one of its targets
  opened otherwise (by hand, or by another stack, such as another worktree's, which is named) is
  kept;
  `airlock down --all` closes every running session of the stack's targets, whoever opened them.
  In CLI mode the sessions belong to the dashboard of `airlock up`: quit it.
- **`airlock exec --stack [stack] -- <command>`** opens every target of the stack that has a
  tunnel or a direct address (`dynamodb`, `s3`, `logs` and `forward` are skipped), runs the command and closes
  them, as `airlock exec` does for one target. The variables of each target get the prefix of its
  name in upper case, characters other than letters and digits becoming `_` (`orders-db`:
  `ORDERS_DB_DATABASE_URL`, `ORDERS_DB_PGHOST`, `ORDERS_DB_AIRLOCK_PORT`); when two names give the
  same prefix, it is the type and the name (`RDS_ORDERS_`, `ELASTICACHE_ORDERS_`), and a leading
  digit gets a `_`. A variable only one target sets is also set without prefix, so a stack with
  one database still gets `DATABASE_URL`; one several targets set (`AIRLOCK_HOST`, two
  `DATABASE_URL`) only exists prefixed. `--profile`, `--region`, `--db-user` and `--db-secret` are
  refused: the file gives them per target.

The file comes from a repository: airlock reads it as untrusted input. It can only name targets you
could already open with your own credentials and settings; nothing in it is run, written to an MCP
client file or IDE file, remembered as your sign-in choices, or changes a setting. Writes it turns on
for agents are still approved in a dashboard each time an agent asks for them.
