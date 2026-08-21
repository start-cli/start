<p align="center">
  <img src="images/start.png" alt="start" width="320">
</p>

[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](https://opensource.org/licenses/MPL-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/p3bot/start)](https://goreportcard.com/report/github.com/p3bot/start)
[![Go Reference](https://pkg.go.dev/badge/github.com/p3bot/start.svg)](https://pkg.go.dev/github.com/p3bot/start)
[![GitHub Tag](https://img.shields.io/github/v/tag/p3bot/start)](https://github.com/p3bot/start/tags)

Context-aware AI agent launcher powered by CUE.

## Why start?

**Stop re-explaining yourself to every AI session.**

Every time you open an AI coding session you provide the same background: what the project does, what role the agent should play, what you're working on today. start eliminates this by composing intelligent prompts from your project's context files and launching your configured AI agent — every time, consistently, with zero ceremony.

- **Role-based sessions** - Define agent expertise once, reuse across projects (`golang/assistant`, `gitlab/teacher`, `cwd/role-md`)
- **Reusable tasks** - Package common workflows as shareable prompts (`github/issue/triage`, `review/git-diff`, `jira/item/read`)
- **Automatic context injection** - Project files, environment info, and documentation included without manual setup
- **Multi-agent support** - Works with Claude, Gemini, aichat, aider, opencode, or any AI CLI tool
- **CUE-powered configuration** - Type-safe, validated, order-preserving config with built-in schema enforcement
- **Skills** - Install library skills into detected agent dests (`finding/one-by-one`)
- **Registry packages** - Install curated roles, contexts, tasks, and skills from the CUE Central Registry

**Perfect for:**

- Developers who run AI coding sessions daily and want consistent context
- Teams sharing prompt engineering patterns across projects
- Anyone tired of repeating themselves at the start of every session

## Quick Start

```bash
# Install
brew tap p3bot/tap
brew trust p3bot/tap
brew install p3bot/tap/start

# Auto-setup detects your installed AI agent and writes initial config
# Launch an AI session with full project context
start

# Use a specific role
start --role golang/agent

# Run a reusable task
start task review/security

# Add extra context to a task
start task git-diff "Only focus on the documentation changes."

# Send a one-off prompt (minimal context, focused output)
start prompt "Explain this error message: 404 Not Found"

# Install a skill into detected agent dests
start install skills:finding/one-by-one

# Print a skill's SKILL.md without installing
start get skills:finding/one-by-one
```

## Installation

### Homebrew (Linux/macOS)

```bash
brew tap p3bot/tap
brew trust p3bot/tap
brew install p3bot/tap/start
```

### Go Install

```bash
go install github.com/p3bot/start/cmd/start@latest
```

### Build from Source

```bash
git clone https://github.com/p3bot/start.git
cd start
go build ./cmd/start
./start --version
```

## How It Works

start is built around four concepts: **agents**, **roles**, **contexts**, and **tasks**. These are all defined in CUE and distributed as packages through the CUE Central Registry.

### Agents

An agent is your AI CLI tool — Claude Code, Gemini, or anything else. You configure which agent to use, and start handles the command construction and process handoff.

```bash
# Use your default configured agent
start

# Switch to a different agent for this session
start --agent gemini
```

_Note: start is not an agent harness, it is a launcher._

To make this clear, here is the command configuration for the Claude Code Interactive agent:

```
command: "{{.bin}} --model {{.model}} --permission-mode default --append-system-prompt-file {{.role_file}} {{.prompt}}"
```

### Roles

A role defines how the AI agent should behave — its expertise, tone, and focus area. Roles become the system prompt for your session.

```bash
# Start with a Golang expert role
start --role golang/assistant

# Use a role from a local file (must start with ./ or /)
start --role ./prompts/senior-reviewer.md
```

Roles are installed from the registry:

```bash
start install golang/teacher
start install git/agent
```

Roles come in three modes:

- agent mode: fully hands off operation
- assistant mode: interactive sessions
- teacher mode: to learn as you build

### Contexts

Contexts are document fragments injected into the prompt such as project overviews, environment details, coding standards, or anything else the agent needs to know. Contexts are tagged and selectively included.

```bash
# Include specific contexts by tag
start --context security,performance

# Include a context from a local file (must start with ./ or /)
start --context ./AGENTS.md
```

Your project's context files (like `AGENTS.md`, `README.md`, or `PROJECT.md`) are mapped to context definitions in config, so start knows exactly what to include and when.

```bash
# Add the ./AGENTS.md context
start install contexts:cwd/agents-md

# Use the ./AGENTS.md context (it is a required context)
start
```

### Tasks

A task is a reusable, parameterisable prompt for a specific workflow. Run a task instead of typing the same instructions repeatedly.

```bash
# Run a configured task
start task review/git-diff

# Pass instructions to a parameterised task
start task github/issue/triage "Implement the feature in issue #87"

# Run a task from a local file (must start with ./ or /)
start task ./tasks/my-review.md
```

Tasks only include required contexts by default, keeping prompts focused. Tasks are also available from the registry:

```bash
start install review/git-diff
start install jira/item/research
```

### Aliases

An alias is a personal, global-only shortcut that expands a leading token into a saved `start` command. The alias value is the command without the leading `start`, captured verbatim and spliced back in before dispatch.

```bash
# Save an alias (value is everything after 'start')
start alias set pc task review/pre-commit

# Run it — expands to: start task review/pre-commit
start pc

# Trailing arguments pass through to the expanded command
start pc "fix the lint errors"

# Aliases can capture flags too
start alias set dev --role go-expert --context cwd/agents-md
start dev
```

Manage the alias store with the `alias` subcommands:

```bash
start alias                       # List all aliases
start alias get pc                # Show one alias as its expanded command
start alias delete pc dev         # Delete one or more aliases (alias: rm)
start alias open                  # Edit the store in $EDITOR
start alias export                # Print the store to stdout
start alias import aliases.cue    # Merge aliases from a file (--replace to overwrite)
```

### Configuration

Configuration is stored in CUE format in `~/.config/start/` (global) and `./.start/` (project-local). Each directory can contain one or more `.cue` files. The `--local` flag targets project config instead of global.

```bash
# View effective configuration
start config

# List all configured items
start config list

# Add a new item interactively
start config add

# Edit an item by name
start config edit claude

# Remove an item (equivalent to the top-level `start uninstall`)
start config remove claude --force

# Show raw config fields for an item
start config get claude

# Open a config file directly in $EDITOR
start config open

# Set a setting
start config settings default_agent claude

# Use project-local config
start --local
```

### Inspection

Use `start describe` to inspect resolved configuration — what agents, roles, contexts, and tasks are actually configured and what their content looks like after merging global and local config:

```bash
# List all configured items with descriptions
start describe

# Search across all categories and dump full detail
start describe golang/assistant
```

The `--global` and `--local` flags restrict output to a single config scope; omitting both shows the effective merged configuration.

On an interactive terminal, `start get` and `start describe` render Markdown content (rendered prompts and `.md`/`.markdown` file bodies) with terminal styling. Piped or redirected output, `--color=never`, and `NO_COLOR` keep the content raw, so `start get` stays pipe-clean for scripting.

### Dry Run

Run the full composition pipeline without launching the agent:

```bash
start --dry-run
start task review/duplication --dry-run
start prompt "My question" --dry-run
```

`start install --dry-run` and `start uninstall --dry-run` resolve and report without writing. `start update --dry-run` previews upgrades.

For launch, task, and prompt, dry run writes the composed inputs to `/tmp/start-<timestamp>/` for post-run inspection:

```
/tmp/start-<timestamp>/
├── role.md       # System prompt (role content)
├── prompt.md     # Full composed prompt
└── command.txt   # Exact command that would execute
```

## Usage

### Core Commands

```bash
# Launch interactive session with full context
start [flags]

# Send a focused one-off prompt
start prompt [text] [flags]

# Run a reusable predefined task
start task <name> [instructions] [flags]
```

### Inspection

```bash
# List all configured items with descriptions
start describe

# Inspect a specific resource by name (searches all categories)
start describe <name>

# Output a module's resolved content to stdout (pipe-clean)
start get <name>
start get contexts:cwd/agents-md
start get skills:finding/one-by-one
```

### Modules Management

```bash
# Show the available module library
start library

# Show details for a specific module
start describe golang/assistant

# Install a package
start install golang/teacher
start install review/git-diff
start install skills:finding/one-by-one

# Remove installed modules (aliases: remove, rm)
start uninstall golang/teacher
start uninstall claude review/git-diff   # multiple at once
start uninstall agents:claude            # category-qualified
start uninstall claude --force           # skip the confirmation prompt
start uninstall claude --local           # target ./.start/ instead of global

# List installed modules
start list

# Update installed packages
start update

# Validate index and module version consistency (maintainer tool)
start doctor validate
```

### Configuration

```bash
# Display current configuration
start config

# List all configured items
start config list

# List by category
start config list agent
start config list role
start config list context
start config list task

# Add a new item (prompts for category if omitted)
start config add
start config add agent

# Edit an item by name (search across all categories)
start config edit
start config edit claude
start config edit gemini/interactive

# Show raw config fields for an item
start config get
start config get claude

# Remove an item (equivalent to the top-level `start uninstall`)
start config remove claude
start config remove claude --force

# Reorder contexts or roles
start config order
start config order context
start config order role

# Open a config file directly in $EDITOR
start config open

# Export config as text to stdout
start config export

# Manage settings
start config settings default_agent claude
```

### Aliases

```bash
# List personal command aliases
start alias

# Save an alias (value is the command without 'start')
start alias set pc task review/pre-commit

# Run a saved alias
start pc

# Inspect, remove, or edit aliases
start alias get pc
start alias delete pc
start alias open
```

### Search and Discovery

```bash
# Search global config, local config, and the module registry index
start search go
```

### Diagnostics

```bash
# Diagnose setup, validate configuration, suggest fixes
start doctor

# Show the --json output shapes and exit-code reference
start help schemas
```

### Shell Completions

```bash
# Install tab-completion for your shell
start completion bash
start completion zsh
start completion fish
```

## CLI Reference

### Global Flags

| Flag         | Short | Description                                             |
| ------------ | ----- | ------------------------------------------------------- |
| `--agent`    | `-a`  | Override agent for this session                         |
| `--role`     | `-r`  | Override role (config name or file path)                |
| `--model`    | `-m`  | Override model selection                                |
| `--context`  | `-c`  | Select contexts (tags or file paths, repeatable)        |
| `--dry-run`  |       | Preview without launching or writing                    |
| `--local`    | `-l`  | Use project-local config (`./.start/`)                  |
| `--quiet`    | `-q`  | Suppress output                                         |
| `--verbose`  |       | Detailed output                                         |
| `--debug`    |       | Debug output (implies `--verbose`)                      |
| `--color`    |       | Colour output: `auto` (default), `always`, `never`      |

`--role none` skips role assignment entirely. `--context none` suppresses the contexts that load automatically (required and default): used alone it yields zero contexts, and combined with selectors it keeps only those — `--context none,project` drops the required/default contexts and loads just `project`. The token is case-insensitive and accepts the aliases `nil`, `off`, and `0`.

### File Path Support

The `--role`, `--context`, and task name arguments accept file paths alongside config names. Detected by prefix:

```bash
start --role ./roles/custom.md
start --context /absolute/path/context.md
start --context ~/shared/project-overview.md
start task ./tasks/my-workflow.md "Additional instructions"
```

### Task Resolution Order

1. Exact full name match in installed configuration
2. Combined search across installed config and registry — merged results presented for selection
3. Auto-install from registry when a single unambiguous match is found

## Contributing

Contributions welcome! Please:

1. Check existing issues: https://github.com/p3bot/start/issues
2. Create an issue for bugs or feature requests
3. Submit pull requests against the `main` branch

## License

`start` is licensed under the [Mozilla Public License 2.0](LICENSE).

## Author

Grant Carthew <grant@carthew.net>
