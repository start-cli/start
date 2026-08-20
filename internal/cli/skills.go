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

	sourceDir, origin, version, err := fetchSkillModule(ctx, client, selected)
	if err != nil {
		return err
	}

	var dests []string
	for _, root := range roots {
		dest, err := skills.DestDir(root.Root, selected.Name)
		if err != nil {
			return err
		}
		if err := skills.Materialise(sourceDir, dest); err != nil {
			return err
		}
		dests = append(dests, dest)
	}

	if err := skills.Upsert(configDir, selected.Name, origin, version); err != nil {
		return fmt.Errorf("recording skill inventory: %w", err)
	}

	if !flags.Quiet {
		addr := formatAddress(selected.Category, selected.Name)
		if version != "" {
			fmt.Fprintf(w, "\nInstalled %s@%s to %s config\n", addr, version, scopeName)
		} else {
			fmt.Fprintf(w, "\nInstalled %s to %s config\n", addr, scopeName)
		}
		for _, dest := range dests {
			fmt.Fprintf(w, "  %s\n", dest)
		}
		fmt.Fprintf(w, "Inventory: %s\n", skills.InventoryPath(configDir))
	}
	return nil
}

func uninstallSkill(stdout io.Writer, cmd *cobra.Command, name string, local, quiet bool) error {
	ctx := context.Background()
	cat, err := openSkillCatalog(cmd)
	if err != nil {
		return err
	}
	roots, err := cat.UninstallRoots(ctx, local)
	if err != nil {
		return err
	}

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}
	configDir := paths.Dir(local)

	for _, root := range roots {
		dest, err := skills.DestDir(root.Root, name)
		if err != nil {
			return err
		}
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

func launchAgentName(flags *Flags) (string, error) {
	switch len(flags.Agent) {
	case 0:
		return "", nil
	case 1:
		return flags.Agent[0], nil
	default:
		return "", usageError(fmt.Errorf("launch accepts at most one --agent"))
	}
}
