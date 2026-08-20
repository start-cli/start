package cli

import (
	"github.com/spf13/cobra"
)

func addUninstallCommand(parent *cobra.Command) {
	uninstallCmd := &cobra.Command{
		Use:     "uninstall [query]...",
		Aliases: []string{"remove", "rm"},
		GroupID: "modules",
		Short:   "Remove installed modules",
		Long: `Remove one or more installed modules from your configuration, the inverse of install.

Resolves each query against installed modules only — the registry is never
consulted. A bare query matches by substring across all library categories; a
"category:name" query matches by prefix within that category; a whole-name exact
match resolves ahead of both. If multiple modules match, prompts for selection.

Multiple queries can be provided to remove several modules at once.

By default, removes from global config (~/.config/start/).
Use --local to remove from project config (./.start/).

A confirmation prompt is shown before removal; use --force to skip it.`,
		Args: cobra.MinimumNArgs(0),
		RunE: runUninstall,
	}

	uninstallCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	parent.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	flags := getFlags(cmd)
	force, _ := cmd.Flags().GetBool("force")

	if len(args) == 0 {
		query, err := promptSearchQuery(cmd.OutOrStdout(), cmd.InOrStdin(), 1)
		if err != nil {
			return err
		}
		if query == "" {
			return nil
		}
		args = []string{query}
	}

	return runRemoval(cmd, args, flags.Local, force)
}
