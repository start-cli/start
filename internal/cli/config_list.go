package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

// ConfigListItem represents a single configured item for JSON output.
type ConfigListItem struct {
	Category     string            `json:"category"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Bin          string            `json:"bin,omitempty"`
	Command      string            `json:"command,omitempty"`
	DefaultModel string            `json:"defaultModel,omitempty"`
	File         string            `json:"file,omitempty"`
	Prompt       string            `json:"prompt,omitempty"`
	Role         string            `json:"role,omitempty"`
	Required     bool              `json:"required,omitempty"`
	Default      bool              `json:"default,omitempty"`
	Optional     bool              `json:"optional,omitempty"`
	Models       map[string]string `json:"models,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Uses         []string          `json:"uses,omitempty"`
	Source       string            `json:"source"`
	Origin       string            `json:"origin,omitempty"`
}

func buildConfigListItem(m configMatch, scope config.Scope) (ConfigListItem, error) {
	item := ConfigListItem{Category: m.Category, Name: m.Name}
	switch m.Category {
	case "agent":
		agents, _, err := loadAgentsForScope(scope)
		if err != nil {
			return item, err
		}
		_, agent, err := lookupInstalledName(agents, "agent", m.Name)
		if err != nil {
			return item, err
		}
		item.Bin = agent.Bin
		item.Command = agent.Command
		item.DefaultModel = agent.DefaultModel
		item.Description = agent.Description
		item.Models = agent.Models
		item.Tags = agent.Tags
		item.Uses = agent.Uses
		item.Source = agent.Source
		item.Origin = agent.Origin
	case "role":
		roles, _, err := loadRolesForScope(scope)
		if err != nil {
			return item, err
		}
		_, role, err := lookupInstalledName(roles, "role", m.Name)
		if err != nil {
			return item, err
		}
		item.Command = role.Command
		item.Description = role.Description
		item.File = role.File
		item.Optional = role.Optional
		item.Prompt = role.Prompt
		item.Tags = role.Tags
		item.Uses = role.Uses
		item.Source = role.Source
		item.Origin = role.Origin
	case "context":
		contexts, _, err := loadContextsForScope(scope)
		if err != nil {
			return item, err
		}
		_, ctx, err := lookupInstalledName(contexts, "context", m.Name)
		if err != nil {
			return item, err
		}
		item.Command = ctx.Command
		item.Default = ctx.Default
		item.Description = ctx.Description
		item.File = ctx.File
		item.Prompt = ctx.Prompt
		item.Required = ctx.Required
		item.Tags = ctx.Tags
		item.Uses = ctx.Uses
		item.Source = ctx.Source
		item.Origin = ctx.Origin
	case "task":
		tasks, _, err := loadTasksForScope(scope)
		if err != nil {
			return item, err
		}
		_, task, err := lookupInstalledName(tasks, "task", m.Name)
		if err != nil {
			return item, err
		}
		item.Command = task.Command
		item.Description = task.Description
		item.File = task.File
		item.Prompt = task.Prompt
		item.Role = task.Role
		item.Tags = task.Tags
		item.Uses = task.Uses
		item.Source = task.Source
		item.Origin = task.Origin
	default:
		return item, fmt.Errorf("unknown category %q", m.Category)
	}
	return item, nil
}

// JSON output sorts all categories alphabetically; the human-readable display preserves
// injection order for roles and contexts.
func collectConfigListItems(scope config.Scope, category string) ([]ConfigListItem, error) {
	var items []ConfigListItem

	if category == "" || category == "agent" {
		agents, order, err := loadAgentsForScope(scope)
		if err != nil {
			return nil, err
		}
		sort.Strings(order)
		for _, name := range order {
			a := agents[name]
			items = append(items, ConfigListItem{
				Category: "agent", Name: name, Bin: a.Bin, Command: a.Command,
				DefaultModel: a.DefaultModel, Description: a.Description,
				Models: a.Models, Tags: a.Tags, Uses: a.Uses, Source: a.Source, Origin: a.Origin,
			})
		}
	}

	if category == "" || category == "role" {
		roles, order, err := loadRolesForScope(scope)
		if err != nil {
			return nil, err
		}
		sort.Strings(order)
		for _, name := range order {
			r := roles[name]
			items = append(items, ConfigListItem{
				Category: "role", Name: name, Command: r.Command, Description: r.Description,
				File: r.File, Optional: r.Optional, Prompt: r.Prompt,
				Tags: r.Tags, Uses: r.Uses, Source: r.Source, Origin: r.Origin,
			})
		}
	}

	if category == "" || category == "context" {
		contexts, order, err := loadContextsForScope(scope)
		if err != nil {
			return nil, err
		}
		sort.Strings(order)
		for _, name := range order {
			c := contexts[name]
			items = append(items, ConfigListItem{
				Category: "context", Name: name, Command: c.Command, Default: c.Default,
				Description: c.Description, File: c.File, Prompt: c.Prompt,
				Required: c.Required, Tags: c.Tags, Uses: c.Uses, Source: c.Source, Origin: c.Origin,
			})
		}
	}

	if category == "" || category == "task" {
		tasks, order, err := loadTasksForScope(scope)
		if err != nil {
			return nil, err
		}
		sort.Strings(order)
		for _, name := range order {
			t := tasks[name]
			items = append(items, ConfigListItem{
				Category: "task", Name: name, Command: t.Command, Description: t.Description,
				File: t.File, Prompt: t.Prompt, Role: t.Role,
				Tags: t.Tags, Uses: t.Uses, Source: t.Source, Origin: t.Origin,
			})
		}
	}

	return items, nil
}

func addConfigListCommand(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:     "list [category]",
		Aliases: []string{"ls"},
		Short:   "List configuration items",
		Long: `List configured agents, roles, contexts, and tasks.

Without a category, lists all items grouped by category.
With a category (agent, role, context, task), lists only that category.

Plural aliases (agents, roles, contexts, tasks) are accepted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigListCmd,
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	parent.AddCommand(cmd)
}

func runConfigListCmd(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	scope := config.ScopeFromLocal(getFlags(cmd).Local)
	jsonFlag, _ := cmd.Flags().GetBool("json")

	if jsonFlag {
		category := ""
		if len(args) > 0 {
			category = normalizeCategoryArg(args[0])
			if category == "" {
				return usageError(fmt.Errorf("unknown category %q: expected agent, role, context, or task", args[0]))
			}
			if category == "skill" {
				return usageError(fmt.Errorf("skills is not a config-merge module"))
			}
		}
		items, err := collectConfigListItems(scope, category)
		if err != nil {
			return err
		}
		if items == nil {
			items = []ConfigListItem{}
		}
		if err := writeJSON(cmd.OutOrStdout(), items); err != nil {
			return fmt.Errorf("marshalling config list: %w", err)
		}
		return nil
	}

	w := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	if len(args) == 0 {
		if err := listAgents(w, stderr, scope); err != nil {
			return err
		}
		fmt.Fprintln(w)
		if err := listRoles(w, stderr, scope); err != nil {
			return err
		}
		fmt.Fprintln(w)
		if err := listContexts(w, stderr, scope); err != nil {
			return err
		}
		fmt.Fprintln(w)
		return listTasks(w, stderr, scope)
	}

	category := normalizeCategoryArg(args[0])
	if category == "" {
		return fmt.Errorf("unknown category %q: expected agent, role, context, or task", args[0])
	}
	if category == "skill" {
		return usageError(fmt.Errorf("skills is not a config-merge module"))
	}

	switch category {
	case "agent":
		return listAgents(w, stderr, scope)
	case "role":
		return listRoles(w, stderr, scope)
	case "context":
		return listContexts(w, stderr, scope)
	case "task":
		return listTasks(w, stderr, scope)
	}
	return nil
}

func listAgents(w io.Writer, stderr io.Writer, scope config.Scope) error {
	agents, order, err := loadAgentsForScope(scope)
	if err != nil {
		printWarning(stderr, "failed to load agents: %s", err)
	}
	sort.Strings(order)

	tui.ColorAgents.Fprint(w, "agents")
	fmt.Fprintln(w, ":")

	if len(agents) == 0 {
		tui.ColorDim.Fprintln(w, "  none")
		return nil
	}

	defaultAgent := ""
	if cfg, err := loadScopeConfigValue(scope); err == nil {
		defaultAgent = getDefaultAgentFromConfig(cfg)
	}

	for _, name := range order {
		agent := agents[name]
		marker := "  "
		if name == defaultAgent {
			marker = tui.ColorInstalled.Sprint("→") + " "
		}
		source := agent.Source
		if agent.Origin != "" {
			source += ", registry"
		}
		if agent.Description != "" {
			fmt.Fprintf(w, "%s%s ", marker, name)
			tui.ColorDim.Fprint(w, "- "+agent.Description+" ")
			fmt.Fprintln(w, tui.Annotate("%s", source))
		} else {
			fmt.Fprintf(w, "%s%s ", marker, name)
			fmt.Fprintln(w, tui.Annotate("%s", source))
		}
	}
	return nil
}

func listRoles(w io.Writer, stderr io.Writer, scope config.Scope) error {
	roles, order, err := loadRolesForScope(scope)
	if err != nil {
		printWarning(stderr, "failed to load roles: %s", err)
	}

	tui.ColorRoles.Fprint(w, "roles")
	fmt.Fprint(w, ": ")
	fmt.Fprintln(w, tui.Annotate("injection order"))

	if len(roles) == 0 {
		tui.ColorDim.Fprintln(w, "  none")
		return nil
	}

	for _, name := range order {
		role := roles[name]
		source := role.Source
		if role.Origin != "" {
			source += ", registry"
		}
		if role.Description != "" {
			fmt.Fprintf(w, "  %s ", name)
			tui.ColorDim.Fprint(w, "- "+role.Description+" ")
			fmt.Fprintln(w, tui.Annotate("%s", source))
		} else {
			fmt.Fprintf(w, "  %s ", name)
			fmt.Fprintln(w, tui.Annotate("%s", source))
		}
	}
	return nil
}

func listContexts(w io.Writer, stderr io.Writer, scope config.Scope) error {
	contexts, order, err := loadContextsForScope(scope)
	if err != nil {
		printWarning(stderr, "failed to load contexts: %s", err)
	}

	tui.ColorContexts.Fprint(w, "contexts")
	fmt.Fprint(w, ": ")
	fmt.Fprintln(w, tui.Annotate("injection order"))

	if len(contexts) == 0 {
		tui.ColorDim.Fprintln(w, "  none")
		return nil
	}

	for _, name := range order {
		ctx := contexts[name]
		source := ctx.Source
		if ctx.Origin != "" {
			source += ", registry"
		}
		if ctx.Description != "" {
			fmt.Fprintf(w, "  %s ", name)
			tui.ColorDim.Fprint(w, "- "+ctx.Description+" ")
			fmt.Fprint(w, tui.Annotate("%s", source))
		} else {
			fmt.Fprintf(w, "  %s ", name)
			fmt.Fprint(w, tui.Annotate("%s", source))
		}
		if ctx.Required {
			fmt.Fprintf(w, " %s", tui.Bracket("required"))
		}
		if ctx.Default {
			fmt.Fprintf(w, " %s", tui.Bracket("default"))
		}
		if len(ctx.Tags) > 0 {
			fmt.Fprint(w, " ")
			tui.ColorDim.Fprint(w, "tags:")
			fmt.Fprint(w, tui.Bracket("%s", strings.Join(ctx.Tags, ", ")))
		}
		fmt.Fprintln(w)
	}
	return nil
}

func runConfigTaskList(cmd *cobra.Command, _ []string) error {
	fmt.Fprintln(cmd.OutOrStdout())
	scope := config.ScopeFromLocal(getFlags(cmd).Local)
	return listTasks(cmd.OutOrStdout(), cmd.ErrOrStderr(), scope)
}

func listTasks(w io.Writer, stderr io.Writer, scope config.Scope) error {
	tasks, order, err := loadTasksForScope(scope)
	if err != nil {
		printWarning(stderr, "failed to load tasks: %s", err)
	}
	sort.Strings(order)

	tui.ColorTasks.Fprint(w, "tasks")
	fmt.Fprintln(w, ":")

	if len(tasks) == 0 {
		tui.ColorDim.Fprintln(w, "  none")
		return nil
	}

	for _, name := range order {
		task := tasks[name]
		source := task.Source
		if task.Origin != "" {
			source += ", registry"
		}
		if task.Description != "" {
			fmt.Fprintf(w, "  %s ", name)
			tui.ColorDim.Fprint(w, "- "+task.Description+" ")
			fmt.Fprintln(w, tui.Annotate("%s", source))
		} else {
			fmt.Fprintf(w, "  %s ", name)
			fmt.Fprintln(w, tui.Annotate("%s", source))
		}
	}
	return nil
}
