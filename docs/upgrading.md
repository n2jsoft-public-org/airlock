# Upgrading from older versions

## Saved connections

The first run of a version with `saved-connections.json` moves `databases.json` and `connections.json` into
`saved-connections.json` and keeps the old files as `databases.json.bak` and `connections.json.bak`.
Remembered keychain passwords are kept. A daemon started before the update keeps its old version
until `airlock daemon restart`.

## Flat config keys

Files written by earlier versions of airlock used flat keys (`jumphostTag`, `mcpGatewayPort`,
`disabledModules`…). They are still read, and `airlock config show` lists them as `deprecated`;
the next `airlock config set` or `unset` writes the file in the [current layout](configuration.md). A setting in the new
layout wins over its flat key. `airlock config set jumphostTag ...` still works and tells the new
name; `disabledModules` is replaced by `airlock config set modules.<name>.enabled false`.

## `productionProfiles`

`productionProfiles` is deprecated: a file that still has it reads it as
`environments = "prod!=<patterns>"`, and `airlock config set environments ...` replaces it. It
refuses a value whose production environment has no patterns while `productionProfiles` still flags
some: keep them with `airlock config set environments 'prod!=<patterns>; <your other environments>'`,
or drop them with `airlock config unset productionProfiles`.
