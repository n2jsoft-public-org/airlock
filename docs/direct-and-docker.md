# Direct and Docker databases

## Direct databases

Some PostgreSQL, SQL Server, MySQL and MariaDB databases need no jumphost: you reach them directly (VPN, peering
or a public endpoint) with a user and a password. Declare them once, and airlock opens them like
any other connection, without tunnel nor AWS profile: same MCP server (`airlock-<name>`), audit
log, IntelliJ data source, sign-in check and environment badge.

```sh
airlock connection add legacy-billing --engine sqlserver --host billing.db.internal --user mcp_ro --environment prod
airlock connection add analytics --engine postgres --host 10.0.4.12 --database events --user reader --tls verify
airlock connection add shop --engine mysql --host shop.db.internal --user reader
airlock connection list                # every saved connection, with its ENV (--direct or --jumphost to filter)
airlock connection edit analytics --port 6432
airlock connection test legacy-billing # signs in and runs SELECT 1
airlock direct legacy-billing          # opens it in the dashboard
airlock connection remove analytics    # also forgets its saved choices and keychain password (--yes skips the question)
```

The dashboard does the same: pick the **Direct database** card on the Type step, then `a` adds a
database, `e` edits the selected one and `x` deletes it. Opened direct databases show up in the
recent connections as `direct · host:port`; `d` forgets one.

| Flag | Default | Meaning |
|---|---|---|
| `--engine` | required | `postgres`, `sqlserver`, `mysql` or `mariadb` |
| `--host` | required | Host name or IP address, without scheme or port |
| `--port` | 5432 / 1433 / 3306 | Port of the database |
| `--database` | `postgres` / the login's default / none | Database opened when none is named; on MySQL and MariaDB, none: the MCP tools list every database and take the database to use |
| `--user` | required | User signing in with its password |
| `--tls` | `require` | `require` encrypts without checking the certificate; `verify` also checks it, against the host name, with your system's trusted CAs; `disable` sends everything in clear text, for local databases only (MySQL and MariaDB never send a cleartext password without TLS) |
| `--environment` | none | Environment of the environments setting; `prod` is production |

`--environment ""` clears the environment. The production environment is always saved as `prod`,
whatever your setting calls it, and `airlock connection import` warns about environments missing from
your `environments` setting.

`airlock connection edit` changes only the flags you give; any change but `--environment` (renaming it with
`--name`, or another engine, host, port, database, user or TLS mode) forgets the password remembered
before, so that it never reaches another server or user. The dashboard's form and `airlock connection
import` do the same.

Definitions live in `~/.config/airlock/saved-connections.json`, next to `config.json`. Passwords
never do: you type the password on the review step, and **Remember** keeps it in the OS keychain.
`airlock direct <name>` without a terminal needs it there. `airlock db …` still works as an alias
of `airlock connection …`.

**Sharing.** `airlock connection export [name…]` prints the definitions as one `airlockdb1:` string, without
any password. Without names, a terminal first lists every database, all selected: `space` unchecks
the ones to leave out, `a` toggles them all, `enter` prints the string. The list is drawn on stderr, so
`airlock connection export | pbcopy` still works; `--all`, or no terminal, exports them all without
asking. `airlock connection import <string>` lists every database it adds (`+`), changes (`~`, field
by field) or keeps (`=`), then asks before writing; `--yes` skips the question. A database whose
connection changes is marked `(remembered password will be forgotten)`: its password is forgotten
once the import is applied. A name held by an entry airlock cannot read is marked `(replaces an
invalid entry)`, and the password remembered for the user it names is forgotten too. An import never
removes a database.

> [!WARNING]
> Read-only access to a direct **SQL Server** database rests on the permissions of its user: there
> are no read-only secrets as on RDS, and SQL Server has no read-only transaction to fall back on.
> Declare a user that can only read, unless you mean to allow writes.

## Docker databases and caches

airlock finds the PostgreSQL, SQL Server, MySQL, MariaDB, Redis and Valkey containers running locally in Docker and
signs in with the credentials of their environment or command: nothing to declare, nothing saved.

```bash
docker run -d --name pg -e POSTGRES_PASSWORD=secret -p 55432:5432 postgres:16
airlock docker pg
```

A cache works the same way:

```bash
docker run -d --name cache -p 6379:6379 redis:7
airlock docker cache
```

- `airlock docker` opens the dashboard on the list of database and cache containers; the Type step also
  offers a **Docker database** card.
- User, password and database come from `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`
  (Bitnami: `POSTGRESQL_*`), or `sa` and `MSSQL_SA_PASSWORD` / `SA_PASSWORD` for SQL Server. A
  password given as a `*_FILE` secret is typed in the dashboard.
- MySQL and MariaDB (official and Bitnami images, container port 3306; an image named `mariadb` is
  MariaDB): `root` and `MYSQL_ROOT_PASSWORD` / `MARIADB_ROOT_PASSWORD`, else `MYSQL_USER` /
  `MYSQL_PASSWORD` (or `MARIADB_*`), and the database of `MYSQL_DATABASE` / `MARIADB_DATABASE`.
  `MYSQL_ALLOW_EMPTY_PASSWORD` / `MARIADB_ALLOW_EMPTY_ROOT_PASSWORD` sign in as `root` without
  password; with `MYSQL_RANDOM_ROOT_PASSWORD` or a `*_FILE` secret, the password is typed in the
  dashboard.
- Redis and Valkey: container port 6379, user `default`; the password comes from `--requirepass`
  in the container command, else `REDIS_PASSWORD` / `VALKEY_PASSWORD`, else there is no auth.
  Agents see them as `type: "docker-cache"` with the `cache_*` tools. Not supported yet: ACL
  users, Redis Cluster, TLS, a password only in a mounted `redis.conf` (the dashboard asks for it
  once refused), a command given as one shell string.
- The engine port must be published (`-p <host-port>:5432`, `:3306`…); TLS is off.
- The active Docker context must be the local daemon: published ports are reached on
  `127.0.0.1`, so remote contexts are not supported.
- MCP writes are on by default. Without a terminal, `--mcp-allow-writes=false` turns them off; in
  the dashboard, the `MCP` toggle of the review does. Agents list the containers with `list_targets` and open them with `open_session`
  (`type: "docker"`, or `"docker-cache"`) under the usual agent policy.
