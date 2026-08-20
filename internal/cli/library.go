package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"cuelang.org/go/mod/modconfig"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

func addLibraryCommand(parent *cobra.Command) {
	libraryCmd := &cobra.Command{
		Use:     "library [category]",
		Aliases: []string{"lib"},
		GroupID: "modules",
		Short:   "Show the available module library",
		Long: `Display the full module library from the CUE Central Registry.

Shows all available modules grouped by type (agents, roles, contexts, tasks, skills).
Installed modules are marked with ★.

Optionally filter by category: agents, roles, contexts, tasks, or skills.
Category filtering is supported with --json but not with --export.

Use --json to output machine-readable JSON, or --export to display the
raw CUE source files from the library module.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runLibrary,
	}

	libraryCmd.Flags().Bool("json", false, "Output library as JSON")
	libraryCmd.Flags().Bool("export", false, "Output raw CUE source files")

	parent.AddCommand(libraryCmd)
}

func runLibrary(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	// Validate category arg before any network I/O.
	var category string
	if len(args) > 0 {
		singular := normalizeCategoryArg(args[0])
		if singular == "" {
			return usageError(fmt.Errorf("unknown category %q: expected agents, roles, contexts, tasks, or skills", args[0]))
		}
		category = singular + "s"
	}

	ctx := context.Background()
	flags := getFlags(cmd)
	jsonFlag, _ := cmd.Flags().GetBool("json")
	exportFlag, _ := cmd.Flags().GetBool("export")

	client, err := getProvider(cmd)()
	if err != nil {
		return fmt.Errorf("creating registry client: %w", err)
	}

	prog := tui.NewProgress(cmd.ErrOrStderr(), flags.Quiet)
	defer prog.Done()

	prog.Update("Fetching index...")
	// Cache-gated under the shared rule: a fresh cache skips the metadata resolve
	// (unless --refresh); the helper writes the cache on a live resolve.
	resolvedPath, err := resolveDisplayIndexVersion(ctx, client, resolveLibraryIndexPath(), cmd.ErrOrStderr(), flags)
	if err != nil {
		return fmt.Errorf("resolving index version: %w", err)
	}

	version := modules.VersionFromOrigin(resolvedPath)
	if version == "" {
		version = resolvedPath
	}

	result, err := client.Fetch(ctx, resolvedPath)
	if err != nil {
		return fmt.Errorf("fetching index module: %w", err)
	}
	prog.Done()

	w := cmd.OutOrStdout()

	switch {
	case exportFlag:
		if category != "" {
			return usageError(fmt.Errorf("category filter cannot be used with --export"))
		}
		return printExportIndex(w, result.SourceDir)
	case jsonFlag:
		return printJSONIndex(w, result.SourceDir, client.Registry(), category)
	default:
		index, err := registry.LoadIndex(result.SourceDir, client.Registry())
		if err != nil {
			return fmt.Errorf("loading index: %w", err)
		}
		installed := collectInstalledNames()
		printIndex(w, index, version, flags.Verbose, installed, category)
		return nil
	}
}

func printExportIndex(w io.Writer, sourceDir string) error {
	return printCueFiles(w, sourceDir)
}

// printJSONIndex loads the index and outputs it as formatted JSON, restricted
// to category when non-empty.
func printJSONIndex(w io.Writer, sourceDir string, reg modconfig.Registry, category string) error {
	index, err := registry.LoadIndex(sourceDir, reg)
	if err != nil {
		return fmt.Errorf("loading index: %w", err)
	}

	if category != "" {
		index = filterIndexByCategory(index, category)
	}

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling index: %w", err)
	}

	fmt.Fprintln(w, string(data))
	return nil
}

func filterIndexByCategory(index *registry.Index, category string) *registry.Index {
	switch category {
	case "agents":
		return &registry.Index{Agents: index.Agents}
	case "roles":
		return &registry.Index{Roles: index.Roles}
	case "contexts":
		return &registry.Index{Contexts: index.Contexts}
	case "tasks":
		return &registry.Index{Tasks: index.Tasks}
	case "skills":
		return &registry.Index{Skills: index.Skills}
	default:
		return index
	}
}

// printIndex prints the index grouped by category (filtered to category when
// non-empty), but the header total always reflects the full index.
func printIndex(w io.Writer, index *registry.Index, version string, verbose bool, installed map[string]bool, category string) {
	total := len(index.Agents) + len(index.Roles) + len(index.Contexts) + len(index.Tasks) + len(index.Skills)
	fmt.Fprintf(w, "\nIndex: %s (%d modules)\n\n", version, total)

	categories := []struct {
		name    string
		entries map[string]registry.IndexEntry
	}{
		{"agents", index.Agents},
		{"roles", index.Roles},
		{"contexts", index.Contexts},
		{"tasks", index.Tasks},
		{"skills", index.Skills},
	}

	for _, cat := range categories {
		if len(cat.entries) == 0 {
			continue
		}
		if category != "" && cat.name != category {
			continue
		}

		names := make([]string, 0, len(cat.entries))
		for name := range cat.entries {
			names = append(names, name)
		}
		sort.Strings(names)

		tui.CategoryColor(cat.name).Fprint(w, cat.name)
		fmt.Fprintf(w, ": %s\n", tui.Annotate("%d", len(cat.entries)))

		for _, name := range names {
			entry := cat.entries[name]
			marker := "  "
			if installed[cat.name+"/"+name] {
				marker = tui.ColorInstalled.Sprint("★") + " "
			}

			fmt.Fprintf(w, "  %s%-25s %s\n", marker, name, tui.ColorDim.Sprint(entry.Description))

			if verbose {
				fmt.Fprintf(w, "      Module: %s\n", tui.ColorDim.Sprint(entry.Module))
				if len(entry.Tags) > 0 {
					fmt.Fprintf(w, "      Tags: %s\n", tui.ColorDim.Sprint(strings.Join(entry.Tags, ", ")))
				}
			}
		}
		fmt.Fprintln(w)
	}
}
