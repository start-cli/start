package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/skills"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// The post-fetch logic here overlaps with install.go, but the repetition is
// kept inline because each call site bakes command-specific UX into the same
// shape; extracting a helper would hide the per-command messages or require
// callbacks. The shared client + fetch + cache-write sequence lives in fetchIndex.

// UpdateResult tracks the result of an update operation.
type UpdateResult struct {
	Module       InstalledModule `json:"module"`
	OldVersion   string          `json:"oldVersion,omitempty"`
	NewVersion   string          `json:"newVersion,omitempty"`
	Updated      bool            `json:"updated"`
	Error        error           `json:"-"`
	ErrorMessage string          `json:"error,omitempty"`
}

// addUpdateCommand adds the update command to the root command.
func addUpdateCommand(parent *cobra.Command) {
	updateCmd := &cobra.Command{
		Use:     "update [query]",
		Aliases: []string{"upgrade"},
		GroupID: "modules",
		Short:   "Update installed modules",
		Long: `Update installed modules to their latest versions.

Without arguments, updates all installed modules including skills.
With a query, updates only matching modules.

The whole query skill or skills selects every installed skill.
A skills:<name> address selects that inventory key (not every skill).

Use --dry-run to preview what would be updated without applying changes.
Use --force to re-fetch and update modules even when already at the latest version.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runUpdate,
	}

	updateCmd.Flags().Bool("force", false, "Re-fetch even if already at latest version")
	updateCmd.Flags().Bool("json", false, "Output as JSON")

	parent.AddCommand(updateCmd)
}

// runUpdate updates installed modules.
func runUpdate(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	query := ""
	if len(args) > 0 {
		query = args[0]
	}

	jsonFlag, _ := cmd.Flags().GetBool("json")
	ctx := context.Background()

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}

	if !paths.AnyExists() {
		if jsonFlag {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No configuration found. Run 'start' to set up.")
		return nil
	}

	dirs := paths.ForScope(config.ScopeMerged)
	loader := internalcue.NewLoader()
	cfg, err := loader.Load(dirs)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	// Local config is loaded separately for scope detection.
	var localCfg cue.Value
	if paths.LocalExists {
		if v, loadErr := loader.LoadSingle(paths.Local); loadErr == nil {
			localCfg = v
		}
	}

	installed := collectInstalledModules(cfg.Value, paths, localCfg)
	skillMods, err := collectSkillModules(paths)
	if err != nil {
		return err
	}

	installed, err = selectUpdateTargets(cmd, query, installed, skillMods)
	if err != nil {
		return err
	}

	if len(installed) == 0 {
		if jsonFlag {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
			return nil
		}
		if query != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "No installed modules matching %q\n", query)
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No modules installed from registry.")
		return nil
	}

	flags := getFlags(cmd)
	prog := tui.NewProgress(cmd.ErrOrStderr(), flags.Quiet)
	defer prog.Done()

	index, client, err := fetchIndex(ctx, cmd, prog, "Checking for updates...")
	if err != nil {
		return err
	}
	prog.Done()

	dryRun := getFlags(cmd).DryRun
	force, _ := cmd.Flags().GetBool("force")
	skillEnv := &skillUpdateEnv{cmd: cmd}
	total := len(installed)
	var results []UpdateResult
	for i, mod := range installed {
		prog.Update("Updating %d/%d %s...", i+1, total, formatAddress(mod.Category, mod.Name))
		var result UpdateResult
		if mod.Category == "skills" {
			result = checkAndUpdateSkill(ctx, client, skillEnv, index, mod, dryRun, force)
		} else {
			result = checkAndUpdate(ctx, client, paths, index, mod, dryRun, force)
		}
		results = append(results, result)
	}
	prog.Done()

	if jsonFlag {
		// Populate ErrorMessage for JSON serialisation.
		for i := range results {
			if results[i].Error != nil {
				results[i].ErrorMessage = results[i].Error.Error()
			}
		}
		if err := writeJSON(cmd.OutOrStdout(), results); err != nil {
			return fmt.Errorf("marshalling update results: %w", err)
		}
		return nil
	}

	printUpdateResults(cmd.OutOrStdout(), results, dryRun)

	return nil
}

// checkAndUpdate checks for updates and optionally applies them.
func checkAndUpdate(ctx context.Context, client registry.Client, paths config.Paths, index *registry.Index, mod InstalledModule, dryRun, force bool) UpdateResult {
	result := UpdateResult{Module: mod}

	entry := findInIndex(index, mod.Category, mod.Name)
	if entry == nil {
		return result
	}

	result.OldVersion = mod.InstalledVer
	result.NewVersion = entry.Version

	needsUpdate := force || (entry.Version != "" && (mod.InstalledVer == "" || semver.Compare(entry.Version, mod.InstalledVer) > 0))

	if !needsUpdate {
		return result
	}

	if dryRun {
		result.Updated = true
		return result
	}

	if mod.ConfigFile == "" {
		result.Error = fmt.Errorf("no config file path for module")
		return result
	}

	modulePath := entry.Module
	if !strings.Contains(modulePath, "@") {
		modulePath += "@v0"
	}

	resolvedPath, err := client.ResolveLatestVersion(ctx, modulePath)
	if err != nil {
		result.Error = err
		return result
	}

	fetchResult, err := client.Fetch(ctx, resolvedPath)
	if err != nil {
		result.Error = err
		return result
	}

	searchResult := modules.SearchResult{
		Category: mod.Category,
		Name:     mod.Name,
		Entry:    *entry,
	}

	var roleName string
	if mod.Category == "tasks" && index != nil {
		configDir := filepath.Dir(mod.ConfigFile)
		roleName, err = modules.InstallRoleDependency(ctx, client, index, fetchResult.SourceDir, configDir)
		if err != nil {
			result.Error = fmt.Errorf("installing role dependency: %w", err)
			return result
		}
	}

	moduleContent, err := modules.ExtractModuleContent(fetchResult.SourceDir, searchResult, client.Registry(), resolvedPath, roleName)
	if err != nil {
		result.Error = fmt.Errorf("extracting module content: %w", err)
		return result
	}

	if err := modules.UpdateModuleInConfig(mod.ConfigFile, mod.Category, mod.Name, moduleContent); err != nil {
		result.Error = fmt.Errorf("updating config: %w", err)
		return result
	}

	result.Updated = true
	return result
}

func selectUpdateTargets(cmd *cobra.Command, query string, installed, skillMods []InstalledModule) ([]InstalledModule, error) {
	if query == "" {
		out := append(append([]InstalledModule{}, installed...), skillMods...)
		sortInstalledModules(out)
		return out, nil
	}
	if isSkillsCategoryWord(query) {
		out := append([]InstalledModule{}, skillMods...)
		sortInstalledModules(out)
		return out, nil
	}
	if remainder, ok := skillsAddressRemainder(query); ok {
		return selectSkillsByAddress(cmd, remainder, skillMods)
	}

	queryLower := strings.ToLower(query)
	var filtered []InstalledModule
	for _, a := range installed {
		if modules.NameMatches(query, a.Name, modules.ModeSubstring) ||
			strings.Contains(strings.ToLower(a.Category), queryLower) {
			filtered = append(filtered, a)
		}
	}
	for _, a := range skillMods {
		if modules.NameMatches(query, a.Name, modules.ModeSubstring) {
			filtered = append(filtered, a)
		}
	}
	sortInstalledModules(filtered)
	return filtered, nil
}

func selectSkillsByAddress(cmd *cobra.Command, remainder string, skillMods []InstalledModule) ([]InstalledModule, error) {
	entries := make(map[string]skills.Entry, len(skillMods))
	for _, m := range skillMods {
		entries[m.Name] = skills.Entry{}
	}
	keys := skills.ResolveKey(entries, remainder)
	switch len(keys) {
	case 0:
		return nil, notFoundError(fmt.Errorf("skill %q not found", remainder))
	case 1:
		return skillModsNamed(skillMods, keys[0]), nil
	}

	matches := make([]ModuleMatch, len(keys))
	for i, k := range keys {
		matches[i] = ModuleMatch{Name: k, Category: "skills", Source: ModuleSourceInstalled}
	}
	sel := &selector{
		stdin:  cmd.InOrStdin(),
		stdout: cmd.OutOrStdout(),
		stderr: cmd.ErrOrStderr(),
		flags:  getFlags(cmd),
	}
	scope := resolveScope{
		categories:    []describeCategory{*describeCategoryFor("skills")},
		crossCategory: true,
		displayType:   "skill",
	}
	picked, err := sel.selectMatch(matches, scope, remainder)
	if err != nil {
		return nil, err
	}
	return skillModsNamed(skillMods, picked.Name), nil
}

func skillModsNamed(skillMods []InstalledModule, name string) []InstalledModule {
	var out []InstalledModule
	for _, m := range skillMods {
		if strings.EqualFold(m.Name, name) {
			out = append(out, m)
		}
	}
	return out
}

type skillUpdateEnv struct {
	cmd    *cobra.Command
	once   bool
	global []skills.Dest
	local  []skills.Dest
	err    error
}

func (e *skillUpdateEnv) roots(local bool) ([]skills.Dest, error) {
	if !e.once {
		e.once = true
		e.global, e.local, e.err = scanSkillUninstallRoots(e.cmd)
	}
	if e.err != nil {
		return nil, e.err
	}
	if local {
		return e.local, nil
	}
	return e.global, nil
}

func checkAndUpdateSkill(ctx context.Context, client registry.Client, env *skillUpdateEnv, index *registry.Index, mod InstalledModule, dryRun, force bool) UpdateResult {
	result := UpdateResult{Module: mod}
	entry := findInIndex(index, mod.Category, mod.Name)
	if entry == nil {
		return result
	}

	result.OldVersion = mod.InstalledVer
	result.NewVersion = entry.Version
	needsUpdate := force || (entry.Version != "" && (mod.InstalledVer == "" || semver.Compare(entry.Version, mod.InstalledVer) > 0))
	if !needsUpdate {
		return result
	}

	roots, err := env.roots(mod.Scope == "local")
	if err != nil {
		result.Error = err
		return result
	}
	present, err := skills.PresentDests(roots, mod.Name)
	if err != nil {
		result.Error = err
		return result
	}
	if len(present) == 0 {
		result.Error = fmt.Errorf("skill %q has no SKILL.md under uninstall dests; run '%s' to rematerialise", mod.Name, skills.InstallCommand(mod.Name, mod.Scope == "local"))
		return result
	}
	if dryRun {
		result.Updated = true
		return result
	}

	sourceDir, origin, version, err := fetchSkillModule(ctx, client, modules.SearchResult{
		Category: mod.Category,
		Name:     mod.Name,
		Entry:    *entry,
	})
	if err != nil {
		result.Error = err
		return result
	}
	for _, dest := range present {
		if err := skills.Materialise(sourceDir, dest); err != nil {
			result.Error = fmt.Errorf("rematerialising %s: %w", dest, err)
			return result
		}
	}
	if err := skills.Upsert(filepath.Dir(mod.ConfigFile), mod.Name, origin, version); err != nil {
		result.Error = fmt.Errorf("updating skill inventory: %w", err)
		return result
	}
	result.Updated = true
	return result
}

// printUpdateResults prints the results of the update operation.
func printUpdateResults(w io.Writer, results []UpdateResult, dryRun bool) {
	if dryRun {
		fmt.Fprintln(w, "\nDry run - no changes applied:")
	} else {
		fmt.Fprintln(w)
	}

	var updated, current, failed int

	for _, r := range results {
		name := formatAddress(r.Module.Category, r.Module.Name)
		fmt.Fprintf(w, "  %s ", name)

		if r.Error != nil {
			tui.ColorCyan.Fprint(w, "(")
			tui.ColorError.Fprintf(w, "error: %v", r.Error)
			tui.ColorCyan.Fprintln(w, ")")
			failed++
		} else if r.Updated {
			tui.ColorCyan.Fprint(w, "(")
			if r.OldVersion != "" {
				tui.ColorDim.Fprint(w, r.OldVersion)
				fmt.Fprint(w, " ")
			}
			tui.ColorBlue.Fprint(w, "->")
			if r.NewVersion != "" {
				fmt.Fprint(w, " ")
				tui.ColorSuccess.Fprint(w, r.NewVersion)
			}
			tui.ColorCyan.Fprintln(w, ")")
			updated++
		} else {
			tui.ColorCyan.Fprint(w, "(")
			if r.OldVersion != "" {
				tui.ColorDim.Fprint(w, r.OldVersion)
				fmt.Fprint(w, " ")
				tui.ColorBlue.Fprint(w, "->")
				fmt.Fprint(w, " ")
			}
			tui.ColorDim.Fprint(w, "current")
			tui.ColorCyan.Fprintln(w, ")")
			current++
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Updated: %d, Current: %d", updated, current)
	if failed > 0 {
		fmt.Fprintf(w, ", Failed: %d", failed)
	}
	fmt.Fprintln(w)
}
