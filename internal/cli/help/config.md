# start Configuration Reference

- Global: ~/.config/start/ (or $XDG_CONFIG_HOME/start/)
- Local: ./.start/
- Local overrides global; --local flag targets ./.start/ only

Files: agents.cue, roles.cue, contexts.cue, tasks.cue, settings.cue
Inventory: skills.cue (origin/version for installed skills; not a config-merge module — use start install / start uninstall)

Settings: `default_agent` `shell` `timeout` `library_index`

Context inclusion: `--required` always included; `--default` included when no -c flag; `start prompt` excludes defaults unless `-c default`

```
start config
start config list
start config list agent
start config list role
start config list context
start config list task
start config export agent
start config settings
start config settings default_agent claude
start config settings shell /bin/bash
start config settings timeout 120
start config settings library_index --unset
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

Removal defaults to global scope; `--local` resolves and removes within `./.start/`
only. `start uninstall` is the top-level equivalent.
