package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"

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

// The config-loading shape here is repeated in update.go but kept inline:
// each call site bakes command-specific empty-state UX into the same shape, so
// a shared helper would hide those messages or need callbacks. Update checking
// uses checkForUpdates rather than fetchIndex because the index is only
// fetched on the --verbose path.

const skillStatusMissing = "missing"

// InstalledModule represents an installed module with version info.
type InstalledModule struct {
	Category     string   `json:"category"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Models       []string `json:"models,omitempty"`
	InstalledVer string   `json:"version,omitempty"`
	LatestVer    string   `json:"latestVersion,omitempty"`
	UpdateAvail  bool     `json:"updateAvailable,omitempty"`
	Scope        string   `json:"scope"`
	Origin       string   `json:"origin"`
	ConfigFile   string   `json:"configFile"`
	Status       string   `json:"status,omitempty"`
}

func addListCommand(parent *cobra.Command) {
	listCmd := &cobra.Command{
		Use:     "list [category]",
		Aliases: []string{"ls"},
		GroupID: "modules",
		Short:   "List installed modules",
		Long: `List installed registry modules with update status.

Shows all modules installed via the registry with their current version
and whether updates are available.

Optionally filter by category: agents, roles, contexts, tasks, or skills.

Use --json to output machine-readable JSON.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runList,
	}

	listCmd.Flags().Bool("json", false, "Output as JSON")

	parent.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	// Validate category arg before any I/O.
	var category string
	if len(args) > 0 {
		singular := normalizeCategoryArg(args[0])
		if singular == "" {
			return usageError(fmt.Errorf("unknown category %q: expected agents, roles, contexts, tasks, or skills", args[0]))
		}
		category = singular + "s"
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

	// Load local config separately for scope detection.
	var localCfg cue.Value
	if paths.LocalExists {
		if v, loadErr := loader.LoadSingle(paths.Local); loadErr == nil {
			localCfg = v
		}
	}

	installed := collectInstalledModules(cfg.Value, paths, localCfg)
	if category == "" || category == "skills" {
		skillMods, err := collectSkillModules(paths)
		if err != nil {
			return err
		}
		if len(skillMods) > 0 {
			annotateSkillPresence(cmd, skillMods)
			installed = append(installed, skillMods...)
			sortInstalledModules(installed)
		}
	}

	if len(installed) == 0 {
		if jsonFlag {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No modules installed from registry.")
		return nil
	}

	if category != "" {
		var filtered []InstalledModule
		for _, a := range installed {
			if a.Category == category {
				filtered = append(filtered, a)
			}
		}
		installed = filtered

		if len(installed) == 0 {
			if jsonFlag {
				fmt.Fprintln(cmd.OutOrStdout(), "[]")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "No %s installed from registry.\n", category)
			return nil
		}
	}

	flags := getFlags(cmd)
	if flags.Verbose {
		client, err := getProvider(cmd)()
		if err == nil {
			prog := tui.NewProgress(cmd.ErrOrStderr(), flags.Quiet)
			prog.Update("Checking for updates...")
			checkForUpdates(ctx, client, installed, resolveLibraryIndexPath(), flags, cmd.ErrOrStderr())
			prog.Done()
		}
	}

	if jsonFlag {
		if err := writeJSON(cmd.OutOrStdout(), installed); err != nil {
			return fmt.Errorf("marshalling modules: %w", err)
		}
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout())
	printInstalledModules(cmd.OutOrStdout(), installed, flags.Verbose)

	return nil
}

func collectInstalledModules(v cue.Value, paths config.Paths, localCfg cue.Value) []InstalledModule {
	var installed []InstalledModule

	categories := []string{"agents", "roles", "contexts", "tasks"}
	for _, cat := range categories {
		catVal := v.LookupPath(cue.ParsePath(cat))
		if !catVal.Exists() {
			continue
		}

		iter, err := catVal.Fields()
		if err != nil {
			continue
		}

		for iter.Next() {
			name := iter.Selector().Unquoted()
			moduleVal := iter.Value()

			var origin string
			if originVal := moduleVal.LookupPath(cue.ParsePath("origin")); originVal.Exists() {
				origin, _ = originVal.String()
			}

			// Registry-installed modules only (origin is registry provenance).
			if origin == "" {
				continue
			}

			installedVer := modules.VersionFromOrigin(origin)

			var description string
			if descVal := moduleVal.LookupPath(cue.ParsePath("description")); descVal.Exists() {
				description, _ = descVal.String()
			}

			var tags []string
			if tagsVal := moduleVal.LookupPath(cue.ParsePath("tags")); tagsVal.Exists() {
				tagIter, tagErr := tagsVal.List()
				if tagErr == nil {
					for tagIter.Next() {
						if s, sErr := tagIter.Value().String(); sErr == nil {
							tags = append(tags, s)
						}
					}
				}
			}

			// Extract models (agents only)
			var models []string
			if cat == "agents" {
				if modelsVal := moduleVal.LookupPath(cue.ParsePath("models")); modelsVal.Exists() {
					modIter, modErr := modelsVal.List()
					if modErr == nil {
						for modIter.Next() {
							if s, sErr := modIter.Value().String(); sErr == nil {
								models = append(models, s)
							}
						}
					}
				}
			}

			scope, configFile := determineScopeAndFile(localCfg, paths, cat, name)
			module := InstalledModule{
				Category:     cat,
				Name:         name,
				Description:  description,
				Tags:         tags,
				Models:       models,
				InstalledVer: installedVer,
				Scope:        scope,
				Origin:       origin,
				ConfigFile:   configFile,
			}
			installed = append(installed, module)
		}
	}

	sortInstalledModules(installed)
	return installed
}

func sortInstalledModules(installed []InstalledModule) {
	sort.Slice(installed, func(i, j int) bool {
		if installed[i].Category != installed[j].Category {
			return modules.CategoryOrder(installed[i].Category) < modules.CategoryOrder(installed[j].Category)
		}
		if installed[i].Name != installed[j].Name {
			return installed[i].Name < installed[j].Name
		}
		return installed[i].Scope < installed[j].Scope
	})
}

// needsSkillDestScan reports whether doctor should look up uninstall dests.
// A load error still needs the lookup: CheckSkills loads each scope on its
// own, so a broken file in one scope must not skip dest health in the other.
func needsSkillDestScan(paths config.Paths) bool {
	mods, err := collectSkillModules(paths)
	return err != nil || len(mods) > 0
}

// collectSkillModules loads each scope's skills.cue independently. Dest health
// is not computed here; list annotates presence after a dest scan.
func collectSkillModules(paths config.Paths) ([]InstalledModule, error) {
	var installed []InstalledModule
	if paths.GlobalExists {
		mods, err := skillModulesFromDir(paths.Global, "global")
		if err != nil {
			return nil, err
		}
		installed = append(installed, mods...)
	}
	if paths.LocalExists {
		mods, err := skillModulesFromDir(paths.Local, "local")
		if err != nil {
			return nil, err
		}
		installed = append(installed, mods...)
	}
	return installed, nil
}

func skillModulesFromDir(dir, scope string) ([]InstalledModule, error) {
	entries, err := skills.Load(dir)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]InstalledModule, 0, len(keys))
	for _, k := range keys {
		e := entries[k]
		out = append(out, InstalledModule{
			Category:     "skills",
			Name:         k,
			InstalledVer: e.Version,
			Scope:        scope,
			Origin:       e.Origin,
			ConfigFile:   skills.InventoryPath(dir),
		})
	}
	return out, nil
}

func annotateSkillPresence(cmd *cobra.Command, mods []InstalledModule) {
	stderr := cmd.ErrOrStderr()
	globalRoots, localRoots, err := scanSkillUninstallRoots(cmd)
	if err != nil {
		printWarning(stderr, "agent catalog unavailable; dest scan skipped")
		return
	}
	var destErr error
	for i := range mods {
		roots := globalRoots
		if mods[i].Scope == "local" {
			roots = localRoots
		}
		present, err := skills.PresentDests(roots, mods[i].Name)
		if err != nil {
			destErr = err
			continue
		}
		if len(present) == 0 {
			mods[i].Status = skillStatusMissing
		}
	}
	if destErr != nil {
		printWarning(stderr, "could not check some skill dests: %v", destErr)
	}
}

func scanSkillUninstallRoots(cmd *cobra.Command) (global, local []skills.Dest, err error) {
	cat, err := openSkillCatalog(cmd)
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	global, err = cat.UninstallRoots(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	local, err = cat.UninstallRoots(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	return global, local, nil
}

// determineScopeAndFile reports whether a module is global or local and the
// path to its config file.
func determineScopeAndFile(localCfg cue.Value, paths config.Paths, category, name string) (scope, configFile string) {
	configFileName, ok := internalcue.ConfigFiles[category]
	if !ok {
		configFileName = internalcue.ConfigFiles[internalcue.KeySettings]
	}

	// Local takes precedence over global.
	if paths.LocalExists && modules.ModuleExists(localCfg, category, name) {
		return "local", filepath.Join(paths.Local, configFileName)
	}

	// Fall back to global; this path is for informational display only.
	return "global", filepath.Join(paths.Global, configFileName)
}

// checkForUpdates fills in LatestVer/UpdateAvail by comparing against the
// registry index. Cache-gated under the shared rule: a fresh cache resolves the
// index version offline (unless --refresh) so FetchIndex of that canonical
// version makes no metadata request.
func checkForUpdates(ctx context.Context, client registry.Client, installed []InstalledModule, indexPath string, flags *Flags, stderr io.Writer) {
	version, err := resolveDisplayIndexVersion(ctx, client, indexPath, stderr, flags)
	if err != nil {
		return
	}
	index, _, err := client.FetchIndex(ctx, version)
	if err != nil {
		return
	}

	for i := range installed {
		entry := findInIndex(index, installed[i].Category, installed[i].Name)
		if entry != nil && entry.Version != "" {
			installed[i].LatestVer = entry.Version
			installed[i].UpdateAvail = semver.Compare(entry.Version, installed[i].InstalledVer) > 0
		}
	}
}

func findInIndex(index *registry.Index, category, name string) *registry.IndexEntry {
	var entries map[string]registry.IndexEntry

	switch category {
	case "agents":
		entries = index.Agents
	case "roles":
		entries = index.Roles
	case "contexts":
		entries = index.Contexts
	case "tasks":
		entries = index.Tasks
	case "skills":
		entries = index.Skills
	}

	if entry, ok := entries[name]; ok {
		return &entry
	}
	return nil
}

func printInstalledModules(w io.Writer, installed []InstalledModule, verbose bool) {
	fmt.Fprintln(w, "Installed modules:")
	fmt.Fprintln(w)

	grouped := make(map[string][]InstalledModule)
	for _, a := range installed {
		grouped[a.Category] = append(grouped[a.Category], a)
	}

	categories := []string{"agents", "roles", "contexts", "tasks", "skills"}
	for _, cat := range categories {
		modules := grouped[cat]
		if len(modules) == 0 {
			continue
		}

		tui.CategoryColor(cat).Fprint(w, cat)
		fmt.Fprintln(w, ":")
		for _, a := range modules {
			if verbose && a.LatestVer != "" {
				fmt.Fprintf(w, "  %-25s ", a.Name)
				if a.UpdateAvail {
					fmt.Fprint(w, tui.Annotate("update available: %s", a.LatestVer))
				} else {
					fmt.Fprint(w, tui.Annotate("latest"))
				}
				if a.Category == "skills" {
					fmt.Fprintf(w, " [%s]", a.Scope)
				}
				printSkillMissingAnnotation(w, a)
				fmt.Fprintln(w)
			} else {
				scopeIndicator := ""
				if verbose || a.Category == "skills" {
					scopeIndicator = fmt.Sprintf(" [%s]", a.Scope)
				}
				if a.InstalledVer != "" {
					fmt.Fprintf(w, "  %-25s %s%s", a.Name, tui.Annotate("%s", a.InstalledVer), scopeIndicator)
				} else {
					fmt.Fprintf(w, "  %s%s", a.Name, scopeIndicator)
				}
				printSkillMissingAnnotation(w, a)
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintln(w)
	}
}

func printSkillMissingAnnotation(w io.Writer, a InstalledModule) {
	if a.Status != skillStatusMissing {
		return
	}
	fmt.Fprint(w, " ")
	tui.ColorCyan.Fprint(w, "(")
	tui.ColorWarning.Fprint(w, "missing")
	tui.ColorCyan.Fprint(w, ")")
}
