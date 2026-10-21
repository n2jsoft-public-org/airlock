# Troubleshooting

`airlock doctor` checks what airlock needs, without changing anything (it never signs in, writes a
file nor starts the daemon). Its checks run concurrently in five sections (Installation,
Configuration, AWS profiles, Daemon and MCP gateway, OS keychain); in a terminal, a spinner shows
the sections still running, and each one is printed, in order, as soon as it is done. Every check is
one line (`✓` ok, `!` warning, `✗` failure), with its fix under it, after `→`. A last line counts
the results and gives the time taken:

```text
Installation
  ✓ airlock v1.24.1
  ✓ aws: aws-cli/2.32.12 Python/3.13.9 Darwin/25.0.0 exe/arm64
  ✗ session-manager-plugin is not on the PATH
    → session-manager-plugin is not installed: RDS, EKS and ElastiCache tunnels need it. Install it …

Configuration
  ✓ config /Users/jane/.config/airlock/config.json
  ✓ no organization config

AWS profiles
  ! AWS profile staging: the sign-in is missing or expired
    → run airlock and pick profile staging, or aws sso login --profile staging
  ✓ AWS profile prod (eu-west-3): signed in, account 111111111111
  ! AWS profile prod: missing IAM permission ec2:DescribeInstances to find the jumphost
    → ask your AWS administrator to allow ec2:DescribeInstances for profile prod

Daemon and MCP gateway
  ✓ daemon mode off (mode cli)

OS keychain
  ✓ OS keychain available

✓ 6  ! 2  ✗ 1 · 1.9s
```

It checks the airlock version (and a newer release, unless `AIRLOCK_NO_UPDATE_CHECK` is set),
other `airlock` binaries on the PATH, the AWS CLI and the Session Manager plugin, `config.json`
(invalid or deprecated keys), the organization config, the sign-in and the running jumphost of
every AWS profile (concurrently, 5 s each), the daemon (another version than this airlock), its MCP
gateway port and AI client files, and the OS keychain (5 s: a locked Linux keyring that does not
answer is a warning). It exits with status 1 when a check failed
(✗), 0 otherwise, and 130 when you stop it with `Ctrl+C`.

```sh
airlock doctor                            # every profile, in its own region
airlock doctor --profile prod --region eu-west-3   # -p prod
airlock doctor --json                     # the same results for scripts and support requests
```
