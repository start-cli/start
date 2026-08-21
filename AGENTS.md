# start - AI Agent CLI Orchestrator

`start` is a command-line orchestrator for AI agents built on CUE. It manages prompt composition, context injection, and workflow automation by wrapping AI CLI tools (Claude, Gemini, GPT, etc.) with configurable roles, reusable tasks, and project-aware context documents.

## Project Status

Active development. The CLI is fully implemented with commands for agent launching, config management, module installation, and diagnostics. Built on CUE for type-safe, order-preserving configuration.

### Active Project

When an active project is set, continue by reading it. `none` means no project is queued.

Active Project: None. 03 and 04 are delayed.

When a project is complete, update this file to point to the next active project (or `none` if nothing is queued)

## Build & Test

```bash
go build ./...                  # Build all packages
go build -o start ./cmd/start   # Build the CLI binary
scripts/invoke-tests            # Run the full pipeline (lint + tests)
scripts/invoke-linter           # Run golangci-lint only
scripts/invoke-linter -- --fix  # Apply auto-fixes
go test ./internal/...          # Run all internal package tests
go test ./internal/cli/...      # Run CLI tests only
```

Testing key principles:
- Test real behaviour over mocks (use actual CUE validation, real files via `t.TempDir()`)
- Design functions to accept interfaces/parameters rather than reaching for globals
- Use table-driven tests for multiple cases
- Existing tests use `setupStartTestConfig(t)` with `.start/` dir in temp, `os.Chdir`, and `$HOME` isolation
- `registry.NewClient()` connects to the real CUE registry. The resolver acquires its index through a single substitutable collaborator, the `indexSource` seam — one injection point that replaced the former `skipRegistry`, injected-index, and `testClient` affordances. `newResolver` wires the production source by default, or an offline source (nil index) when `offlineRegistryForTests` is set, which the cli test binary does in `TestMain` to keep resolver-backed surfaces offline. Tests substitute the source rather than hitting the network: an offline source (`newTestResolver`), a pre-loaded index (`newResolverWithIndex`), or a recording source that reports the live-vs-cache-gated decision (`newRecordingResolver`)
- `registry.Client` is an interface; registry-touching commands obtain their client through a per-instance provider (`getProvider(cmd)()`) stored on the command context, not by calling `registry.NewClient()` directly. New registry-backed code should consume the interface through the provider so it stays stubbable. Two paths are not on the seam and still call `registry.NewClient()` directly: the resolver's production index source (`productionIndexSource` in `resolve.go`, the lone `registry.NewClient()` caller behind the resolver's index-source seam, powering `describe`/`get`/auto-install — the resolver holds no `cmd`), and first-run auto-setup (`internal/orchestration/autosetup.go` — it cannot import `cli`). Neither emits `--json`; migrate them only if they need offline stubbing
- Offline `--json` coverage for `library`/`search`/`update`/`doctor`: `setupStartTestConfigWithRegistry(t, idx)` isolates config plus a stub client, and `captureJSON(t, stub, args...)` runs a command with the stub injected and returns decoded JSON. `doctor validate` is excluded from the offline path; its `--json` shape is asserted by a `//go:build registry` integration test run with `go test -tags=registry`

## Commit Convention

This repo uses Scoped Commits (https://scopedcommits.com).

- Format: `<scope>: <description>`, optional body, optional trailers
- Scope is the subsystem, module, or area touched (e.g. `cli`, `modules`, `doctor`, `docs`)
- Multiple scopes are fine — list them comma-separated (e.g. `cli, modules: ...`)
- No `feat`/`fix` type prefix; the scope and description carry the meaning
- A single commit may span multiple scopes; do not split a change that belongs together

## Commands

```bash
start                           # Start interactive session with default role
start --role go-expert          # Start with specific role
start task pre-commit-review    # Run a specific task
start describe                  # List all installed modules grouped by category
start describe <name>           # Inspect a module; auto-installs prompt modules if needed (TTY: Markdown file body styled)
start describe skills:<name>    # Inspect a skill (fetched, not installed); shows files and dests
start get <name>                # Output module content to stdout (pipe-clean; TTY: Markdown styled)
start get skills:<name>         # Print SKILL.md from the library (no dest or inventory write)
start install <pkg>             # Install a module from the library
start install skills:<name>     # Materialise a skill into detected (or --agent) dests; records skills.cue
start uninstall <name>          # Remove installed modules (aliases: remove, rm); --force skips confirm
start list                      # List installed modules
start library                   # Show the available module library
start update                    # Update installed modules
start config list               # List configuration entries
start alias                     # List personal command aliases
start alias set pc task review  # Save an alias (value is the command without 'start')
start <alias>                   # Run a saved alias (e.g. 'start pc')
start search <term>             # Search installed config and the module registry
start doctor                    # Diagnose installation and configuration
start doctor validate           # Maintainer check: index/registry/tag consistency + uses references
start help schemas              # --json output shapes and exit-code reference
start prompt                    # Compose and preview a prompt
echo "summarise" | start        # Pipe text as a one-shot prompt (required contexts only)
echo "..." | start prompt       # Pipe text to fill prompt's [text] arg
echo "..." | start task review  # Pipe text to fill task's [instructions] arg
```

### Aliases

Personal, global-only shortcuts that expand a leading token into a saved `start`
command. An alias value is the saved argv minus the leading `start`, captured
verbatim and spliced back in before cobra dispatch (single-pass, never re-parsed
by a shell). The store is a managed file at `aliases/aliases.cue` under the
global config dir; its subdirectory keeps it out of every directory package
build, so a malformed store never breaks the main config load.

```bash
start alias                       # List all aliases (same as 'start alias list')
start alias set <name> <token>... # Create or update one alias (value captured verbatim)
start alias get <name>            # Show one alias as its expanded command
start alias delete <name>...      # Delete one or more aliases (alias: rm)
start alias open                  # Edit aliases/aliases.cue in $EDITOR
start alias export                # Print the store to stdout
start alias import [file]         # Merge aliases from stdin or a file (--replace to overwrite)
```

### Persistent Flags

| Flag | Short | Description |
| ---- | ----- | ----------- |
| `--agent` | `-a` | Override agent (launch: one library agent; skill install/describe: agentdex catalog ids) |
| `--role` | `-r` | Override role (config name, file path, or http(s) URL); `none` skips role assignment |
| `--model` | `-m` | Override the model |
| `--context` | `-c` | Select contexts (tags, file paths, or http(s) URLs, repeatable); `none` drops auto-loaded required/default contexts (`none,foo` keeps only foo) |
| `--dry-run` | | Preview without launching or writing |
| `--quiet` | `-q` | Suppress non-essential output |
| `--verbose` | | Show detailed output |
| `--debug` | | Debug output (implies --verbose) |
| `--color` | | Colour output: `auto` (default), `always`, `never` |
| `--local` | `-l` | Target local config |
| `--refresh` | | Bypass the 24h index cache and resolve the registry index live (inert on `install`/`update`/`doctor validate`, which already resolve live) |

## Architecture

### Package Structure

| Package | Path | Purpose |
| ------- | ---- | ------- |
| cli | `internal/cli/` | Command implementations (cobra) |
| orchestration | `internal/orchestration/` | Prompt composition and agent execution |
| modules | `internal/modules/` | Module search and installation |
| detection | `internal/detection/` | Detect installed AI CLI tools on PATH |
| cue | `internal/cue/` | CUE configuration loading and validation |
| registry | `internal/registry/` | CUE Central Registry client |
| config | `internal/config/` | Configuration path and settings management |
| doctor | `internal/doctor/` | Diagnostic checks and reporting |
| fault | `internal/fault/` | Cross-cutting error sentinels for exit-code mapping |
| cache | `internal/cache/` | Registry index caching |
| temp | `internal/temp/` | Temporary file and directory management |
| shell | `internal/shell/` | Shell detection and command execution |
| tui | `internal/tui/` | Terminal UI colour and formatting |

### Key Files

| File | Purpose |
| ---- | ------- |
| `internal/modules/candidates.go` | Shared candidate-gathering primitive (`GatherCandidates`) and matchers (`MatchByName` literal name-only, `MatchSearch` regex/tag) consumed by every selecting surface |
| `internal/cli/engine.go` | Unified name-only resolution engine (exact tier → floor → substring/prefix fallback) shared by all surfaces |
| `internal/cli/resolve.go` | Resolver state, surface entry points (`resolveAgent`/`resolveRole`/`resolveContexts`), registry fetch and auto-install |
| `internal/cli/root.go` | Root command factory with all subcommands registered |
| `internal/cli/start.go` | Main `start` command: config loading and execution env setup |
| `internal/cli/task.go` | Task execution command |
| `internal/orchestration/composer.go` | Prompt composition with context injection |
| `internal/orchestration/executor.go` | Agent command execution |
| `internal/cli/exitcodes.go` | Maps `fault` sentinels to semantic exit codes (see `start help schemas`) |
| `internal/cue/keys.go` | Centralized CUE config key constants |

### Resolution Logic

One name-only engine (`engine.go`) resolves every module-selecting surface —
`start task`, `--role`, `--context`, `--agent`, the cross-category
`start get`/`start describe`, and the installed-only `start uninstall`/
`start config remove` — against installed config and the registry index as
two equal sources, de-duplicated by `category:name` (installed wins). The
removal surfaces reuse the same exact→fallback reduction over a source-agnostic
matcher (`selector.match` / `matchSource` in `engine.go`, wired to the
installed-only `installedMatcher` in `internal/cli/removal.go`), but draw
candidates from one scope's installed config only: no registry, no auto-install,
and floor 0 so one- and two-character queries resolve. The match rule, specified
in `docs/module-resolution.md`, is:

1. Interpret the identifier. A leading `./`, `/`, `~`, or `~/` is a filesystem
   path, and an `http(s)://` scheme a remote locator, each read directly (no
   search — the path from disk, the URL by a bounded fetch); `--agent` rejects
   both. A `category:name`
   prefix scopes to that category and selects prefix fallback; a mismatched or
   unknown category is a usage error.
2. Exact-whole-name tier first, for every non-path input. A single case-
   insensitive whole-name match resolves directly — even when the name is a
   substring of longer names, even without a TTY, and including a registry-only
   match (installed then used). This tier is exempt from the floor. A lone
   installed exact resolves offline on the category-specific surfaces; the cross-
   category surfaces also consult the registry here to detect a same-name twin.
3. Fallback tier when no exact match exists, over the names only: a bare term is
   a case-insensitive literal substring, a category-qualified term a literal
   prefix. The query must be at least three characters (counting the name,
   excluding any `category:` prefix). Zero matches is not-found, one is used, and
   more than one menus on a TTY or errors with the list otherwise. A `skills:`
   prefix miss then matches the dest leaf (`skills:one-by-one` →
   `finding/one-by-one`) on get/describe (inventory, then registry) and on
   uninstall/update (inventory only).

Matching is literal and case-insensitive over names only — no regex, no
description/tag matching, no multi-term splitting. The registry index is fetched
lazily and its absence is non-fatal: an uninstalled name is not-found when the
index is reachable, and a transient (retry) error when it is unreachable, since
absence cannot be confirmed. Model resolution (`--model`) is out of scope; it
keeps the search-style match against the agent's `models` map.

### Candidate Gathering

Beneath the per-surface match rules, one shared primitive in `internal/modules`
(`GatherCandidates`) enumerates the candidate set for a scope and a
caller-selected set of sources (installed-only, registry-only, or both), tagging
each candidate by source and config scope and retaining its index entry
(description, tags, origin) — installed entries via `extractIndexEntryFromCUE`,
registry entries from the index — un-deduplicated and unfiltered. Installed
sources are passed as a list of (config value, scope) pairs, so `search` feeds
its separate local and global configs as two while the merged-config resolver
feeds one. Each surface layers its own matcher on top: the resolution surfaces,
the installed-only `start uninstall`/`start config remove`, and the
config-inspection `start config get`/`start config edit` use the literal
name-only matcher (`MatchByName`); `start search` and `start install` use the
regex/tag matcher (`MatchSearch`) over the same candidates, matching names,
descriptions, and tags; `start update` keeps its `collectInstalledModules`
inventory (which carries version/config-file metadata the primitive does not) and
matches a bare query by shared name (`NameMatches`) or category substring. A
`skills:` address uses the same exact→prefix reduction as uninstall over that
inventory, then dest-leaf fallback on a miss. The
installed-over-registry merge and `category:name` de-duplication (`mergeMatches`)
is a resolution-only step layered after gathering, not part of the primitive, so
`search` keeps its local/global/registry sections and `install` keeps each
candidate's registry entry. `start config get`/`start config edit` apply the
literal exact-then-substring rule across all installed categories and return the
full match set rather than reducing to one — `config get --json` stays an array
and a genuine multi-match menus on a TTY — and the exact tier short-circuits the
substring fallback, so a bare exact name that is a prefix of installed siblings
(`claude` alongside `claude/edit`) resolves to just that name. The shared
candidate type lives in `internal/modules` and is reconciled with the engine's
`ModuleMatch` as a type alias so both packages share one representation without
an import cycle.

### Index Caching and Liveness

One rule governs how each command reads the library index version from the
registry, expressed by the shared `decideCachedIndex` primitive (cache read +
`IsFresh` against a 24h window + `modules.ModuleFromOrigin` module-match guard):

- Read-only display commands (`start list --verbose`, `start search`,
  `start library`) are cache-gated. With a fresh cache they resolve the index
  version offline (no registry metadata request); a stale or missing cache
  triggers one resolve and a best-effort cache write. They consume the primitive
  through `resolveDisplayIndexVersion`. Plain `start list` and `start list --json`
  make no registry call at all (the index is only read under `--verbose`).
- Resolution surfaces (`start`, `start task`, `--role`, `--context`, `--agent`,
  and the cross-category `start get` / `start describe`) resolve the index live
  when, and only when, some surface in the invocation has no installed match — it
  is about to auto-install, so the whole invocation must see the latest index,
  like `start install`. An invocation whose every surface is satisfied by an
  installed module stays cache-gated. The decision (`computeWantLive`) is made
  once, up front, as a union over the flag/arg-bound surfaces, interpreting each
  identifier through the single `interpretSurface` function that `resolve()` also
  uses, so a locator or `none`/`default`/empty sentinel surface (which bypasses
  the index) never forces a spurious live resolve. The lone exception is the
  late-bound task-declared role, whose name lives in the task content: it carries
  a targeted late liveness check after the task resolves.
- `start install`, `start update`, and `start doctor validate` already resolve
  live on every invocation and are unchanged. `start doctor` (non-validate) and
  first-run auto-setup are out of scope.

The persistent `--refresh` flag is the single user-facing override of the
cache-gating rule: it forces a live index resolve on the display commands (across
default, `--json`, and `--export`) and ORs into `computeWantLive` on the
resolution surfaces, and is an inert no-op on the already-live commands.

### Module Cross-References (`uses`)

A module may declare an optional `uses` list naming the other library modules it
pulls in at runtime via `start get` (for example a task that runs
`start get contexts:start/library/publishing`). Each entry is a fully-qualified
colon-form address (`category:path`). The field is mirrored on all four config
structs (`AgentConfig`/`RoleConfig`/`ContextConfig`/`TaskConfig`) and decoded
permissively like `tags`; a module without `uses` behaves exactly as before.

- It is preserved through every config writer: install, update, add, edit, and
  reorder all mutate config through one AST layer in `internal/modules`
  (`UpsertConfigModule`/`ReorderConfigCategory`, with entry structs built by
  `FormatModuleStruct` for install/update and the `internal/cli` struct builders
  for add/edit). All iterate the shared `CategoryFieldOrder`, so `uses` and field
  order are retained uniformly and the comment header survives every mutation.
- It surfaces in `config list --json` and `config get --json` through the `Uses`
  field on `ConfigListItem`, populated in both `collectConfigListItems` and
  `buildConfigListItem`. `list --json` (the installed-inventory summary) omits it.
- `start doctor validate` checks every declared `uses` entry: each must be a
  fully-qualified colon-form address whose category is known and whose path
  resolves to an index entry under that category, matched with the same
  case-insensitive whole-name rule (`nameMatches`/`modeExact`) `start get` uses.
  The declarations are read from the modules-repo clone (descending to the module
  value via the shared `modules.DescendToModuleValue` helper), not installed
  config. A malformed or unresolvable entry, or a per-module content load that
  fails to build, becomes a per-module issue — never a propagated error — so the
  declaring module fails itself while the rest of the walk continues.

### Architecture Principles

- CUE-native: All configuration, schemas, and validation in CUE
- Registry-driven: Packages distributed via CUE Central Registry, not a custom GitHub system
- Order-aware: Configuration order preserved for context injection
- Type-safe: CUE schemas prevent configuration errors
- Simple: Let CUE handle complexity instead of building custom systems

## Core Concepts

- Roles: Define AI agent behaviour and expertise (e.g., `go-expert`, `code-reviewer`)
- Tasks: Reusable prompts for common workflows (e.g., `pre-commit-review`, `debug-help`)
- Contexts: Environment-specific information loaded at runtime and injected into prompts
- Agents: AI model configurations (Claude, GPT, Gemini, etc.) with command templates
- Packages: Roles, tasks, and configurations distributed via CUE Central Registry

## Why CUE?

CUE (Configure Unify Execute) provides:
- Order preservation: Configuration order matters for context injection and prompt composition
- Built-in validation: Schema definition and validation are native features
- Type safety: Strong typing prevents configuration errors
- Packages and modules: CUE Central Registry provides proper package distribution
- Templating: Native support for constraints, defaults, and composition
- Data and logic together: Configuration can include validation rules and transformations

## What Changed From Prototype

| Aspect | Prototype (TOML) | This Version (CUE) |
| ------ | ---------------- | ------------------ |
| Config format | TOML (unordered tables) | CUE (ordered, typed) |
| Module distribution | Custom GitHub API system | CUE Central Registry |
| Validation | Custom Go code | CUE schemas |
| Package management | Custom catalog/cache | CUE modules |
| Schema definition | Documentation only | Enforced by CUE |
| Order preservation | Failed assumption | Native support |

## References

- CUE language: [cuelang.org](https://cuelang.org)
- CUE Central Registry: [registry.cuelang.org](https://registry.cuelang.org)

## Library Repository

The `./library/` directory contains the cloned [p3bot/library](https://github.com/p3bot/library) repository for local development and testing. This directory is git-ignored.

Use for: Developing and testing new modules, schemas, and registry content before publishing.

```
library/
├── agents/          # Agent definitions
├── contexts/        # Context definitions
├── docs/            # Library documentation
├── index/           # Registry index module
├── roles/           # Role definitions
├── schemas/         # CUE schema definitions for all module types
└── tasks/           # Task definitions
```
