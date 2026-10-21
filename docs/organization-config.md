# Organization config

A company can publish its conventions once (jumphost tag, environments, which modules are on, whether agents
may write) instead of each developer importing a string. Point airlock at one file; its values apply
under your `config.json`, except the keys the file `locked`.

```sh
airlock config set organizationConfig /srv/shared/airlock.json
airlock config set organizationConfig https://config.example.com/airlock.json
airlock config set organizationConfig https://github.com/acme/tools/blob/main/airlock/airlock.json
```

The source is an absolute path, an `https://` URL (no user name in it) or a `github.com` link whose
first segment after `blob/` is the ref. `http://`, relative paths and GitHub Enterprise are not
supported. `organizationConfig` is personal: it is never exported, nor taken from the organization.
To set it at install time, pass `--organization-config <source>` to `install.sh` or
`-OrganizationConfig <source>` to `install.ps1`; a failure only warns.

```json
{
  "settings": {
    "environments": "prod!=*prod*; staging=*stg*",
    "aws": { "jumphostTag": "Usage=jumphost" },
    "agent": { "writes": false }
  },
  "locked": ["agent.writes", "aws.jumphostTag"]
}
```

`settings` uses the layout of `config.json`; `locked` lists dotted keys, or is `"*"` for all of them.
Unknown keys and personal keys are ignored with a warning. A malformed value rejects the whole file:
airlock keeps the previous copy and warns.

Values layer as defaults, organization, `config.json`, flags. A locked key wins over everything
else: the file, `config set` and `unset`, `config import` and the flags (`--jumphost-tag`,
`--read-only-secret-tag`, `--mcp-server-prefix`, `--mcp-clients`, `--mode`). Per-run choices such as
`--region` and `--db-user` stay free. A locked `agent.writes: false` also refuses
`--mcp-allow-writes`, makes the TUI write toggles read-only and keeps every MCP session read-only.
`airlock config show` tells which keys come from the organization and which are locked.

A remote file is refreshed in the background every hour (with its ETag) and cached; the first read
without a cache waits up to 5 s. The daemon refreshes hourly too and follows the changes; an
interactive run keeps the organization it resolved at launch. The changes are shown once, at the next
launch. airlock fails open: an unreachable source uses the cache, or runs on your own settings with a
warning, and a source not refreshed for 24 h warns. `airlock config sync` forces a refresh, then
lists the local values that repeat the organization's, which hide its later changes, and offers to
remove them; it exits 1 when the source cannot be read.

A GitHub file is read with `GH_TOKEN`, `GITHUB_TOKEN`, `gh auth token`, then the token stored in the
keychain by `airlock config organization-token`. A source URL must not carry a token, in its path or
its query: use a GitHub link, read with these tokens.

To enforce it on managed machines, deploy a managed file instead of asking developers to set the
source: `/Library/Application Support/airlock/managed.json` on macOS, `/etc/airlock/managed.json` on
Linux, `%ProgramData%\airlock\managed.json` on Windows. It holds either a pointer,
`{"organizationConfig": "<source>"}`, or the organization file itself, and wins over the local
setting. An invalid managed file is ignored with a warning.

> [!WARNING]
> Locks are policy, not a barrier: the barrier is AWS IAM. Developers can edit their own
> `config.json` and cache, so a lock resists a determined developer only with a managed file that
> holds the organization file inline, or a path they cannot write.
