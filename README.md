<div align="center">

# 🔐 airlock

**Reach private AWS databases, clusters and caches from your laptop, with no VPN, no SSH keys and no open ports.**

Guided SSM port forwarding to **RDS**, **EKS** and **ElastiCache** through a jumphost, with
IntelliJ data sources and a guarded **MCP server** so AI assistants can query tunneled databases.

![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-SSM%20Session%20Manager-FF9900?logo=amazonwebservices&logoColor=white)
![MCP](https://img.shields.io/badge/MCP-ready-7C3AED)
![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

[Features](#-features) ·
[Quick start](#-quick-start) ·
[How it works](#-how-it-works) ·
[Usage](#-usage) ·
[AI access (MCP)](#-ai-access-with-mcp) ·
[Configuration](#%EF%B8%8F-configuration) ·
[Jumphost setup](docs/jumphost-setup.md)

</div>

---

## ✨ Features

|  |  |
|---|---|
| 🧭 **Guided wizard** | Pick a recent connection, or go resource type → profile → target → review. Search every target of a profile in one list. |
| 🔑 **Passwordless databases** | RDS sign-in with short-lived AWS IAM tokens. Password and AWS Secrets Manager sign-in are available too, with an optional OS keychain. |
| 🪟 **Parallel sessions** | Open as many tunnels as you need, switch between them with a keystroke. Local ports never collide. |
| 🤖 **AI-ready** | Each database, EKS and cache session serves a guarded MCP server and registers it for Claude Code and VS Code. |
| 🗄 **Direct databases** | Declare the PostgreSQL and SQL Server databases you reach without jumphost, and get the same MCP server, audit log and IDE data source. |
| 🧾 **Audited** | Every MCP call is shown live in the dashboard and written to a daily JSONL audit log. |
| 🧩 **IDE integration** | Registers the tunnel as an IntelliJ data source in `.idea/dataSources.xml`. |
| 🌍 **Region aware** | Switch region at any time from a searchable list (`paris`, `eu-west`, `japan`…). |
| 👥 **Team settings** | Share one config string with `airlock config export` / `import`. |

## 🚀 Quick start

**1. Install.** No Go needed: the installer downloads the latest release for your machine. The
repository is private, so sign in to GitHub once with the [GitHub CLI](https://cli.github.com)
(`brew install gh` or `winget install GitHub.cli`, then `gh auth login`).

macOS and Linux:

```sh
gh api repos/n2jsoft/airlock/contents/install.sh -H 'Accept: application/vnd.github.raw' | sh
```

Windows (PowerShell 7+):

```pwsh
& ([scriptblock]::Create((gh api repos/n2jsoft/airlock/contents/install.ps1 -H 'Accept: application/vnd.github.raw' | Out-String)))
```

**2. Connect.** Open a new terminal, then:

```sh
airlock                      # launch the wizard
airlock rds my-db 5432       # …or connect directly
```

Then point your tools at `localhost:5432`. That's it. Later, `airlock update` keeps it current.

<details>
<summary><b>What the installer does, and its options</b></summary>

It reads the release with `GH_TOKEN`, `GITHUB_TOKEN` or the token of the GitHub CLI, checks the
archive against the release's `checksums.txt`, then installs `airlock` to a folder you own, so
`airlock update` never needs `sudo` or an administrator terminal:

| | macOS and Linux (`install.sh`) | Windows (`install.ps1`) |
|---|---|---|
| Install folder | `~/.local/bin` | `%LOCALAPPDATA%\Programs\airlock` |
| PATH | Added to your shell profile (`.zshrc`, `.bashrc`, `.bash_profile` or fish `conf.d`) | Added to your user PATH |
| Other folder | `--dir <path>` or `AIRLOCK_INSTALL_DIR` | `-InstallDir <path>` or `AIRLOCK_INSTALL_DIR` |
| Given release | `--version <tag>` or `AIRLOCK_VERSION` | `-Version <tag>` or `AIRLOCK_VERSION` |
| Leave PATH alone | `--skip-path` | `-SkipPath` |
| Uninstall ([details](#uninstalling)) | `--uninstall`, with `--purge` for your data | `-Uninstall`, with `-Purge` for your data |

When piped, pass the options after `sh -s --`, for example `… | sh -s -- --dir /opt/bin`. In
PowerShell, add them after the script block: `& ([scriptblock]::Create(…)) -Uninstall`. From a
checkout, run `./install.sh` or `.\install.ps1` directly; if PowerShell refuses to run scripts,
run `install.cmd` instead (from `cmd` or by double-clicking it): it starts the same script without
changing your execution policy.

The installer then warns about another `airlock` left on your PATH, such as one from an earlier
`make install` or `go install` in `GOPATH/bin`, and about a missing AWS CLI or Session Manager
plugin.

</details>

<details>
<summary><b>Install a release by hand</b></summary>

Every push to `main` publishes a [GitHub release](https://github.com/n2jsoft/airlock/releases)
named after its version (`YYYYMMDD-<short sha>`), with archives for macOS, Linux and Windows. With
the GitHub CLI, for example on an Apple silicon Mac:

```sh
gh release download -R n2jsoft/airlock -p '*darwin_arm64.tar.gz'
tar -xzf airlock_*_darwin_arm64.tar.gz airlock && mkdir -p ~/.local/bin && mv airlock ~/.local/bin/
```

</details>

<details>
<summary><b>Build from source (needs Go 1.27+)</b></summary>

From a checkout, `make install` builds to `GOBIN` (or `GOPATH/bin`); on Windows, without `make`,
run `go install ./cmd/airlock`. Without a checkout:

```sh
GOPRIVATE=github.com/n2jsoft \
  go install github.com/n2jsoft/airlock/cmd/airlock@latest
```

On Windows (PowerShell):

```pwsh
$env:GOPRIVATE = 'github.com/n2jsoft'
go install github.com/n2jsoft/airlock/cmd/airlock@latest
```

</details>

### Prerequisites

- **GitHub access** to `n2jsoft/airlock`, through the GitHub CLI (`gh auth login`) or a token in `GH_TOKEN`, to install and update airlock
- **AWS CLI v2** and the **[Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)** (used by `aws ssm start-session`)
- **AWS credentials and profiles** (`aws configure` or AWS SSO), with IAM permissions for the AWS API calls airlock makes, including `ssm:StartSession`.
  When a profile's credentials are missing or expired, airlock runs `aws sso login` for you: the
  dashboard steps aside while it runs, so the browser prompt, URL and code stay readable, then comes back.
- **A running jumphost**: an EC2 instance tagged `Usage=jumphost` ([configurable](#%EF%B8%8F-configuration)) in each account and region you connect to. See [Setting up a jumphost](docs/jumphost-setup.md).

## 🛰 How it works

```mermaid
flowchart LR
  subgraph laptop["Developer machine"]
    tools["IDE · psql · kubectl · redis-cli<br/>AI assistant (MCP)"] -->|"localhost:&lt;port&gt;"| airlock["airlock"]
    airlock --> plugin["AWS CLI +<br/>Session Manager plugin"]
  end
  plugin -->|"HTTPS 443<br/>ssm:StartSession"| ssm["AWS Systems Manager"]
  subgraph vpc["VPC, private subnets"]
    jumphost["EC2 jumphost<br/>tag Usage=jumphost<br/>SSM Agent, no inbound port"]
    rds[("RDS")]
    eks["EKS private<br/>API endpoint"]
    cache[("ElastiCache")]
    jumphost -->|"database port"| rds
    jumphost -->|"443"| eks
    jumphost -->|"cache port"| cache
  end
  jumphost -->|"outbound HTTPS 443<br/>(NAT or VPC endpoints)"| ssm
```

1. **Discover.** airlock finds the target with the AWS APIs (`rds describe-db-instances`,
   `eks describe-cluster`, `elasticache describe-*`) and the jumphost with `ec2 describe-instances`,
   filtered on the jumphost tag.
2. **Tunnel.** It starts `aws ssm start-session` with the `AWS-StartPortForwardingSessionToRemoteHost`
   document. Systems Manager relays the traffic to the jumphost's SSM Agent, which opens the
   connection to the target's endpoint inside the VPC.
3. **Connect.** Your tools connect to `localhost:<port>`.

> [!NOTE]
> Nothing is exposed to the internet. The jumphost has no public IP and no inbound rule; it only
> opens outbound connections to Systems Manager.

## 🧭 Usage

```sh
airlock                              # interactive wizard
airlock rds my-db 5432               # tunnel to an RDS instance on localhost:5432
airlock eks my-cluster 15432         # tunnel to a private EKS API endpoint
airlock elasticache my-cache 6379    # tunnel to an ElastiCache cluster
airlock direct legacy-billing        # a direct database declared with airlock db add
airlock rds my-db 5432 --db-user app_ro
```

<details>
<summary><b>All flags</b></summary>

| Flag | Meaning |
|---|---|
| `-p`, `--profile` | AWS profile to use (the wizard asks when omitted). `--aws-profile` is an alias. |
| `--region` | AWS region (defaults to `AWS_REGION`, then the profile's region) |
| `--db-user` | Database or cache user for AWS IAM authentication |
| `--db-secret` | Secrets Manager secret (ARN or name) holding the database or cache password |
| `--no-ide-datasource` | Do not register the connection in `./.idea/dataSources.xml` |
| `--no-mcp` | Do not serve the MCP server |
| `--mcp-allow-writes` | Let the MCP server modify data (databases and caches) |
| `--jumphost-tag` | Tag of the jumphost instance, as `Key=Value` |
| `--read-only-secret-tag` | Tag of the read-only SQL Server secrets, as `Key=Value` |
| `--mcp-server-prefix` | Prefix of the MCP server names |

</details>

<details>
<summary><b>Shell completion</b></summary>

`airlock completion bash|zsh|fish|powershell` prints the completion script. It completes the
resource type, the targets you connected to (filtered by `--profile` and `--region` when given),
the local port they last used, the AWS profiles and the regions, all without calling AWS.

```sh
airlock completion zsh > "${fpath[1]}/_airlock"      # zsh
airlock completion bash > ~/.local/share/bash-completion/completions/airlock   # bash
airlock completion fish > ~/.config/fish/completions/airlock.fish              # fish
```

</details>

`airlock version` prints the commit the binary was built from, as `YYYYMMDD-<short sha>` (commit
date in UTC, for example `20261001-b50fc20`). It reads `dev` when run with `go run`.

### Updating

`airlock update` installs the latest release in place of the running binary when it is newer than
your build, after checking the download against the release's `checksums.txt`:

```sh
airlock update           # install the latest release when it is newer
airlock update --check   # only tell whether a newer release exists
airlock update --force   # install the latest release even over a newer or unreleased build
```

The repository is private, so airlock reads releases with `GH_TOKEN`, `GITHUB_TOKEN` or the token
of the GitHub CLI (`gh auth login`). When airlock lives in a folder you cannot write, such as
`/usr/local/bin`, run `sudo GH_TOKEN="$(gh auth token)" airlock update` (sudo drops your token) or
reinstall with the [installer](#-quick-start), which installs to a folder you own; on Windows, run
it from an administrator terminal or reinstall with `install.ps1`.

Once a day, in the background, the connect command checks for a newer release; the next launch
then shows `update available` in the wizard, or a line on stderr for a direct connection. Set
`AIRLOCK_NO_UPDATE_CHECK` to any non-empty value (`AIRLOCK_NO_UPDATE_CHECK=1`) to turn the check off.

### Uninstalling

`airlock uninstall` removes airlock however it was installed (installer, `make install` or
`go install`). It lists what it removes and what it keeps, then asks before changing anything:

```sh
airlock uninstall           # remove the binary and the PATH entry its installer added
airlock uninstall --purge   # also remove your settings, history, logs and saved passwords
airlock uninstall --yes     # do not ask (required without a terminal)
```

| | Removed | With `--purge` |
|---|---|---|
| The airlock binary | ✅ | ✅ |
| The installer's PATH entry: the block in your shell profile, or the Windows user PATH entry and folder | ✅ when the folder holds nothing else | ✅ |
| `~/.config/airlock` (`%AppData%\airlock`): settings, direct databases, recent connections, MCP query logs, kubeconfigs | | ✅ |
| Database passwords saved in the OS keychain | | ✅ |

The IntelliJ data sources airlock added to your projects' `.idea/dataSources.xml` stay. On
Windows, `airlock.exe` is deleted right after airlock exits, since Windows cannot delete a running
program; quit any other airlock first. The installers' `--uninstall` / `-Uninstall` (and `--purge`
/ `-Purge`) run the same command, and still remove an airlock too old to have it.

### The wizard

The wizard opens on your **recent connections**, most recently used first. Picking one checks that
the target still exists and opens its review step prefilled. Without any recent connection, it opens
on the resource type step.

| Key | Action |
|---|---|
| any letter · `/` | Filter recent connections by name, type, profile or region: type right away, or press `/` first when the name starts with a shortcut letter (`n`, `s`, `d`, `q`, `j`, `k`…) |
| `n` | Start a new connection (resource type → AWS profile → target → review) |
| `s` | Search a target: choose a profile, then pick among all its RDS, EKS and ElastiCache targets in one list |
| `d` | Forget a recent connection |
| `r` | Switch region (on the resource type, target and search steps) |
| `esc` | Go back one step (from the first step of a wizard opened with `n`, back to the sessions); while AWS answers, cancel |
| `?` | List the keys of the current step |

- `--profile` and `--region` filter the recent connections and skip the profile question.
- Target types that cannot be listed during a search are named in a warning.
- A recent connection whose profile no longer exists asks for another one. A profile that reaches a
  different AWS account than the one the connection was saved in is refused, since same-named targets
  often exist in several accounts.

### Regions

The wizard never asks for a region up front: the header shows the current one, resolved in this
order:

`--region` → `AWS_REGION` → the profile's configured region → `defaultRegion`

Without any of them, the header shows `region none` and the wizard opens the region list before it
looks for targets. Non-interactive runs fail and name the settings to add. Press `r` to switch region
from a searchable list (by code, city or country). Switching on the target step reloads the targets,
or the search, for the new region.

### Database sign-in

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
| **Password** | PostgreSQL, Aurora PostgreSQL and SQL Server targets, with the MCP server on (no `--no-mcp`). |
| **Secrets Manager** | Same as Password. |

Password and Secrets Manager are offered only where airlock itself uses the credentials, that is for
the MCP server. Elsewhere (MySQL, or `--no-mcp`) only IAM token is shown when enabled, and your IDE
asks for the password as before.

- **IAM token.** airlock signs in without a password, generating short-lived AWS IAM tokens. The user
  is prefilled with `--db-user`, then the user saved for that database, then `defaultDbUser`.
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

#### Sign-in check

Once the tunnel is up, airlock checks the sign-in with `SELECT 1` and shows the result on the
**Sign-in** row of the session. With **Auto** on SQL Server, it looks for the instance's read-only
secrets (tag `readOnlySecretTag`), with or without a `dbname`: one is used right away, several are
offered in a dialog (`1`–`9` or `enter`; `esc` decides later). A working secret becomes the session's
sign-in (MCP server, IntelliJ data source, reconnect command) and is remembered in `connections.json`
as a Secrets Manager sign-in, its ARN and user only, so the next connection skips the choice. When a
check fails, the tunnel stays open and the other credentials are offered; if the database rejects a
remembered secret, it is forgotten and the sign-in falls back to **Auto**. Press `a` to choose or
check again. The master user secret is never offered; Auto with MCP writes and engines airlock does
not query (MySQL) are not checked.

#### The OS keychain

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

#### IAM permissions

Secrets Manager sign-in needs `secretsmanager:ListSecrets` and `secretsmanager:GetSecretValue`, plus
`kms:Decrypt` when the secret uses a customer-managed KMS key.

> [!TIP]
> airlock remembers each connection per target (AWS account, region, resource type and name), so
> several profiles that reach one account share it. It stores the last profile and local port, plus the
> user, sign-in method, secret ARN, keychain flag and MCP write access for databases, in
> `~/.config/airlock/connections.json` (`$XDG_CONFIG_HOME/airlock` when set), as `sign_in`,
> `db_secret_arn` and `password_in_keychain`. A saved port that is already in use falls back to a free
> one, and `--db-user` / `--db-secret` / `--mcp-allow-writes` still win. **`connections.json` never
> holds a password.**

### Kubernetes (EKS)

Each EKS session gets its own kubeconfig, written to `~/.config/airlock/kube/`
(`$XDG_CONFIG_HOME/airlock/kube` when set) and removed when the session stops. Its only context
points at the tunnel, checks the certificate against the real cluster endpoint (`tls-server-name`)
and signs in with `aws eks get-token`, with the session's profile and region. Your own
`~/.kube/config` is never touched. A kubeconfig left by an airlock that was killed is removed at
the next start; those of another running airlock are kept.

```sh
export KUBECONFIG=~/.config/airlock/kube/airlock-<profile>-<region>-<cluster>.json
kubectl get namespaces    # k9s, Lens and other clients read it too
```

The session log shows the exact path, and the dashboard's `Kube` row the `KUBECONFIG` value. To
keep your own context of the cluster instead, point it at the tunnel:
`kubectl --server https://localhost:<port> --tls-server-name <endpoint host> get namespaces`.

### Direct databases

Some PostgreSQL and SQL Server databases need no jumphost: you reach them directly (VPN, peering
or a public endpoint) with a user and a password. Declare them once, and airlock opens them like
any other connection, without tunnel nor AWS profile: same MCP server (`airlock-<name>`), audit
log, IntelliJ data source, sign-in check and `PROD` badge.

```sh
airlock db add legacy-billing --engine sqlserver --host billing.db.internal --user mcp_ro --production
airlock db add analytics --engine postgres --host 10.0.4.12 --database events --user reader --tls verify
airlock db list                      # name, engine, address, database, user, TLS, production
airlock db edit analytics --port 6432
airlock db test legacy-billing       # signs in and runs SELECT 1
airlock direct legacy-billing        # opens it in the dashboard
airlock db remove analytics          # also forgets its saved choices and keychain password
```

The dashboard does the same: pick **Direct database** (`4`) on the Type step, then `a` adds a
database, `e` edits the selected one and `x` deletes it. Opened direct databases show up in the
recent connections as `direct · host:port`; `d` forgets one.

| Flag | Default | Meaning |
|---|---|---|
| `--engine` | required | `postgres` or `sqlserver` |
| `--host` | required | Host name or IP address, without scheme or port |
| `--port` | 5432 / 1433 | Port of the database |
| `--database` | `postgres` / the login's default | Database opened when none is named |
| `--user` | required | User signing in with its password |
| `--tls` | `require` | `require` encrypts without checking the certificate; `verify` also checks it, against the host name, with your system's trusted CAs; `disable` sends everything in clear text, for local databases only |
| `--production` | off | Flags the database `PROD` in the wizard and the dashboard |

`airlock db edit` changes only the flags you give; any change but `--production` (renaming it with
`--name`, or another engine, host, port, database, user or TLS mode) forgets the password remembered
before, so that it never reaches another server or user. The dashboard's form and `airlock db
import` do the same.

Definitions live in `~/.config/airlock/databases.json`, next to `config.json`. Passwords never do:
you type the password on the review step, and **Remember** keeps it in the OS keychain.
`airlock direct <name>` without a terminal needs it there.

**Sharing.** `airlock db export [name…]` prints the definitions as one `airlockdb1:` string, without
any password. `airlock db import <string>` lists every database it adds (`+`), changes (`~`, field
by field) or keeps (`=`), then asks before writing; `--yes` skips the question. A database whose
connection changes is marked `(remembered password will be forgotten)`: its password is forgotten
once the import is applied. An import never removes a database.

> [!WARNING]
> Read-only access to a direct **SQL Server** database rests on the permissions of its user: there
> are no read-only secrets as on RDS, and SQL Server has no read-only transaction to fall back on.
> Declare a user that can only read, unless you mean to allow writes.

### Parallel sessions

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
| `x` | Stop the selected session, or close it once ended |
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

- **Ports never collide.** The suggested port skips ports used by other sessions or other local
  processes (a second `5432` RDS gets `5433`). A busy port requested explicitly is rejected.
- **No duplicates.** Connecting twice to the same target (same type, name, profile and region) is
  refused.

## 🤖 AI access with MCP

For **RDS PostgreSQL** and **SQL Server** sessions, **direct databases**, **EKS** clusters and
**Redis / Valkey ElastiCache** caches, airlock serves an MCP server on `127.0.0.1` for as long as the
session runs, so AI assistants can explore them, through the tunnel or, for direct databases, straight
at their server.

**Database tools**

| Tool | Description |
|---|---|
| `list_databases` | List the databases of the instance |
| `list_tables` | List the tables of a database |
| `describe_table` | Show the columns of a table |
| `query` | Run a single read-only `SELECT` / `WITH` (max 1000 rows, 30 s timeout) |
| `execute` | Run a statement that modifies data. **Only with `--mcp-allow-writes`.** |

**Kubernetes tools (EKS, always read-only)**

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

The server signs in with `aws eks get-token`, as your IAM identity: what it can read is what the
cluster's RBAC grants you (EKS access entry or `aws-auth`). **Secret values are never returned**:
names, types and keys are, every value reads `<redacted>`. `--mcp-allow-writes` does not apply.

**Cache tools (Redis OSS and Valkey ElastiCache)**

| Tool | Description |
|---|---|
| `info` | Show `INFO` sections (server, memory, keyspace, stats, replication by default) and the number of keys |
| `scan_keys` | Find keys with `SCAN` (never `KEYS`), with their type and TTL (max 1000 per call) |
| `get_key` | Read a key of any type (string, hash, list, set, sorted set, stream), with TTL and memory usage |
| `command` | Run one read command (`GET`, `HGET`, `LRANGE`, `ZRANGE`, `TTL`, `XRANGE`, `SLOWLOG GET`…) |
| `execute` | Run one command that modifies data. **Only with `--mcp-allow-writes`.** `FLUSHALL`, `FLUSHDB`, `KEYS`, `CONFIG`, scripts, blocking, Pub/Sub, replication and connection-state commands stay refused. |

Replies of `get_key`, `command` and `execute` are capped: strings and the items of collections are
cut at 64 KiB, arrays at 1000 items, and items stop being added once a reply reaches 256 KiB, with
a `truncated` flag.

The server signs in the way the cache asks, detected from the ElastiCache API:

| Cache | Sign-in |
|---|---|
| No AUTH token, no user group | none |
| AUTH token | password (user `default`), typed in the wizard or read from Secrets Manager (`--db-secret`) |
| RBAC user group | AWS IAM (`--db-user <user id>`, the wizard suggests the IAM users), a password, or Secrets Manager |

AWS IAM tokens are signed by airlock with your profile's credentials (it needs
`elasticache:Connect` on the cache and the user; the wizard's suggestions also use
`elasticache:DescribeUserGroups` and `elasticache:DescribeUsers`). TLS is used when the cache
encrypts in transit, always for serverless caches. With cluster mode enabled, only the shard behind
the tunnel is reachable: keys of other shards answer `MOVED`. Memcached sessions stay plain tunnels.

**Registration.** The server is registered as `<prefix>-<profile>-<instance>` for a database,
`<prefix>-<profile>-<cluster>-eks` for an EKS cluster and `<prefix>-<profile>-<cache>-cache` for a
cache (e.g. `airlock-dev-platform-eks`, `airlock-dev-sessions-cache`), so resources of the same
name never share one, in the configuration of the AI clients chosen by `mcpClients` (or
`--mcp-clients`):

| Client | Name | Project file (`<client>`) | User file (`<client>:user`) | Detected by `auto` when |
|---|---|---|---|---|
| **Claude Code** | `claude` | `.mcp.json` | `~/.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json` when set) | `.mcp.json` exists (`.claude/` alone is not enough) |
| **VS Code** | `vscode` | `.vscode/mcp.json` | `mcp.json` of the VS Code user settings directory | `.vscode/` exists |
| **Codex** | `codex` | `.codex/config.toml` | `~/.codex/config.toml` (`$CODEX_HOME/config.toml` when set) | `.codex/` exists |
| **Cursor** | `cursor` | `.cursor/mcp.json` | `~/.cursor/mcp.json` | `.cursor/` exists |
| **Gemini CLI** | `gemini` | `.gemini/settings.json` | `~/.gemini/settings.json` | `.gemini/` exists |

`mcpClients` is `auto` by default: the project files of the clients the working directory uses.
For Claude Code, that means `.mcp.json` is only updated when it already exists: create it (`{}`)
or select `claude` to have airlock write it.
Otherwise it lists clients, comma-separated, each written in its project file, or in its user file
with `:user`. The user file serves every project, wherever airlock runs. `auto` can be combined with
the list, and `none` registers nothing:

```sh
airlock config set mcpClients claude:user,codex   # Claude Code everywhere, Codex in the current project
airlock config set mcpClients auto,codex:user     # detected project clients, plus Codex everywhere
airlock --mcp-clients none                        # this run only: register nowhere
```

A client chosen explicitly gets its project file even when its directory does not exist yet. Project
files are never written when the working directory is your home directory: use `:user` there. Each
file is edited in place: the other servers and settings are kept. JSON files must be plain JSON,
without comments. Codex's TOML keeps its comments.

The entry is removed when the session stops. The prefix comes from `mcpServerPrefix` (default `airlock`)
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

**Credentials.** Nothing is written to disk.

- **PostgreSQL** uses an IAM token for the chosen database user. Read-only mode relies on that user
  being read-only in the database, in addition to the query guard.
- **SQL Server** uses the read-only secrets of the instance tagged with `readOnlySecretTag`, one per
  database. Without that tag, SQL Server read-only access is off.

**Write access.** `--mcp-allow-writes`, or the `MCP` toggle on the review step, adds the destructive
`execute` tool. PostgreSQL still signs in as the chosen database user, so pick a user that is allowed
to write: its grants decide what can change. SQL Server has no IAM sign-in and switches to the RDS
master user secret (administrators only). `--no-mcp` disables the server entirely.

### Audit trail

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

Result rows are never logged. The files are readable by your user only, since statements may
contain sensitive values.

## ⚙️ Configuration

Settings live in `~/.config/airlock/config.json` (`$XDG_CONFIG_HOME/airlock` when set,
`%AppData%\airlock` on Windows). Every key is optional and flags take precedence over the file. An
unknown key or a malformed value is an error that names the file.

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `jumphostTag` | `--jumphost-tag` | `Usage=jumphost` | Tag (`Key=Value`) of the running SSM jumphost EC2 instance |
| `defaultDbUser` | `--db-user` | none (asked) | Database user signing in with AWS IAM |
| `readOnlySecretTag` | `--read-only-secret-tag` | none (SQL Server read-only MCP off) | Tag (`Key=Value`) of the per-database read-only Secrets Manager secrets |
| `mcpServerPrefix` | `--mcp-server-prefix` | `airlock` | Start of the MCP server names |
| `defaultRegion` | `--region` | none (asked) | Region used after `AWS_REGION` and the profile's region |
| `productionProfiles` | none | none | Comma-separated patterns (`*` wildcard, any case) of the AWS profiles flagged as production, e.g. `*prod*,*-live` |
| `mcpClients` | `--mcp-clients` | `auto` | AI clients the MCP servers are registered in: `auto`, `none`, or e.g. `claude:user,codex` (see [AI access with MCP](#-ai-access-with-mcp)) |
| `mode` | `--mode` | `cli` | `cli`: sessions live in each `airlock` and close with it; `daemon`: a background daemon keeps them (see [Daemon mode](#daemon-mode)) |

Profiles matching `productionProfiles` get an orange `PROD` badge everywhere they appear: the
wizard header, profile list, recent connections and review step, and the dashboard's session
cards, pills and details, whose borders turn orange. Naming conventions differ between teams, so
no profile is flagged until you set it, typically in your team's shared settings.

### Commands

```sh
airlock config show              # every setting, its value and where it comes from (--json for effective values)
airlock config path              # print the file location
airlock config init              # write every key with its default and explain each one (--force keeps a .bak)
airlock config set <key> <value> # validate and write one key, e.g. jumphostTag Env=bastion
airlock config unset <key>       # remove a key so its default applies again
```

The file is rewritten indented, with keys in a fixed order. Errors caused by a setting name it. For
example, a jumphost not found with the configured tag points to `jumphostTag`, `airlock config show`
and `--jumphost-tag`.

### Daemon mode

With `"mode": "daemon"`, a background daemon keeps the sessions. `airlock` starts it when needed,
then shows its dashboard: closing the terminal or pressing `q` leaves the sessions running, and
`airlock` in any terminal shows them again. Several dashboards can be open at once; each has its
own selection, and closes on its own. `airlock direct <name>` opens the review step of that
database in the dashboard, so you can type the password, like in CLI mode. `airlock <type> <name>`
opens that target in the daemon, or shows its session when the daemon already runs it (same type,
name and profile, and region when `--region` is given).

```sh
airlock config set mode daemon
airlock --mode daemon    # or choose the mode for one run (--mode cli to leave it)
airlock daemon status    # running or not, version, sessions, views, socket and log
airlock daemon stop      # stop every session, then the daemon
airlock daemon restart   # after an update, or to pick up a new environment
```

| Key | In a daemon view |
|---|---|
| `q`, `ctrl+c` | Leave this view; the sessions keep running |
| `Q` | Stop all and quit: asks first, then stops every session and the daemon |
| `r` | After a lost connection: reconnect (starting the daemon again if needed) |

When the daemon is stopped on purpose (`Q`, `airlock daemon stop`, a signal, or the idle stop),
every view closes and prints `The airlock daemon stopped.` If the connection is lost otherwise (the
daemon crashed), the view shows a banner: `r` reconnects, `q` quits.

The daemon takes its settings from `config.json` and reads it again when it changes: the next
connections use the new values. For that reason, the setting flags `--jumphost-tag`,
`--read-only-secret-tag`, `--mcp-server-prefix` and `--mcp-clients` are refused in daemon mode: use
`airlock config set`, or run with `--mode cli`.

It stops on its own after 10 minutes without running session nor dashboard; ended sessions go
with it. It listens on a socket only you can open (`~/Library/Caches/airlock/daemon/daemon.sock` on
macOS, `~/.cache/airlock/daemon/daemon.sock` on Linux, or under `$XDG_CACHE_HOME` when set,
`%LocalAppData%\airlock\daemon\daemon.sock` on Windows); on macOS and Linux, airlock refuses a
daemon directory owned by another user. It logs to `daemon.log` in airlock's logs
directory, next to the MCP query logs; the previous log is kept as `daemon.log.1`. A daemon that
fails to start writes why there, and `airlock` quotes the end of it.

The IntelliJ data source and the MCP entries of a session are written in the directory of the
terminal that opened it. An AWS sign-in during an opening runs in that terminal too. A session that
drops while its terminal is gone is reconnected automatically, unless AWS asks to sign in again:
then press `r` in any dashboard.

Runs without a terminal (scripts) stay standalone, whatever the mode. `airlock update` replaces
the binary, not the running daemon: when it runs another version, the next `airlock` asks before
restarting it (restarting closes its sessions), and `airlock daemon status` tells too.
`airlock uninstall` lists the running daemon and its sessions among what it will do, and stops them
only after you confirm.

### Sharing settings with your team

A team usually sets the same few keys, for example:

```json
{
  "defaultRegion": "eu-west-1",
  "defaultDbUser": "app_ro",
  "readOnlySecretTag": "Role=read-only",
  "mcpServerPrefix": "team",
  "productionProfiles": "*prod*"
}
```

```sh
airlock config export            # prints the keys set in your file as one string: airlock1:…
airlock config import <string>   # shows what changes, then asks before writing
```

`import` lists every key it adds (`+`), changes (`~`) or keeps (`=`), with what each key does. `--yes`
skips the question. Keys in your file that the string does not set are kept, unless you pass
`--replace`, which removes them (`-`).

> [!WARNING]
> The string is deliberately not human-readable, so review the list before you answer `y`. Its
> checksum only catches a damaged copy and paste; it does not prove who made the string. Keep your
> real preset internal: set it once, then share it with `airlock config export` rather than in a
> public document.

## 🛠 Development

```sh
make run                     # run the CLI
make build                   # build to bin/airlock
make dist                    # build macOS, Linux and Windows archives to dist/ (needs GoReleaser)
make install                 # build and install to GOBIN (make uninstall to remove)
make fmt vet lint test       # format, vet, lint and test
```

See [.claude/CLAUDE.md](.claude/CLAUDE.md) for the architecture and contribution conventions.

Dependency updates are handled by Renovate (`chore(deps): …`), configured in [`renovate.json`](renovate.json).
