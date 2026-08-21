package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

var errNoModules = errors.New("no modules found")

// Post-fetch logic here overlaps update.go but is kept inline: each call site
// bakes command-specific UX into the same shape, so a shared helper would hide
// the per-command messages or need callbacks. The shared client+fetch+cache
// sequence is centralised in fetchIndex.

func addInstallCommand(parent *cobra.Command) {
	installCmd := &cobra.Command{
		Use:     "install [query]...",
		GroupID: "modules",
		Short:   "Install modules from registry",
		Long: `Install one or more modules from the CUE registry to your configuration.

Searches the registry index for matching modules. If multiple matches are found,
prompts for selection. Use a direct path (e.g., "golang/code-review") for exact match.
Prefix the query with a category (e.g., "roles:golang/code-review") to scope the
search to that category; an unknown category is an error.

A skills: prefix (or a bare skill name) materialises the skill into detected
agent dests, or into dests named by --agent (agentdex catalog ids, repeatable).
It records origin and version in skills.cue; it does not write a prompt-module
config file.

Multiple queries can be provided to install several modules at once.

By default, installs to global config (~/.config/start/).
Use --local to install to project config (./.start/).
Use --dry-run to preview without writing.`,
		Args: cobra.MinimumNArgs(0),
		RunE: runInstall,
	}

	parent.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	prompted := false
	if len(args) == 0 {
		query, err := promptSearchQuery(cmd.OutOrStdout(), cmd.InOrStdin(), 3)
		if err != nil {
			return err
		}
		if query == "" {
			return nil
		}
		args = []string{query}
		prompted = true
	}

	w := cmd.OutOrStdout()
	stdin := cmd.InOrStdin()
	var validated []string
	for _, q := range args {
		// Measure the 3-character floor against the name only; a "category:"
		// prefix scopes the install and is excluded from the count, consistent
		// with search and the resolution engine. An unknown category fails fast
		// as a usage fault before the index is fetched.
		_, name, err := modules.SplitCategoryQuery(q)
		if err != nil {
			return err
		}
		if len(name) < 3 {
			if !isTerminal(stdin) {
				return usageError(fmt.Errorf("query %q must be at least 3 characters", q))
			}
			fmt.Fprintf(w, "Query %q must be at least 3 characters\n", q)
			input, err := promptSearchQuery(w, stdin, 3)
			if err != nil {
				return err
			}
			if input == "" {
				continue
			}
			q = input
			prompted = true
		}
		validated = append(validated, q)
	}
	if len(validated) == 0 {
		return nil
	}
	args = validated

	ctx := context.Background()
	flags := getFlags(cmd)
	prog := tui.NewProgress(cmd.ErrOrStderr(), flags.Quiet)
	defer prog.Done()

	index, client, err := fetchIndex(ctx, cmd, prog, "Fetching index...")
	if err != nil {
		return err
	}
	prog.Done()

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}

	configDir := paths.Dir(flags.Local)
	scopeName := scopeString(flags.Local)

	// On error with no CUE files (fresh install), cfg is a zero-value
	// cue.Value, on which ModuleExists correctly returns false.
	loader := internalcue.NewLoader()
	cfg, err := loader.LoadSingle(configDir)
	if err != nil {
		if matches, _ := filepath.Glob(filepath.Join(configDir, "*.cue")); len(matches) > 0 {
			return fmt.Errorf("invalid config in %s:\n%s\nRun 'start doctor' to diagnose",
				configDir, internalcue.IdentifyBrokenFiles(matches))
		}
	}

	var dryRunNoted bool
	var errs []error
	for _, query := range args {
		if err := installModule(ctx, cmd, prog, client, index, query, configDir, scopeName, flags, cfg, &dryRunNoted); err != nil {
			if prompted && len(args) == 1 && errors.Is(err, errNoModules) {
				fmt.Fprintf(w, "No modules found matching %q\n", query)
				return nil
			}
			if IsSilentError(err) {
				errs = append(errs, err)
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", query, err))
			if len(args) > 1 {
				fmt.Fprintf(w, "Error installing %q: %v\n", query, err)
			}
		}
	}

	return errors.Join(errs...)
}

// installModule searches for, selects, and installs a single module.
func installModule(ctx context.Context, cmd *cobra.Command, prog *tui.Progress, client registry.Client, index *registry.Index, query, configDir, scopeName string, flags *Flags, cfg cue.Value, dryRunNoted *bool) error {
	w := cmd.OutOrStdout()

	// Install enumerates registry candidates through the shared primitive and
	// applies search's regex/tag matcher on top, so it keeps matching names,
	// descriptions, and tags while staying registry-first.
	cands := modules.GatherCandidates(categoryKeys(describeCategories), nil, index)
	matched, err := modules.MatchSearch(cands, query, nil)
	if err != nil {
		return err
	}
	results := modules.ResultsFromCandidates(matched)
	if len(results) == 0 {
		// notFoundError tags for exit 3 while errNoModules stays reachable via
		// errors.Is for runInstall's interactive empty-result message.
		return notFoundError(fmt.Errorf("%w matching %q", errNoModules, query))
	}

	var selections []modules.SearchResult
	if len(results) == 1 {
		selections = results
	} else {
		var err error
		selections, err = promptModuleSelection(w, cmd.InOrStdin(), results, cfg)
		if err != nil {
			return err
		}
		if len(selections) == 0 {
			return nil
		}
	}

	noteInstallDryRun(w, flags, dryRunNoted)

	var errs []error
	for _, selected := range selections {
		if selected.Category == "skills" {
			if err := installSkill(ctx, w, cmd, client, selected, configDir, scopeName, flags); err != nil {
				fmt.Fprintf(w, "Error installing %s: %v\n", formatAddress(selected.Category, selected.Name), err)
				errs = append(errs, silenced(err))
			}
			continue
		}
		if err := installSingleModule(ctx, w, prog, client, index, selected, configDir, scopeName, flags, cfg); err != nil {
			fmt.Fprintf(w, "Error installing %s: %v\n", formatAddress(selected.Category, selected.Name), err)
			errs = append(errs, silenced(err))
		}
	}

	return errors.Join(errs...)
}

func noteInstallDryRun(w io.Writer, flags *Flags, noted *bool) {
	if flags == nil || !flags.DryRun || flags.Quiet || noted == nil || *noted {
		return
	}
	fmt.Fprintln(w, "\nDry run - no changes applied:")
	*noted = true
}

// installSingleModule checks and installs a single selected module.
func installSingleModule(ctx context.Context, w io.Writer, prog *tui.Progress, client registry.Client, index *registry.Index, selected modules.SearchResult, configDir, scopeName string, flags *Flags, cfg cue.Value) error {
	if modules.ModuleExists(cfg, selected.Category, selected.Name) {
		origin := modules.GetInstalledOrigin(cfg, selected.Category, selected.Name)

		// Manually-added module (no origin) — warn and proceed with install
		if origin == "" {
			if !flags.Quiet {
				printWarning(w, "replacing manually-added %s with registry version",
					formatAddress(selected.Category, selected.Name))
			}
		} else {
			if !flags.Quiet {
				installedVer := modules.VersionFromOrigin(origin)
				latestVer := selected.Entry.Version
				outdated := latestVer != "" && installedVer != "" && semver.Compare(latestVer, installedVer) > 0

				if outdated {
					fmt.Fprint(w, "○ ")
				} else {
					tui.ColorSuccess.Fprint(w, "✓ ")
				}
				tui.ColorDim.Fprint(w, "Already installed: ")
				tui.CategoryColor(selected.Category).Fprint(w, selected.Category)
				fmt.Fprintf(w, ":%s ", selected.Name)
				tui.ColorCyan.Fprint(w, "(")
				if installedVer != "" {
					tui.ColorDim.Fprint(w, installedVer)
				}
				if outdated {
					fmt.Fprint(w, " ")
					tui.ColorBlue.Fprint(w, "->")
					fmt.Fprint(w, " ")
					tui.ColorWarning.Fprint(w, latestVer)
				} else {
					fmt.Fprint(w, " ")
					tui.ColorBlue.Fprint(w, "->")
					fmt.Fprint(w, " ")
					tui.ColorDim.Fprint(w, "current")
				}
				tui.ColorCyan.Fprintln(w, ")")
			}
			return nil
		}
	}

	configFile, ok := internalcue.ConfigFiles[selected.Category]
	if !ok {
		configFile = internalcue.ConfigFiles[internalcue.KeySettings]
	}

	if flags.DryRun {
		if !flags.Quiet {
			printModuleInstall(w, selected, selected.Entry.Version, scopeName, configDir, configFile, true)
		}
		return nil
	}

	prog.Update("Fetching module...")
	version, err := modules.InstallModule(ctx, client, index, selected, configDir)
	if err != nil {
		return err
	}
	prog.Done()

	if !flags.Quiet {
		printModuleInstall(w, selected, version, scopeName, configDir, configFile, false)
	}

	return nil
}

func printModuleInstall(w io.Writer, selected modules.SearchResult, version, scopeName, configDir, configFile string, dryRun bool) {
	verb := "Installed"
	if dryRun {
		verb = "Would install"
	}
	addr := formatAddress(selected.Category, selected.Name)
	if version != "" {
		fmt.Fprintf(w, "\n%s %s@%s to %s config\n", verb, addr, version, scopeName)
	} else {
		fmt.Fprintf(w, "\n%s %s to %s config\n", verb, addr, scopeName)
	}
	fmt.Fprintf(w, "Config: %s/%s\n", configDir, configFile)
}

// promptModuleSelection prompts to select one or more modules from multiple
// matches. Supports single numbers, CSV (1,3,5), ranges (1-3), "all", or name
// matching. Returns nil, nil if the user cancels (empty input).
func promptModuleSelection(w io.Writer, r io.Reader, results []modules.SearchResult, cfg cue.Value) ([]modules.SearchResult, error) {
	isTTY := isTerminal(r)

	if !isTTY {
		var names []string
		for _, res := range results {
			names = append(names, formatAddress(res.Category, res.Name))
		}
		return nil, usageError(fmt.Errorf(
			"multiple modules found: %s\nSpecify exact path or run interactively",
			strings.Join(names, ", "),
		))
	}

	fmt.Fprintf(w, "\nFound %d matches:\n\n", len(results))

	for i, res := range results {
		marker := "  "
		if modules.ModuleExists(cfg, res.Category, res.Name) {
			marker = tui.ColorInstalled.Sprint("★") + " "
		}
		fmt.Fprintf(w, "  %s%d. ", marker, i+1)
		tui.CategoryColor(res.Category).Fprint(w, res.Category)
		fmt.Fprintf(w, ":%s ", res.Name)
		tui.ColorDim.Fprintf(w, "- %s", res.Entry.Description)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "CSV %s, range %s, or \"all\" supported\n",
		tui.Annotate("1,2,3"), tui.Annotate("1-3"))
	fmt.Fprintf(w, "Select %s: ", tui.Annotate("1-%d", len(results)))

	reader := bufio.NewReader(r)
	input, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}

	input = strings.TrimSpace(input)

	if input == "" {
		fmt.Fprintln(w, "Cancelled.")
		return nil, nil
	}
	if strings.ToLower(input) == "all" {
		return results, nil
	}

	// Match by name before parsing as indices.
	inputLower := strings.ToLower(input)
	for _, res := range results {
		fullPath := formatAddress(res.Category, res.Name)
		if strings.ToLower(res.Name) == inputLower || strings.ToLower(fullPath) == inputLower {
			return []modules.SearchResult{res}, nil
		}
	}

	indices, err := parseSelectionInput(input, len(results))
	if err != nil {
		return nil, err
	}
	if len(indices) == 0 {
		fmt.Fprintln(w, "Cancelled.")
		return nil, nil
	}
	selected := make([]modules.SearchResult, len(indices))
	for i, idx := range indices {
		selected[i] = results[idx]
	}
	return selected, nil
}
