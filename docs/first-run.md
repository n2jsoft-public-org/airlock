# First launch

The first time you run `airlock` in a terminal, with no `config.json` yet, a short wizard asks the
settings that change how you use airlock. Run `airlock setup` to open it again later.

## What it asks

1. **Language**: auto (the system's), English or French. The rest of the wizard switches to it.
2. **Organization config**: the URL or path of the config your company provides, or nothing. The
   wizard reads it and shows what it sets and what it locks. When a managed file on your machine
   already sets the organization, the wizard only names it.
3. **Mode**: `cli` (sessions close with airlock) or `daemon` (a background daemon keeps them).
4. **Production environments**: which AWS profiles are production. Without this setting no profile
   is, so the wizard proposes `*prod*`: every profile with `prod` in its name asks for a confirmation
   before opening.
5. **Agent auto-connect**: when an AI agent may open a target without asking you: never, outside
   production, or always.
6. **AI clients**: where airlock registers its MCP servers.

A key the organization locks is not asked: the summary shows it with the organization's value.
Everything else (jumphost tag, region, modules, ports) keeps its default and stays editable with
`airlock config set` or `,` in the dashboard.

## Summary and doctor

The summary lists every answer before anything is written. Saving writes only the values that
differ from what applies already, so a value the organization gives stays the organization's. The
wizard then offers to run `airlock doctor`, which checks the AWS CLI, the Session Manager plugin,
your AWS sign-ins and the jumphost.

## Skipping

`esc` on the first page offers to skip: airlock writes an empty `config.json`, keeps its defaults,
and does not ask again. `ctrl+c` quits without writing anything, so the wizard opens at the next
launch.

The wizard never opens without a terminal, with a `config.json` already there, or for a command
other than the dashboard (`airlock rds …`, `airlock daemon`, MCP, `eks-token`, `rds-token`, `exec`).
