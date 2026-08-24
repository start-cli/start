package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/p3bot/agentdex"
	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/skills"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

type skillCatalogOptsKey struct{}
type resolverSourceKey struct{}

// WithSkillCatalogOpts injects agentdex options (fixture catalog, lookPath)
// so skill install/describe/uninstall tests stay offline.
func WithSkillCatalogOpts(ctx context.Context, opts ...agentdex.Option) context.Context {
	return context.WithValue(ctx, skillCatalogOptsKey{}, opts)
}

func skillCatalogOpts(cmd *cobra.Command) []agentdex.Option {
	if opts, ok := cmd.Context().Value(skillCatalogOptsKey{}).([]agentdex.Option); ok {
		return opts
	}
	return nil
}

// withResolverSource injects an index source so get/describe command tests
// can resolve registry skills without the production registry.
func withResolverSource(ctx context.Context, src indexSource) context.Context {
	return context.WithValue(ctx, resolverSourceKey{}, src)
}

func attachResolverSource(r *resolver, cmd *cobra.Command) {
	if src, ok := cmd.Context().Value(resolverSourceKey{}).(indexSource); ok {
		r.indexSrc = src
	}
}

func openSkillCatalog(cmd *cobra.Command) (*skills.Catalog, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolving working directory: %w", err)
	}
	return skills.Open(wd, skillCatalogOpts(cmd)...)
}

func fetchSkillModule(ctx context.Context, client registry.Client, selected modules.SearchResult) (sourceDir, origin, version string, err error) {
	if client == nil {
		return "", "", "", fmt.Errorf("registry client unavailable")
	}
	modulePath := selected.Entry.Module
	if modulePath == "" {
		return "", "", "", fmt.Errorf("skill %q has no module path", selected.Name)
	}
	if !strings.Contains(modulePath, "@") {
		modulePath += "@v0"
	}
	resolvedPath, err := client.ResolveLatestVersion(ctx, modulePath)
	if err != nil {
		return "", "", "", fmt.Errorf("resolving skill version: %w", err)
	}
	result, err := client.Fetch(ctx, resolvedPath)
	if err != nil {
		return "", "", "", fmt.Errorf("fetching skill module: %w", err)
	}
	return result.SourceDir, resolvedPath, modules.VersionFromOrigin(resolvedPath), nil
}

func installSkill(ctx context.Context, w io.Writer, cmd *cobra.Command, client registry.Client, selected modules.SearchResult, configDir, scopeName string, flags *Flags) error {
	cat, err := openSkillCatalog(cmd)
	if err != nil {
		return err
	}
	roots, err := cat.ResolveRoots(ctx, flags.Agent, flags.Local)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return fmt.Errorf("no skill install destinations resolved")
	}

	inventory, err := skills.Load(configDir)
	if err != nil {
		return fmt.Errorf("reading skill inventory: %w", err)
	}
	keys := make([]string, 0, len(inventory))
	for k := range inventory {
		keys = append(keys, k)
	}
	if conflict := skills.ConflictingKeys(keys, selected.Name); len(conflict) > 0 {
		return fmt.Errorf("dest leaf %q already used by %s", skills.Leaf(selected.Name), strings.Join(conflict, ", "))
	}

	dests, err := skillDests(roots, selected.Name)
	if err != nil {
		return err
	}

	if flags.DryRun {
		if !flags.Quiet {
			printSkillInstall(w, selected, selected.Entry.Version, scopeName, configDir, dests, true)
		}
		return nil
	}

	sourceDir, origin, version, err := fetchSkillModule(ctx, client, selected)
	if err != nil {
		return err
	}

	for _, dest := range dests {
		if err := skills.Materialise(sourceDir, dest); err != nil {
			return err
		}
	}

	if err := skills.Upsert(configDir, selected.Name, origin, version); err != nil {
		return fmt.Errorf("recording skill inventory: %w", err)
	}

	if !flags.Quiet {
		printSkillInstall(w, selected, version, scopeName, configDir, dests, false)
	}
	return nil
}

func skillDests(roots []skills.Dest, name string) ([]string, error) {
	dests := make([]string, 0, len(roots))
	for _, root := range roots {
		dest, err := skills.DestDir(root.Root, name)
		if err != nil {
			return nil, err
		}
		dests = append(dests, dest)
	}
	return dests, nil
}

func printSkillInstall(w io.Writer, selected modules.SearchResult, version, scopeName, configDir string, dests []string, dryRun bool) {
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
	for _, dest := range dests {
		fmt.Fprintf(w, "  %s\n", dest)
	}
	fmt.Fprintf(w, "Inventory: %s\n", skills.InventoryPath(configDir))
}

// matchSkillLeaf resolves a skills: query by exact inventory key, then dest
// leaf, after the engine's prefix rule reports no match. Installed keys are
// tried first so a unique installed leaf still resolves when the registry is
// down. An empty inventory falls through to the registry index when provided.
func matchSkillLeaf(sel *selector, inventory map[string]skills.Entry, index *registry.Index, input string, scope resolveScope) (ModuleMatch, error) {
	addr, err := parseAddress(input)
	if err != nil {
		return ModuleMatch{}, err
	}
	if !addr.HasPrefix || addr.Category != "skills" {
		return ModuleMatch{}, notFoundError(fmt.Errorf("module %q not found", input))
	}
	keys := skills.ResolveKey(inventory, addr.Name)
	source := ModuleSourceInstalled
	if len(keys) == 0 {
		keys = skills.ResolveKey(indexSkillEntries(index), addr.Name)
		source = ModuleSourceRegistry
	}
	switch len(keys) {
	case 0:
		return ModuleMatch{}, notFoundError(fmt.Errorf("skill %q not found", addr.Name))
	case 1:
		return skillLeafModuleMatch(keys[0], source, inventory, index), nil
	default:
		matches := make([]ModuleMatch, len(keys))
		for i, k := range keys {
			matches[i] = skillLeafModuleMatch(k, source, inventory, index)
		}
		return sel.selectMatch(matches, scope, addr.Name)
	}
}

// skillLeafModuleMatch is the dest-leaf match for one resolved key. Origin and
// version come from the map that produced the key so get/describe can fetch
// without waiting for enrichSkillMatch to find an index row.
func skillLeafModuleMatch(key string, source modules.Source, inventory map[string]skills.Entry, index *registry.Index) ModuleMatch {
	m := ModuleMatch{Name: key, Category: "skills", Source: source}
	switch source {
	case ModuleSourceInstalled:
		if e, ok := inventory[key]; ok {
			m.Entry = registry.IndexEntry{Module: e.Origin, Version: e.Version}
		}
	case ModuleSourceRegistry:
		if index != nil {
			if e, ok := index.Skills[key]; ok {
				m.Entry = e
			}
		}
	}
	return m
}

func indexSkillEntries(index *registry.Index) map[string]skills.Entry {
	out := map[string]skills.Entry{}
	if index == nil {
		return out
	}
	for name, e := range index.Skills {
		out[name] = skills.Entry{Origin: e.Module, Version: e.Version}
	}
	return out
}

func skillUninstallPlan(cmd *cobra.Command, name string, local bool) (dests []string, configDir string, err error) {
	cat, err := openSkillCatalog(cmd)
	if err != nil {
		return nil, "", err
	}
	roots, err := cat.UninstallRoots(context.Background(), local)
	if err != nil {
		return nil, "", err
	}
	paths, err := config.ResolvePaths("")
	if err != nil {
		return nil, "", fmt.Errorf("resolving config paths: %w", err)
	}
	dests, err = skillDests(roots, name)
	if err != nil {
		return nil, "", err
	}
	return existingSkillDests(dests), paths.Dir(local), nil
}

func existingSkillDests(dests []string) []string {
	var out []string
	for _, dest := range dests {
		_, err := os.Stat(dest)
		if !os.IsNotExist(err) {
			out = append(out, dest)
		}
	}
	return out
}

func uninstallSkill(stdout io.Writer, cmd *cobra.Command, name string, local, quiet bool) error {
	dests, configDir, err := skillUninstallPlan(cmd, name, local)
	if err != nil {
		return err
	}

	for _, dest := range dests {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("removing %s: %w", dest, err)
		}
	}

	if err := skills.Remove(configDir, name); err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintf(stdout, "Removed skill %q\n", name)
	}
	return nil
}

// enrichSkillMatch overlays the registry index row onto a skill match so
// describe (and get fetch) see library description, tags, module, and version
// even when an inventory hit won the installed-over-registry merge. A missing
// index leaves the inventory origin in place so fetch can still use the module
// cache.
func enrichSkillMatch(r *resolver, match ModuleMatch) ModuleMatch {
	index, _, _ := r.ensureIndex()
	if index == nil {
		return match
	}
	for name, entry := range index.Skills {
		if !nameMatches(match.Name, name, modeExact) {
			continue
		}
		if entry.Module == "" {
			entry.Module = match.Entry.Module
		}
		if entry.Version == "" {
			entry.Version = match.Entry.Version
		}
		match.Entry = entry
		return match
	}
	return match
}

func getSkill(stdout, stderr io.Writer, flags *Flags, client registry.Client, match ModuleMatch) error {
	ctx := context.Background()
	sourceDir, _, _, err := fetchSkillModule(ctx, client, modules.SearchResult{
		Category: match.Category,
		Name:     match.Name,
		Entry:    match.Entry,
	})
	if err != nil {
		return err
	}
	path := skills.SkillFile(sourceDir)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading SKILL.md: %w", err)
	}
	body := ensureTrailingNewline(string(content))
	if flags.Verbose {
		fmt.Fprintf(stderr, "Skill: %s\n", match.Name)
	}
	if shouldStyleMarkdown(sourceFile, path) {
		return tui.RenderMarkdown(stdout, body, flags.MarkdownStyle())
	}
	fmt.Fprint(stdout, body)
	return nil
}

func describeSkill(w io.Writer, cmd *cobra.Command, flags *Flags, client registry.Client, match ModuleMatch) error {
	ctx := context.Background()
	sourceDir, origin, version, err := fetchSkillModule(ctx, client, modules.SearchResult{
		Category: match.Category,
		Name:     match.Name,
		Entry:    match.Entry,
	})
	if err != nil {
		return err
	}
	if origin == "" {
		origin = match.Entry.Module
	}
	if version == "" {
		version = match.Entry.Version
	}

	files, err := skills.MaterialisableFiles(sourceDir)
	if err != nil {
		return fmt.Errorf("listing skill files: %w", err)
	}

	cat, err := openSkillCatalog(cmd)
	if err != nil {
		return err
	}
	roots, err := cat.ListRoots(ctx, flags.Agent, flags.Local)
	if err != nil {
		return err
	}

	label := tui.ColorDim.Sprint
	tui.CategoryColor("skills").Fprint(w, "Skill")
	fmt.Fprintf(w, ": %s\n", match.Name)
	printSeparator(w)

	if match.Entry.Description != "" {
		fmt.Fprintf(w, "%s %s\n", label("Description:"), match.Entry.Description)
	}
	if len(match.Entry.Tags) > 0 {
		fmt.Fprintf(w, "%s %s\n", label("Tags:"), strings.Join(match.Entry.Tags, ", "))
	}
	if origin != "" {
		fmt.Fprintf(w, "%s %s\n", label("Origin:"), origin)
	}
	if version != "" {
		fmt.Fprintf(w, "%s %s\n", label("Version:"), version)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s\n", label("Files:"))
	if len(files) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		for _, f := range files {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s\n", label("Install targets:"))
	if len(roots) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		for _, root := range roots {
			dest, err := skills.DestDir(root.Root, match.Name)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "  %s\n", dest)
		}
	}
	return nil
}
