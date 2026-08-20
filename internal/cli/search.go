package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

// searchSection groups search results under a labelled section.
type searchSection struct {
	Label         string                 `json:"label"`
	Path          string                 `json:"path,omitempty"`
	Results       []modules.SearchResult `json:"results"`
	ShowInstalled bool                   `json:"-"` // Only true for registry section; display-only
}

// addSearchCommand adds the top-level search command.
func addSearchCommand(parent *cobra.Command) {
	searchCmd := &cobra.Command{
		Use:     "search [query]...",
		Aliases: []string{"find"},
		GroupID: "modules",
		Short:   "Search configs and registry for modules",
		Long: `Search local config, global config, and the module registry by keyword.

Searches module names, descriptions, and tags. Multiple words are combined
with AND logic - all terms must match. Terms can be space-separated or
comma-separated. Total query must be at least 3 characters.
Terms support regex patterns (e.g. '^home', 'expert$', 'go.*review').
Results are grouped by source (local, global, registry) and category.

Prefix a query with a category to scope the search to it
(e.g. 'roles:golang', 'contexts:cwd'). The category prefix is excluded from the
3-character minimum. An unknown category is an error.

Use --tag to filter by tags. Tags can be used alone or combined with a query.`,
		Args: cobra.MinimumNArgs(0),
		RunE: runSearch,
	}
	searchCmd.Flags().StringSlice("tag", nil, "Filter by tags (comma-separated)")
	searchCmd.Flags().Bool("json", false, "Output as JSON")

	parent.AddCommand(searchCmd)
}

// runSearch searches local config, global config, and the registry.
func runSearch(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	query := strings.Join(args, " ")
	jsonFlag, _ := cmd.Flags().GetBool("json")

	tagFlags, _ := cmd.Flags().GetStringSlice("tag")
	tags := modules.ParseSearchTerms(strings.Join(tagFlags, ","))

	// Split off any "category:" prefix and validate the 3-character floor against
	// the name only — the prefix scopes the search and is excluded from the count,
	// matching the resolution engine's floor rule. An unknown category fails fast
	// here as a usage fault rather than surfacing later from the search call. The
	// full query (prefix included) is still passed to the search functions, which
	// re-split internally to apply the scope.
	_, name, err := modules.SplitCategoryQuery(query)
	if err != nil {
		return err
	}
	terms := modules.ParseSearchPatterns(name)
	if err := modules.ValidateSearchQuery(terms, tags); err != nil {
		if jsonFlag {
			return usageError(err)
		}
		w := cmd.OutOrStdout()
		stdin := cmd.InOrStdin()
		if !isTerminal(stdin) {
			return usageError(err)
		}
		if query != "" {
			fmt.Fprintln(w, "Query must be at least 3 characters")
		}
		input, promptErr := promptSearchQuery(w, stdin, 3)
		if promptErr != nil {
			return promptErr
		}
		if input == "" {
			return nil
		}
		query = input
		_, name, err = modules.SplitCategoryQuery(query)
		if err != nil {
			return err
		}
		terms = modules.ParseSearchPatterns(name)
	}

	if len(terms) > 0 {
		if _, err := modules.CompileSearchTerms(terms); err != nil {
			return usageError(err)
		}
	}

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}

	loader := internalcue.NewLoader()
	stderr := cmd.ErrOrStderr()

	// Installed sources are gathered per scope so a per-scope load failure warns
	// and is skipped without losing the others; the resolved index (or nil, on a
	// registry outage) is the registry source. All three feed one gathering
	// primitive; the regex/tag matcher and the per-scope bucketing are layered on
	// top of the single candidate set.
	var installedSources []modules.InstalledSource
	if paths.LocalExists {
		cfg, err := loader.LoadSingle(paths.Local)
		if err != nil && !errors.Is(err, internalcue.ErrNoCUEFiles) {
			printWarning(stderr, "failed to load local config: %s", err)
		} else if err == nil {
			installedSources = append(installedSources, modules.InstalledSource{Config: cfg, Scope: config.ScopeLocal})
		}
	}
	if paths.GlobalExists {
		cfg, err := loader.LoadSingle(paths.Global)
		if err != nil && !errors.Is(err, internalcue.ErrNoCUEFiles) {
			printWarning(stderr, "failed to load global config: %s", err)
		} else if err == nil {
			installedSources = append(installedSources, modules.InstalledSource{Config: cfg, Scope: config.ScopeGlobal})
		}
	}

	// Resolve the registry index; degrade gracefully if unavailable. Cache-gated
	// under the shared rule: a fresh cache resolves the index version offline
	// (unless --refresh), so a follow-up FetchIndex of that canonical version
	// makes no metadata request. A nil index means no registry candidates.
	var registryErr error
	var index *registry.Index
	ctx := context.Background()
	flags := getFlags(cmd)
	client, err := getProvider(cmd)()
	if err != nil {
		registryErr = err
	} else if version, vErr := resolveDisplayIndexVersion(ctx, client, resolveLibraryIndexPath(), stderr, flags); vErr != nil {
		registryErr = vErr
	} else if idx, _, fErr := client.FetchIndex(ctx, version); fErr != nil {
		registryErr = fErr
	} else {
		index = idx
	}

	cands := modules.GatherCandidates(categoryKeys(describeCategories), installedSources, index)
	matched, err := modules.MatchSearch(cands, query, tags)
	if err != nil {
		return err
	}

	sections := buildSearchSections(matched, paths)

	displayQuery := query
	if displayQuery == "" && len(tags) > 0 {
		displayQuery = "--tag " + strings.Join(tags, ",")
	}

	// Nothing matched while the registry was unreachable: the empty result is
	// not authoritative, so fail with the transient code so an agent retries.
	// --json returns the raw error (stdout stays empty); text mode prints the
	// no-matches line and outage warning but silences the error so main.go adds
	// no duplicate "Error:" line.
	if len(sections) == 0 && registryErr != nil {
		if jsonFlag {
			return registryErr
		}
		fmt.Fprintf(cmd.OutOrStdout(), "No matches found for %q\n", displayQuery)
		printWarning(cmd.ErrOrStderr(), "registry unavailable: %v", registryErr)
		return silenced(registryErr)
	}

	if jsonFlag {
		if sections == nil {
			sections = []searchSection{}
		}
		if err := writeJSON(cmd.OutOrStdout(), sections); err != nil {
			return fmt.Errorf("marshalling search results: %w", err)
		}
		// Local matches but registry down: succeed (exit 0) yet warn on stderr
		// so a --json consumer can tell the set is incomplete without breaking
		// the stdout-JSON contract.
		if registryErr != nil {
			printWarning(cmd.ErrOrStderr(), "registry unavailable: %v", registryErr)
		}
		return nil
	}

	if len(sections) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No matches found for %q\n", displayQuery)
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout())
	installed := collectInstalledNames()
	printSearchSections(cmd.OutOrStdout(), sections, flags.Verbose, installed)

	if registryErr != nil {
		fmt.Fprintln(cmd.OutOrStdout())
		printWarning(cmd.ErrOrStderr(), "registry unavailable: %v", registryErr)
	}

	return nil
}

// buildSearchSections buckets matched candidates into the local, global, and
// registry sections in display order, projecting each bucket to SearchResult.
// Empty buckets are dropped, preserving the documented section shape. The source
// and scope tags drive the bucketing; the registry section carries ShowInstalled
// so installed registry rows are marked.
func buildSearchSections(matched []modules.Candidate, paths config.Paths) []searchSection {
	var local, global, reg []modules.Candidate
	for _, c := range matched {
		switch {
		case c.Source == modules.SourceRegistry:
			reg = append(reg, c)
		case c.Scope == config.ScopeLocal:
			local = append(local, c)
		case c.Scope == config.ScopeGlobal:
			global = append(global, c)
		}
	}

	var sections []searchSection
	if len(local) > 0 {
		sections = append(sections, searchSection{Label: "local", Path: "./.start", Results: modules.ResultsFromCandidates(local)})
	}
	if len(global) > 0 {
		sections = append(sections, searchSection{Label: "global", Path: shortenHome(paths.Global), Results: modules.ResultsFromCandidates(global)})
	}
	if len(reg) > 0 {
		sections = append(sections, searchSection{Label: "registry", Results: modules.ResultsFromCandidates(reg), ShowInstalled: true})
	}
	return sections
}

// printSearchSections prints search results grouped by section and category.
func printSearchSections(w io.Writer, sections []searchSection, verbose bool, installed map[string]bool) {
	for i, section := range sections {
		if len(section.Results) == 0 {
			continue
		}

		if i > 0 {
			fmt.Fprintln(w)
		}
		if section.Path != "" {
			fmt.Fprintf(w, "%s %s\n", section.Label, tui.Annotate("%s", section.Path))
		} else {
			fmt.Fprintln(w, section.Label)
		}

		grouped := make(map[string][]modules.SearchResult)
		for _, r := range section.Results {
			grouped[r.Category] = append(grouped[r.Category], r)
		}

		firstCat := true
		for _, cat := range categoryKeys(describeCategories) {
			catResults := grouped[cat]
			if len(catResults) == 0 {
				continue
			}

			if !firstCat {
				fmt.Fprintln(w)
			}
			firstCat = false

			fmt.Fprint(w, "  ")
			tui.CategoryColor(cat).Fprint(w, cat)
			fmt.Fprintln(w, ":")

			for _, r := range catResults {
				marker := "  "
				if section.ShowInstalled && installed[r.Category+"/"+r.Name] {
					marker = tui.ColorInstalled.Sprint("★") + " "
				}

				fmt.Fprintf(w, "    %s%-25s %s\n", marker, r.Name, tui.ColorDim.Sprint(r.Entry.Description))
				if verbose {
					if r.Entry.Module != "" {
						fmt.Fprintf(w, "      Module: %s\n", tui.ColorDim.Sprint(r.Entry.Module))
					}
					if len(r.Entry.Tags) > 0 {
						fmt.Fprintf(w, "      Tags: %s\n", tui.ColorDim.Sprint(strings.Join(r.Entry.Tags, ", ")))
					}
				}
			}
		}
	}
}

// collectInstalledNames returns a set of "category/name" keys for installed
// modules. Config-merge categories come from collectInstalledModules (the list
// inventory). Skills are not a list category; their keys are read from the
// skills inventory so library and search can mark them installed.
func collectInstalledNames() map[string]bool {
	paths, err := config.ResolvePaths("")
	if err != nil || !paths.AnyExists() {
		return nil
	}

	dirs := paths.ForScope(config.ScopeMerged)
	loader := internalcue.NewLoader()
	cfg, err := loader.Load(dirs)
	if err != nil {
		return nil
	}

	var localCfg cue.Value
	if paths.LocalExists {
		if v, loadErr := loader.LoadSingle(paths.Local); loadErr == nil {
			localCfg = v
		}
	}

	installedModules := collectInstalledModules(cfg.Value, paths, localCfg)
	names := make(map[string]bool, len(installedModules))
	for _, a := range installedModules {
		names[a.Category+"/"+a.Name] = true
	}
	for name := range inventoryFromValue(cfg.Value) {
		names["skills/"+name] = true
	}
	return names
}

// shortenHome replaces the home directory prefix with ~.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
