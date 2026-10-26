# Installing and updating airlock

## Prerequisites

- **GitHub access** to `n2jsoft/airlock`, through the GitHub CLI (`gh auth login`) or a token in `GH_TOKEN`, to install and update airlock
- **AWS CLI v2** and the **[Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)**, only for RDS, EKS and ElastiCache tunnels (used by `aws ssm start-session`).
  Profiles, discovery, sign-in, DynamoDB, S3 and CloudWatch Logs go through the AWS API and need neither.
- **AWS credentials and profiles** (`aws configure` or AWS SSO), with IAM permissions for the AWS API calls airlock makes, including `ssm:StartSession`.
  When a profile's credentials are missing or expired, airlock signs you in again for you (no AWS CLI needed for that): the
  dashboard steps aside while it runs, so the browser prompt, URL and code stay readable, then comes back.
  Profiles that assume a role with MFA (`mfa_serial`) ask for the code once per role session: see [MFA profiles](connecting.md#mfa-profiles).
- **A running jumphost** (not for DynamoDB, S3 and CloudWatch Logs): an EC2 instance tagged `Usage=jumphost` ([configurable](configuration.md)) in each account and region you connect to. See [Setting up a jumphost](jumphost-setup.md).

`airlock doctor` checks all of these for you: see [Troubleshooting](troubleshooting.md).

## Installer

No Go needed: the installer downloads the latest release for your machine. The
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
| Organization config ([details](organization-config.md)) | `--organization-config <source>` | `-OrganizationConfig <source>` |
| Uninstall ([details](#uninstalling)) | `--uninstall`, with `--purge` for your data | `-Uninstall`, with `-Purge` for your data |

When piped, pass the options after `sh -s --`, for example `… | sh -s -- --dir /opt/bin`. In
PowerShell, add them after the script block: `& ([scriptblock]::Create(…)) -Uninstall`. From a
checkout, run `./install.sh` or `.\install.ps1` directly; if PowerShell refuses to run scripts,
run `install.cmd` instead (from `cmd` or by double-clicking it): it starts the same script without
changing your execution policy.

The installer then warns about another `airlock` left on your PATH, such as one from an earlier
`make install` or `go install` in `GOPATH/bin`, and about a missing AWS CLI or Session Manager
plugin, which only RDS, EKS and ElastiCache tunnels need.

Like `airlock update`, the installer adds a 🚀 to the release it installs: see
[Install tracking](#install-tracking).

## Install a release by hand

Every push to `main` publishes a [GitHub release](https://github.com/n2jsoft/airlock/releases)
tagged with its [semantic version](https://semver.org/) (`vX.Y.Z`), with archives for macOS, Linux and Windows. With
the GitHub CLI, for example on an Apple silicon Mac:

```sh
gh release download -R n2jsoft/airlock -p '*darwin_arm64.tar.gz'
tar -xzf airlock_*_darwin_arm64.tar.gz airlock && mkdir -p ~/.local/bin && mv airlock ~/.local/bin/
```

## Build from source (needs Go 1.27+)

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

## Shell completion

`airlock completion bash|zsh|fish|powershell` prints the completion script. It completes the
resource type, the targets you connected to (filtered by `--profile` and `--region` when given),
the local port they last used, the AWS profiles and the regions, all without calling AWS.

```sh
airlock completion zsh > "${fpath[1]}/_airlock"      # zsh
airlock completion bash > ~/.local/share/bash-completion/completions/airlock   # bash
airlock completion fish > ~/.config/fish/completions/airlock.fish              # fish
```

## Version

`airlock version` prints the release the binary was built from, such as `v1.2.0`. A build from a
later commit prints a Go pseudo-version, such as `v1.2.1-0.20261001120000-b50fc20abcde`, and `dev`
when run with `go run`.

## Updating

`airlock update` installs the latest release in place of the running binary when it is newer than
your build, after checking the download against the release's `checksums.txt`:

```sh
airlock update           # install the latest release when it is newer
airlock update --check   # only tell whether a newer release exists, with its release notes
airlock update --force   # install the latest release even over a newer or unreleased build
```

It prints its steps as it goes: checking the latest release, then downloading and installing it.
After the install, a **What's new** block lists the notes of every release between your version and
the new one, newest first. `--check` prints the same notes when an update is available, without
installing anything.

airlock reads releases with `GH_TOKEN`, `GITHUB_TOKEN` or the token of the GitHub CLI
(`gh auth login`), and without a token anonymously, which needs a public repository: while the
repository is private, a token is required. A repository without any release yet leaves your build
up to date. After an install, `airlock update` records the binary it replaced in `install.json` in
the config folder (`~/.config/airlock`, `%AppData%\airlock` on Windows). When airlock lives in a folder you cannot write, such as
`/usr/local/bin`, run `sudo GH_TOKEN="$(gh auth token)" airlock update` (sudo drops your token) or
reinstall with the [installer](#installer), which installs to a folder you own; on Windows, run
it from an administrator terminal or reinstall with `install.ps1`.

Once a day, in the background, the connect command checks for a newer release; the next launch
then shows `update available` in the wizard, or a line on stderr for a direct connection. Set
`AIRLOCK_NO_UPDATE_CHECK` to any non-empty value (`AIRLOCK_NO_UPDATE_CHECK=1`) to turn the check off.

### Install tracking

After installing a release, the installer and `airlock update` add a 🚀 reaction to that release on
GitHub, with your GitHub account. The reactions of a release then tell who installed it, once per
account. A reaction never holds an install back: when the token cannot write reactions or GitHub
does not answer within 5 seconds, airlock carries on silently. Without a token, there is no reaction. Set `AIRLOCK_NO_TRACKING` to any
non-empty value (`AIRLOCK_NO_TRACKING=1`) to skip it. To list who installed a release:

```sh
gh api repos/n2jsoft/airlock/releases/tags/<tag> --jq .id   # the release id
gh api repos/n2jsoft/airlock/releases/<id>/reactions --jq '.[] | select(.content == "rocket") | .user.login'
```

## Uninstalling

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
| `~/.config/airlock` (`%AppData%\airlock`): settings, the gateway token (`gateway-token`), direct databases, recent connections, MCP query logs, kubeconfigs | | ✅ |
| Database passwords saved in the OS keychain | | ✅ |

The MCP gateway entry in the user-level files of your AI clients (`~/.claude.json`…) is removed too.
The IntelliJ data sources airlock added to your projects' `.idea/dataSources.xml` stay. On
Windows, `airlock.exe` is deleted right after airlock exits, since Windows cannot delete a running
program; quit any other airlock first. The installers' `--uninstall` / `-Uninstall` (and `--purge`
/ `-Purge`) run the same command, and still remove an airlock too old to have it.
