package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

func addConfigRemoveCommand(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:     "remove [query]",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a config item",
		Long: `Remove an installed module from configuration.

A named query resolves against installed modules across all library categories
(agents, roles, contexts, tasks, skills). If multiple items match, a menu is
presented. With no argument, prompts interactively for a config-merge category
(agent, role, context, or task) and item.

Use --force to skip the confirmation prompt.
Use --dry-run to preview without writing; --force is not required.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runConfigRemove,
	}
	cmd.Flags().Bool("force", false, "Skip confirmation prompt")
	parent.AddCommand(cmd)
}

func runConfigRemove(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	flags := getFlags(cmd)
	force, _ := cmd.Flags().GetBool("force")

	if len(args) == 0 {
		if !isTerminal(stdin) {
			return usageError(fmt.Errorf("interactive remove requires a terminal"))
		}
		return runConfigRemoveInteractive(stdin, stdout, cmd.ErrOrStderr(), flags.Local, force, flags.Quiet, flags.DryRun)
	}

	return runRemoval(cmd, args, flags.Local, force)
}

func runConfigRemoveInteractive(stdin io.Reader, stdout, stderr io.Writer, local bool, skipConfirm bool, quiet bool, dryRun bool) error {
	_, defaultAgent, err := loadRemovalConfig(local)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Remove:")
	category, err := promptSelectCategory(stdout, stdin, allConfigCategories)
	if err != nil || category == "" {
		return err
	}

	names, err := loadNamesForCategory(category, removalScope(local))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintf(stdout, "No %s configured.\n", category)
		return nil
	}

	singular := strings.TrimSuffix(category, "s")
	fmt.Fprintln(stdout)
	selectedNames, err := promptSelectFromList(stdout, stdin, singular, "", names)
	if err != nil || selectedNames == nil {
		return err
	}

	var toRemove []configMatch
	for _, name := range selectedNames {
		toRemove = append(toRemove, configMatch{Name: name, Category: singular})
	}

	if dryRun {
		if !quiet {
			fmt.Fprintln(stdout, "\nDry run - no changes applied:")
		}
		return errors.Join(removeResolvedItems(nil, stdout, stderr, toRemove, local, quiet, defaultAgent, true)...)
	}

	if !skipConfirm {
		confirmed, err := confirmConfigRemoval(stdout, stdin, toRemove, local)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	return errors.Join(removeResolvedItems(nil, stdout, stderr, toRemove, local, quiet, defaultAgent, false)...)
}

// Returns false (without error) when the user declines.
func confirmConfigRemoval(w io.Writer, r io.Reader, items []configMatch, local bool) (bool, error) {
	scope := scopeString(local)
	if len(items) == 1 {
		m := items[0]
		fmt.Fprintf(w, "Remove %s %q from %s config? %s ", m.Category, m.Name, scope, tui.Bracket("y/N"))
	} else {
		fmt.Fprintf(w, "Remove the following items from %s config?\n", scope)
		for _, m := range items {
			fmt.Fprintf(w, "  - %s %s\n", m.Category, m.Name)
		}
		fmt.Fprintf(w, "%s ", tui.Bracket("y/N"))
	}

	reader := bufio.NewReader(r)
	input, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("reading input: %w", err)
	}
	input = strings.TrimSpace(strings.ToLower(input))
	if input != "y" && input != "yes" {
		fmt.Fprintln(w, "Cancelled.")
		return false, nil
	}
	return true, nil
}

// removeConfigItem is the per-category removal dispatch seam: config-merge
// categories drop a CUE entry; skills drop dest directories and the inventory.
func removeConfigItem(cmd *cobra.Command, m configMatch, local bool) error {
	switch m.Category {
	case "agent", "role", "context", "task":
		return removeConfigEntry(m.Category, m.Name, local)
	case "skill":
		return uninstallSkill(io.Discard, cmd, m.Name, local, true)
	}
	return fmt.Errorf("unknown category %q", m.Category)
}
