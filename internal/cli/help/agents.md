# start Agent Reference

CLI orchestrator for AI agents. Manages prompt, role, and context injection.

- Global config: ~/.config/start/ (or $XDG_CONFIG_HOME/start/)
- Local config: ./.start/ (--local flag targets this)
- See `start help config` for config structure
- See `start help templates` for template placeholder syntax
- See `start help schemas` for `--json` output shapes and exit codes

Global flags: --color=auto|always|never, --quiet, --verbose, --debug, --local.
Nine commands accept --json (list, library, search, update, doctor, doctor
validate, config get, config list, config settings); see `start help schemas`.

```
start
start --role go-expert
start --role go-expert --model sonnet
start --agent gemini --model flash
start --context project,readme
start --role none
start --context none
start --context none,project
start --dry-run
start prompt "Fix the bug in main.go"
start prompt ./notes.md
start prompt "Explain this" --role teacher
start prompt "Quick question" -c default
start task pre-commit-review
start task review "focus on error handling"
start task tasks:review
start task ./custom-task.md
start describe
start describe go-expert
start describe --global
start describe --local
start get skills:finding/one-by-one
start describe skills:finding/one-by-one
start config
start config list
start config list agent
start config list role
start config list context
start config list task
start config export agent
start config settings
start config settings default_agent claude
start config settings timeout 120
start search golang
start search --tag review
start install golang/code-review
start install skills:finding/one-by-one
start uninstall golang/code-review
start uninstall skills:finding/one-by-one --force
start uninstall claude --force
start uninstall agents:claude --local
start list
start library
start update
start doctor
```

## Interactive Commands

These commands require a TTY and are not suitable for agent use.

```
start config add agent
start config add role
start config add context
start config add task
start config edit claude
start config remove claude
start config open agent
start config order role
```
