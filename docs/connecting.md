# Connecting

## Commands

```sh
airlock                              # interactive wizard
airlock rds my-db 5432               # tunnel to an RDS instance on localhost:5432
airlock eks my-cluster 15432         # tunnel to a private EKS API endpoint
airlock elasticache my-cache 6379    # tunnel to an ElastiCache cluster
airlock dynamodb eu-west-3           # DynamoDB tables of a region, for the AI assistant (MCP)
airlock s3                           # S3 buckets of a profile, for the AI assistant (MCP)
airlock logs eu-west-3               # CloudWatch Logs of a region, read-only, for the AI assistant (MCP)
airlock direct legacy-billing        # a direct database declared with airlock connection add
airlock docker pg                    # a database running in a local Docker container
airlock forward search.internal 443  # any host:port the jumphost reaches
airlock rds my-db 5432 --db-user app_ro
airlock exec rds my-db -- npm run migrate   # run a command with the tunnel open
airlock up                           # every target of the project's .airlock.json
```

`airlock exec` and `airlock up` are described in [Running commands and project
stacks](exec-and-stacks.md); `airlock direct` and `airlock docker` in [Direct and Docker
databases](direct-and-docker.md).

**AWS profile.** Without `--profile`, the wizard asks for one. A run that does not ask, such as one
without a terminal, uses `AWS_PROFILE`, then the `default` profile (`airlock exec` first takes the
profile saved for the target).

### All flags

| Flag | Meaning |
|---|---|
| `-p`, `--profile` | AWS profile to use (the wizard asks when omitted; otherwise `AWS_PROFILE`, then `default`). `--aws-profile` is an alias. |
| `--region` | AWS region (defaults to `AWS_REGION`, then the profile's region) |
| `--db-user` | Database or cache user for AWS IAM authentication |
| `--db-secret` | Secrets Manager secret (ARN or name) holding the database or cache password |
| `--no-ide-datasource` | Do not register the connection in `./.idea/dataSources.xml` |
| `--no-mcp` | Do not serve the MCP server |
| `--mcp-allow-writes` | Let the MCP server modify data (databases, caches, DynamoDB tables and S3 objects) |
| `--jumphost-tag` | Tag of the jumphost instance, as `Key=Value` |
| `--read-only-secret-tag` | Tag of the read-only SQL Server secrets, as `Key=Value` |
| `--mcp-server-prefix` | Prefix of the MCP server names |
| `--mcp-clients` | AI clients the MCP servers are registered in: `auto`, `none`, or e.g. `claude:user,codex` (see [AI clients](mcp.md#registering-the-servers-in-ai-clients)) |
| `--mode` | `cli` or `daemon`, for this run (see [Daemon mode](daemon.md)) |

Each setting flag has a key in `config.json`: see [Configuration](configuration.md).

## The wizard

The wizard opens on your **recent connections**, most recently used first. Picking one checks that
the target still exists and opens its review step prefilled. Without any recent connection, it opens
on the resource type step.

| Key | Action |
|---|---|
| any letter · `/` | Filter recent connections by name, type, profile or region: type right away, or press `/` first when the name starts with a shortcut letter (`n`, `s`, `d`, `q`, `j`, `k`…) |
| `n` | Start a new connection (resource type → AWS profile → target → review) |
| `s` | Search a target: choose a profile, then pick among all its RDS, EKS, ElastiCache, DynamoDB, S3 and CloudWatch Logs targets in one list |
| `d` | Forget a recent connection |
| `r` | Switch region (on the resource type, target and search steps) |
| `esc` | Go back one step (from the first step of a wizard opened with `n`, back to the sessions); while AWS answers, cancel |
| `?` | List the keys of the current step |

- `--profile` and `--region` filter the recent connections and skip the profile question.
- Target types that cannot be listed during a search are named in a warning.
- A recent connection whose profile no longer exists asks for another one. A profile that reaches a
  different AWS account than the one the connection was saved in is refused, since same-named targets
  often exist in several accounts.

## Regions

The wizard never asks for a region up front: the header shows the current one, resolved in this
order:

`--region` → `AWS_REGION` → the profile's configured region → `aws.defaultRegion`

Without any of them, the header shows `region none` and the wizard opens the region list before it
looks for targets. Non-interactive runs fail and name the settings to add. Press `r` to switch region
from a searchable list (by code, city or country). Switching on the target step reloads the targets,
or the search, for the new region.

## MFA profiles

A profile that assumes a role with an MFA device works like any other:

```ini
[profile prod-admin]
source_profile = base
role_arn = arn:aws:iam::123456789012:role/Admin
mfa_serial = arn:aws:iam::123456789012:mfa/jane
region = eu-west-3
```

When you pick it, airlock asks for the current code on the terminal (the dashboard steps aside
while you type it, as for an SSO sign-in), then caches the role credentials in the AWS CLI's own
cache, `~/.aws/cli/cache` (a file only you can read). Until the role session ends (one hour unless
the profile sets `duration_seconds`), every airlock process uses them without asking again: the
daemon, the MCP servers, `airlock eks-token` run by kubectl, and the `aws ssm start-session` of the
tunnels. A code typed for the AWS CLI serves airlock too, and the other way round.

A profile chaining a role above an MFA profile (`source_profile = prod-admin`, its own `role_arn`)
uses the same cached credentials: picking it asks for the code of `prod-admin`.

airlock asks again 20 minutes before the session ends, 5 minutes before the AWS CLI does, so a
tunnel's `aws ssm start-session` never needs a code. Where no terminal is at hand (an MCP server,
kubectl), an expired session fails with an error telling you to pick the profile in airlock, or to
run `airlock sso-login --profile <profile>`. `mfa_serial` on a profile without `role_arn` is
ignored, as the AWS CLI does.

When AWS refuses the role or the keys themselves (`AccessDenied` on AssumeRole: a wrong `role_arn`,
trust policy or MFA device; `InvalidClientTokenId`: unknown access keys), airlock does not ask for
a code or sign in again: it reports the error, and `airlock doctor` fails, until the profile is fixed.

## Database sign-in

The wizard ends on a **review step** that summarizes the connection. You can edit the local port,
the sign-in method and the database user there before connecting.

| Key | Action |
|---|---|
| `tab` | Switch field |
| `←` `→` · `space` | Change the **Sign-in** method |
| `enter` | Connect (on the Secret field: search the secrets) |

The **Sign-in** selector offers up to four methods, depending on the target:

| Method | Offered when |
|---|---|
| **IAM token** | The instance has IAM authentication enabled. Default when offered. |
| **Auto** | SQL Server only: the tagged read-only secrets, or the master user secret with MCP writes. |
| **Password** | PostgreSQL, MySQL, MariaDB (Aurora included) and SQL Server targets, with the MCP server on (no `--no-mcp`). |
| **Secrets Manager** | Same as Password. |

Password and Secrets Manager are offered only where airlock itself uses the credentials, that is for
the MCP server. Elsewhere (Oracle, or `--no-mcp`) only IAM token is shown when enabled, and your IDE
asks for the password as before.

- **IAM token.** airlock signs in without a password, generating short-lived AWS IAM tokens. The user
  is prefilled with `--db-user`, then the user saved for that database, then the `modules.sql.defaultDbUser` of
  its engine.
- **Password.** Enter a user and its password. It stays in memory for the MCP server; the IntelliJ
  data source switches to user & password (the IDE asks for the password), and the restart command
  falls back to IAM sign-in. The **Remember** toggle (off by default) stores the password in the OS
  keychain, and the next connection prefills it, masked.
- **Secrets Manager.** Press `enter` on the **Secret** field to search every secret of the account
  and region. The secrets linked to the instance come first: tagged `DBInstanceArn`, the RDS master
  user secret, or a name containing the instance name. The secret is either RDS JSON (`username` and
  `password`) or plain text (then fill the **User** field). It is read before the tunnel opens, so an
  unreadable secret fails fast, and the MCP server reads it again after the database rejects the
  password, which covers rotation. From the CLI, `--db-secret <arn|name>` selects it and wins over the
  saved choice; the restart command of such a session carries `--db-secret`.
- The user feeds the connection hints, the IntelliJ data source, the MCP server and the restart
  command. Instances without IAM authentication or MCP support only show the port.

```sh
airlock rds my-db 5432 --db-secret prod/app-user
```

### Sign-in check

Once the tunnel is up, airlock checks the sign-in with `SELECT 1` and shows the result on the
**Sign-in** row of the session. With **Auto** on SQL Server, it looks for the instance's read-only
secrets (tag `modules.sql.readOnlySecretTag`), with or without a `dbname`: one is used right away, several are
offered in a dialog (`1`–`9` or `enter`; `esc` decides later). A working secret becomes the session's
sign-in (MCP server, IntelliJ data source, reconnect command) and is remembered in `saved-connections.json`
as a Secrets Manager sign-in, its ARN and user only, so the next connection skips the choice. When a
check fails, the tunnel stays open and the other credentials are offered; if the database rejects a
remembered secret, it is forgotten and the sign-in falls back to **Auto**. Press `a` to choose or
check again. The master user secret is never offered; Auto with MCP writes and engines airlock does
not query (Oracle) are not checked.

### The OS keychain

With **Remember**, the password is stored under the service `airlock` in the macOS Keychain, the
Windows Credential Manager or the Linux Secret Service. It is forgotten when you turn **Remember**
off, when the database rejects it, or when you forget the connection from the recent list (`d`).
Without an OS keychain (a headless Linux, for example), the toggle is not offered and the password is
asked every time. A keychain failure never blocks the connection: it appears as a `Warning:` line in
the session log.

> [!WARNING]
> On macOS, airlock reaches the Keychain through `/usr/bin/security`, so the item trusts that tool,
> not airlock. Another process running as the same user can read it without a prompt. It is safer
> than a plaintext file, but it is not per-application isolation.

### IAM permissions

Secrets Manager sign-in needs `secretsmanager:ListSecrets` and `secretsmanager:GetSecretValue`, plus
`kms:Decrypt` when the secret uses a customer-managed KMS key.

> [!TIP]
> airlock remembers each connection per target (AWS account, region, resource type and name), so
> several profiles that reach one account share it. It stores the last profile and local port, plus the
> user, sign-in method, secret ARN, keychain flag and MCP write access for databases, in
> `~/.config/airlock/saved-connections.json` (`$XDG_CONFIG_HOME/airlock` when set), next to the
> direct databases. A saved port that is already in use falls back to a free one, and `--db-user` /
> `--db-secret` / `--mcp-allow-writes` still win. **`saved-connections.json` never holds a password.**
> `airlock connection list` shows every saved connection (`--direct` only the direct databases,
> `--jumphost` only the targets reached through a jumphost); `airlock connection remove <name>`
> forgets one (`rds/my-db`, or its full key when several regions or accounts have one), after
> asking (`--yes` skips the question).

## Kubernetes (EKS)

Each EKS session gets its own kubeconfig, written to `~/.config/airlock/kube/`
(`$XDG_CONFIG_HOME/airlock/kube` when set) and removed when the session stops. Its only context
points at the tunnel, checks the certificate against the real cluster endpoint (`tls-server-name`)
and signs in with `airlock eks-token`, with the session's profile and region. Your own
`~/.kube/config` is never touched. A kubeconfig left by an airlock that was killed is removed at
the next start; those of another running airlock are kept.

```sh
export KUBECONFIG=~/.config/airlock/kube/airlock-<profile>-<region>-<cluster>.json
kubectl get namespaces    # k9s, Lens and other clients read it too
```

The session log shows the exact path, and the dashboard's `Kube` row the `KUBECONFIG` value. To
keep your own context of the cluster instead, point it at the tunnel:
`kubectl --server https://localhost:<port> --tls-server-name <endpoint host> get namespaces`.

## Any host and port

`airlock forward <host> <remote-port> [local-port]` tunnels through the jumphost of the profile and
region to any private host and port it reaches: an OpenSearch domain, an MSK broker, an internal
load balancer, DocumentDB, a private API Gateway endpoint… The host is a name or an IP address,
without scheme nor port. Without a local port, a free one is chosen by the OS; with
`aws.randomLocalPort` set to `false`, it is the remote port, or the next free one.

```sh
airlock forward vpc-search.eu-west-3.es.amazonaws.com 443 9443 --profile dev
curl -k https://localhost:9443/_cluster/health
airlock forward b-1.orders.kafka.eu-west-3.amazonaws.com 9098 --region eu-west-3
```

A forward is a session like the others: its dashboard card, parallel sessions, automatic
reconnection, `c` to copy the local address, the recent connections of the wizard (one per account,
region, host and port) and the completion of the hosts you forwarded to. airlock does not look the
host up, and registers neither an IDE data source nor an MCP server for it: it takes `--profile`,
`--region` and `--jumphost-tag` only. Clients checking TLS certificates see `localhost`: pass them
the real host name (`--resolve`, `tls-server-name`, or skip the check).

## Parallel sessions

While a session runs, press `n` to open another one, with any profile and any resource. Running
sessions stay active, and the start screen marks recent connections that are already open: `enter`
on one of them switches to its session instead of opening a second one.

| Key | Action |
|---|---|
| `n` | Open another connection |
| `tab` · `←` `→` · `1`–`9` | Switch between sessions |
| `m` | Switch the bottom panel between logs and MCP calls (sessions with an MCP server) |
| `↑` `↓` · `pgup` `pgdn` | Scroll the logs; scrolling up pauses them |
| `g` · `home` / `G` · `end` | Jump to the start of the logs / back to the live tail |
| `x` | Stop the selected session, or close it once ended (the 20 sessions that ended last are kept, older ones go on their own) |
| `r` | Restart the selected ended session: same target, local port and sign-in |
| `c` | Copy the local address, the IAM token command, the reconnect command, the `KUBECONFIG` export or the last MCP call |
| `a` | Choose the database credentials of the session, or check them again after a failure |
| `p` | Show the database passwords read from Secrets Manager: the picked secret, or the read-only secret of each database (never the master one). They are read again each time; `c` or `1`–`9` copies one, `esc` hides them |
| `esc` | Cancel a connection being prepared |
| `?` | List every key |
| `q` · `ctrl+c` | Stop all sessions and quit, after a confirmation (`y` / `n`, or `ctrl+c` again) |

`c` copies to the system clipboard (`pbcopy`, `xclip`, `wl-copy`…). Over SSH, or without such a
tool, it asks the terminal to copy through OSC 52, which most terminals support (iTerm2, kitty,
WezTerm, Ghostty, Windows Terminal, tmux with `set-clipboard on`), but not macOS Terminal.

A session that ends, on its own or with `x`, stays in the dashboard with its logs until you close it,
restart it or quit.

**Automatic reconnection.** A session that worked and then drops on its own (SSM idle timeout,
network loss, laptop sleep) reconnects in the background, in the same slot, on the same local port
and with the same sign-in, so your tools keep working. Its card shows `Retry in 5s`, then
`Reconnecting`. Attempts wait 2 s, 5 s, 15 s, 30 s, then 1 min, and stop after the fifth in a row;
a session that then runs for a minute gets fresh attempts. `r` reconnects now, `x` closes the
session instead.

- A session stopped with `x`, or that never listened (its settings are likely wrong), is not
  reconnected.
- A reconnection never signs in: nobody may be there to answer the browser. When the AWS
  credentials have expired, it stops and asks you to press `r`, which signs in again.

- **Ports never collide.** By default (`aws.randomLocalPort` is `true`), a tunnel listens on a free
  port chosen by the OS, unless you give one or the wizard reuses the port saved for the target
  while it is free. With `aws.randomLocalPort` set to `false`, the suggested port is the target's,
  skipping ports used by other sessions or other local processes (a second `5432` RDS gets `5433`).
  A busy port requested explicitly is rejected.
- **No duplicates.** Connecting twice to the same target (same type, name, profile and region; S3 has one session per profile, whatever its region) is
  refused.

## Helper commands

Sessions run these for you; they are useful in scripts and other tools:

```sh
airlock sso-login --profile dev                 # sign a profile in (IAM Identity Center, or the MFA code of a role)
airlock rds-token --hostname my-db.abc123.eu-west-3.rds.amazonaws.com --port 5432 --username app_ro --profile dev --region eu-west-3
airlock eks-token --cluster-name platform --profile dev --region eu-west-3
```

- `airlock sso-login` signs a profile in on the terminal, as airlock does when a sign-in expired:
  the IAM Identity Center browser sign-in, or the MFA code of a role (see [MFA profiles](#mfa-profiles)).
- `airlock rds-token` prints an AWS IAM token for a database user, valid 15 minutes, such as a new
  connection needs once the one [`airlock exec`](exec-and-stacks.md) gave has expired. `--port`
  `--hostname` (the instance endpoint) and `--username` are required; `--port` defaults to `5432`,
  `--profile` to the AWS SDK's default and `--region` to the profile's.
- `airlock eks-token` prints the credential kubectl signs in to an EKS cluster with, in the
  `ExecCredential` format; the kubeconfig of an EKS session runs it (see [Kubernetes
  (EKS)](#kubernetes-eks)).
