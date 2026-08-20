# start JSON and Exit Code Reference

The machine-facing contract for driving start programmatically: the `--json`
output shapes and the semantic exit codes.

## JSON Contract

On success a `--json` command writes its documented shape to stdout and exits 0.
On failure it writes a message to stderr, leaves stdout empty, and returns a
non-zero exit code from the table below.

- There is no envelope. Each command emits a bare array or object.
- Diagnostics, prompts, and progress go to stderr, never stdout.
- Parsers should ignore unknown fields so added fields do not break callers.
- Exit code carries the failure class; stderr carries the human message.

## Commands with --json

Nine commands accept `--json`: `list`, `library`, `search`, `update`, `doctor`,
`doctor validate`, `config get`, `config list`, `config settings`.

Content and launch commands (`get`, `prompt`, `task`, `describe`, `install`,
`uninstall`) do not take `--json`.

## Output Shapes

Field lists below mark optional fields with `?`. Optional fields are omitted
when empty.

### list [category]

Array of installed-module objects. Empty result is `[]`.

```
[{ category, name, scope, origin, configFile,
   description?, tags?, models?, version?, latestVersion?, updateAvailable? }]
```

`version`/`latestVersion`/`updateAvailable` populate only under `--verbose`.

### library [category]

Object keyed by category. A category argument narrows it to that one key.

```
{ agents: { <name>: entry }, roles: {...}, contexts: {...}, tasks: {...}, skills: {...} }
entry = { module, description?, tags?, version?, bin? }
```

### search <query>

Array of source sections. Empty result is `[]`.

```
[{ label, path?, results: [ { category, name,
   entry: { module, description?, tags?, version?, bin? } } ] }]
```

`label` is one of `local`, `global`, `registry`.

### update [query]

Array of per-module update results. Empty result is `[]`.

```
[{ module: <list object>, updated, oldVersion?, newVersion?, error? }]
```

`error` is a per-module string; a populated `error` does not fail the command.

### doctor

Object holding diagnostic sections. Exits 1 when issues are found.

```
{ sections: [ { name, summary?,
   results: [ { status, label, message, fix?, details? } ] } ] }
```

`status` is one of `pass`, `fail`, `warn`, `info`, `notfound`.

### doctor validate

Maintainer check; run `start doctor validate --force --json`. Object shape:

```
{ index: { checks: [ { status, label, message? } ] },
  categories: [ { name, modules: [ { name, version?, status, issues? } ] } ],
  stats: { checked, pass, fail } }
```

### config get <query>

Array of config items (same object shape as `config list`). A query that
matches nothing emits `[]`.

### config list [category]

Array of config-item objects. Empty result is `[]`.

```
[{ category, name, source,
   description?, bin?, command?, defaultModel?, file?, prompt?, role?,
   required?, default?, optional?, models?, tags?, uses?, origin? }]
```

### config settings [key]

With no key, an object mapping every setting name to its resolved entry. With a
key, the single entry object.

```
{ <key>: { value, source } }      # no key
{ value, source }                 # one key
source = "default" | "global" | "local" | "not set"
```

## Exit Codes

| Code | Meaning | When |
| ---- | ------- | ---- |
| 0 | Success | Command completed |
| 1 | General | Internal failure or unclassified error; do not retry blindly |
| 2 | Usage | Bad argument, flag, or input value; fix and retry |
| 3 | Not found | A named module, config item, or agent does not exist |
| 4 | Permission | Filesystem permission denied on a user path; fix permissions |
| 5 | Conflict | Reserved; no current producer |
| 75 | Transient | Registry network failure; retry with backoff |
| 78 | Config | Invalid user CUE, or an agent binary missing from PATH; fix the environment |

Exit 75 is the retry signal: a transient registry failure a retry could clear.
A typo'd module name returns 3, never 75. Invalid user configuration returns 78,
never 3.
