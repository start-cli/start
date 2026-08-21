package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHelp_Embedded(t *testing.T) {
	for name, content := range map[string]string{
		"agentsHelp":    agentsHelp,
		"templatesHelp": templatesHelp,
		"configHelp":    configHelp,
		"schemasHelp":   schemasHelp,
	} {
		if content == "" {
			t.Errorf("%s is empty - embed failed", name)
		}
	}
}

func TestAgentsHelp_ContainsExpectedContent(t *testing.T) {
	for _, want := range []string{
		"# start Agent Reference",
		"start help config",
		"start help templates",
		"start task",
		"start prompt",
		"start doctor",
		"start uninstall",
		"start install skills:",
		"start get skills:",
		"start describe skills:",
	} {
		if !strings.Contains(agentsHelp, want) {
			t.Errorf("agentsHelp missing: %s", want)
		}
	}
}

func TestTemplatesHelp_ContainsExpectedContent(t *testing.T) {
	for _, want := range []string{
		"# start Template Reference",
		"{{.prompt}}",
		"{{.role_file}}",
		"{{.instructions}}",
		"{{.file_contents}}",
		"{{.command_output}}",
	} {
		if !strings.Contains(templatesHelp, want) {
			t.Errorf("templatesHelp missing: %s", want)
		}
	}
}

func TestConfigHelp_ContainsExpectedContent(t *testing.T) {
	for _, want := range []string{
		"# start Configuration Reference",
		"~/.config/start/",
		"./.start/",
		"agents.cue",
		"roles.cue",
		"contexts.cue",
		"tasks.cue",
		"settings.cue",
	} {
		if !strings.Contains(configHelp, want) {
			t.Errorf("configHelp missing: %s", want)
		}
	}
}

func TestSchemasHelp_ContainsExpectedContent(t *testing.T) {
	for _, want := range []string{
		"# start JSON and Exit Code Reference",
		"## JSON Contract",
		"## Exit Codes",
		"| 75 | Transient",
		"| 78 | Config",
		"| 2 | Usage",
		"| 3 | Not found",
		// Every --json-capable command must be documented.
		"### list",
		"### library",
		"### search",
		"### update",
		"### doctor",
		"### doctor validate",
		"### config get",
		"### config list",
		"### config settings",
	} {
		if !strings.Contains(schemasHelp, want) {
			t.Errorf("schemasHelp missing: %s", want)
		}
	}
}

func TestHelp_TokenEfficiency(t *testing.T) {
	for name, content := range map[string]string{
		"agentsHelp":    agentsHelp,
		"templatesHelp": templatesHelp,
		"configHelp":    configHelp,
		"schemasHelp":   schemasHelp,
	} {
		words := len(strings.Fields(content))
		estimated := int(float64(words) * 1.3)
		if estimated > 2000 {
			t.Errorf("%s exceeds 2000 token target: ~%d tokens (%d words)", name, estimated, words)
		}
	}
}

func TestHelpCommands_Registered(t *testing.T) {
	root := NewRootCmd()
	var helpCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			helpCmd = c
			break
		}
	}
	if helpCmd == nil {
		t.Fatal("help command not registered")
	}

	want := map[string]bool{"agents": false, "templates": false, "config": false, "schemas": false}
	var schemasCmd *cobra.Command
	for _, c := range helpCmd.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
		if c.Name() == "schemas" {
			schemasCmd = c
		}
	}
	if schemasCmd == nil || !schemasCmd.HasAlias("schema") {
		t.Error("schemas topic should register the 'schema' alias")
	}
	for name, found := range want {
		if !found {
			t.Errorf("help command missing subcommand: %s", name)
		}
	}
}
