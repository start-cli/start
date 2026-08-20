package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"cuelang.org/go/cue"
	cueformat "cuelang.org/go/cue/format"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

// DescribeResult holds the result of preparing describe output.
type DescribeResult struct {
	ItemType string    // "Agent", "Role", "Context", "Task", "Skill"
	Category string    // "agents", "roles", "contexts", "tasks", "skills"
	CueKey   string    // Top-level CUE key (e.g., "agents")
	Name     string    // Item name
	Value    cue.Value // The CUE value for this item
	AllNames []string  // All available items of this type
}

// describeCategory maps category metadata used for cross-category operations.
type describeCategory struct {
	key      string // CUE key (e.g., "agents")
	category string // Category name (e.g., "agents")
	itemType string // Display type (e.g., "Agent")
}

var describeCategories = []describeCategory{
	{internalcue.KeyAgents, "agents", "Agent"},
	{internalcue.KeyRoles, "roles", "Role"},
	{internalcue.KeyContexts, "contexts", "Context"},
	{internalcue.KeyTasks, "tasks", "Task"},
	{internalcue.KeySkills, "skills", "Skill"},
}

// configMergeCategories is the four prompt-module categories written into
// category *.cue files. Skills are a library category but not a config-merge
// module; matchConfigByName and config add/edit/get stay on this list.
var configMergeCategories = []describeCategory{
	{internalcue.KeyAgents, "agents", "Agent"},
	{internalcue.KeyRoles, "roles", "Role"},
	{internalcue.KeyContexts, "contexts", "Context"},
	{internalcue.KeyTasks, "tasks", "Task"},
}

// describeCategoryFor looks up a describeCategory by its category string,
// returning nil when not found. Callers pass Category values drawn from
// describeCategories, so nil is unreachable in practice.
func describeCategoryFor(category string) *describeCategory {
	for i := range describeCategories {
		if describeCategories[i].category == category {
			return &describeCategories[i]
		}
	}
	return nil
}

// parsedAddress represents a user-facing module address. With a category:name
// prefix, Category is set and HasPrefix is true; otherwise Name holds the whole
// input and Category is empty.
type parsedAddress struct {
	Category  string
	Name      string
	HasPrefix bool
}

// parseAddress splits an address on the first colon. A left segment must be one
// of the known library categories or an error listing the valid set is returned;
// with no colon the whole input is the bare name.
func parseAddress(input string) (parsedAddress, error) {
	before, after, ok := strings.Cut(input, ":")
	if !ok {
		return parsedAddress{Name: input}, nil
	}
	cat := before
	name := after
	if describeCategoryFor(cat) == nil {
		// A malformed category prefix is invalid input — usage (exit 2),
		// uniform with the category-mismatch path and the list/library surfaces.
		return parsedAddress{}, usageError(fmt.Errorf("unknown category %q (valid: %s)", cat, knownCategoriesList()))
	}
	return parsedAddress{Category: cat, Name: name, HasPrefix: true}, nil
}

func knownCategoriesList() string {
	names := make([]string, len(describeCategories))
	for i, c := range describeCategories {
		names[i] = c.category
	}
	return strings.Join(names, ", ")
}

// formatAddress returns the canonical "category:name" address.
func formatAddress(category, name string) string {
	return category + ":" + name
}

func addDescribeCommand(parent *cobra.Command, flags *Flags) {
	describeCmd := &cobra.Command{
		Use:     "describe [name]",
		GroupID: "workflow",
		Short:   "Display resolved configuration content",
		Long: `Display resolved configuration content from merged global and local config.

Without arguments, lists all configured items with descriptions.
With an argument, searches across all categories and displays a verbose dump.

Names may be bare (e.g. "claude") or fully qualified as "category:name"
(e.g. "agents:claude/interactive"). The category prefix scopes the search
to a single category; bare names continue to search across all library
categories. A file path
(starting with ./, /, ~, or ~/) or an http(s) URL bypasses the search and its
content is read directly.

Use --global to restrict output to the global config (~/.config/start/) or
--local to restrict to the local config (./.start/). These flags are mutually
exclusive; omitting both shows the effective merged configuration.

Auto-installed modules always land in global config; the post-install lookup
widens to merged scope so a --local invocation can still see the new module.
To inspect strictly within --local, ensure the module is already installed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runDescribe,
	}

	describeCmd.Flags().BoolVar(&flags.Global, "global", false, "Restrict to global config only")
	parent.AddCommand(describeCmd)
}

func runDescribe(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	if err := validateScopeFlags(getFlags(cmd)); err != nil {
		return err
	}

	if len(args) == 0 {
		return runDescribeListing(cmd)
	}

	// The three-character floor lives in the resolver, exempting the exact tier;
	// the raw argument passes straight through.
	return runDescribeSearch(cmd, args[0])
}

func runDescribeListing(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()
	stdin := cmd.InOrStdin()

	scope := scopeFromFlags(getFlags(cmd))

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}
	fmt.Fprintln(w)
	printConfigPaths(w, paths)
	fmt.Fprintln(w)

	entries, err := config.ResolveAllSettings(paths, scope)
	if err != nil {
		return err
	}
	tui.ColorSettings.Fprint(w, "settings")
	fmt.Fprintln(w, ":")
	printSettingsEntries(w, entries)
	fmt.Fprintln(w)

	cfg, err := loadConfig(scope)
	if err != nil {
		return err
	}

	for _, cat := range describeCategories {
		if cat.category == "skills" {
			// Inventory is origin/version only, not prompt-module content.
			continue
		}
		items := cfg.Value.LookupPath(cue.ParsePath(cat.key))
		if !items.Exists() {
			continue
		}

		type entry struct {
			name string
			desc string
		}

		var catEntries []entry
		maxNameLen := 0

		iter, err := items.Fields()
		if err != nil {
			continue
		}
		for iter.Next() {
			name := iter.Selector().Unquoted()
			desc := ""
			if d := iter.Value().LookupPath(cue.ParsePath("description")); d.Exists() {
				desc, _ = d.String()
			}
			catEntries = append(catEntries, entry{name, desc})
			if len(name) > maxNameLen {
				maxNameLen = len(name)
			}
		}

		if len(catEntries) == 0 {
			continue
		}

		tui.CategoryColor(cat.category).Fprint(w, cat.category)
		fmt.Fprintln(w, ":")

		for _, e := range catEntries {
			if e.desc != "" {
				padding := strings.Repeat(" ", maxNameLen-len(e.name)+2)
				fmt.Fprintf(w, "  %s%s", e.name, padding)
				tui.ColorDim.Fprintln(w, e.desc)
			} else {
				fmt.Fprintf(w, "  %s\n", e.name)
			}
		}

		fmt.Fprintln(w)
	}

	if isTerminal(stdin) {
		query, err := promptSearchQuery(w, stdin, 3)
		if err != nil {
			return err
		}
		if query == "" {
			return nil
		}
		if err := runDescribeSearch(cmd, query); err != nil {
			fmt.Fprintln(w, capitalise(err.Error()))
			return nil
		}
		return nil
	}

	return nil
}

func runDescribeSearch(cmd *cobra.Command, name string) error {
	w := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	flags := getFlags(cmd)
	scope := scopeFromFlags(flags)
	stdin := cmd.InOrStdin()

	cfg, err := loadConfigOrEmpty(scope)
	if err != nil {
		return err
	}

	r := newResolver(cfg, flags, w, stderr, stdin)
	attachResolverSource(r, cmd)
	outcome, err := r.resolveCrossNoInstall(name)
	if err != nil {
		return err
	}
	// Leading gap before the describe body, emitted only once resolution
	// succeeds so a resolution error leaves no stray blank line on stdout.
	fmt.Fprintln(w)
	if outcome.locator != "" {
		return outputFileBody(w, flags, outcome.locator)
	}
	match := outcome.match

	if match.Category == "skills" {
		return describeSkill(w, cmd, flags, r.client, enrichSkillMatch(r, match))
	}

	if _, err := r.finalize(match); err != nil {
		return err
	}

	// autoInstall always writes to global config, so after an install merged
	// is the smallest scope guaranteed to see the new module. Widening is a
	// no-op under --global and required under --local (signalled to the user).
	effectiveScope := scope
	if r.didInstall {
		effectiveScope = config.ScopeMerged
		notifyScopeWidenedIfLocal(stderr, flags, r.didInstall)
	}

	cat := describeCategoryFor(match.Category)
	if cat == nil {
		return fmt.Errorf("unknown category %q", match.Category)
	}
	return describeVerboseItem(w, match.Name, effectiveScope, cat.key, cat.itemType, flags)
}

func describeVerboseItem(w io.Writer, name string, scope config.Scope, cueKey, itemType string, flags *Flags) error {
	result, err := prepareDescribe(name, scope, cueKey, itemType)
	if err != nil {
		return err
	}
	printVerboseDump(w, result, flags)
	return nil
}

func prepareDescribe(name string, scope config.Scope, cueKey, itemType string) (DescribeResult, error) {
	if name == "" {
		return DescribeResult{}, fmt.Errorf("name is required")
	}
	cfg, err := loadConfig(scope)
	if err != nil {
		return DescribeResult{}, err
	}

	typePlural := strings.ToLower(itemType) + "s"

	items := cfg.Value.LookupPath(cue.ParsePath(cueKey))
	if !items.Exists() {
		return DescribeResult{}, fmt.Errorf("no %s defined in configuration", typePlural)
	}

	var allNames []string
	iter, err := items.Fields()
	if err != nil {
		return DescribeResult{}, fmt.Errorf("reading %s: %w", typePlural, err)
	}
	for iter.Next() {
		allNames = append(allNames, iter.Selector().Unquoted())
	}
	if len(allNames) == 0 {
		return DescribeResult{}, fmt.Errorf("no %s defined in configuration", typePlural)
	}

	resolvedName := name
	item := items.LookupPath(cue.MakePath(cue.Str(name)))
	if !item.Exists() {
		var matches []string
		for _, n := range allNames {
			if strings.Contains(n, name) {
				matches = append(matches, n)
			}
		}

		switch len(matches) {
		case 0:
			return DescribeResult{}, notFoundError(fmt.Errorf("%s %q not found", strings.ToLower(itemType), name))
		case 1:
			resolvedName = matches[0]
			item = items.LookupPath(cue.MakePath(cue.Str(resolvedName)))
		default:
			return DescribeResult{}, fmt.Errorf("ambiguous %s name %q matches: %s", strings.ToLower(itemType), name, strings.Join(matches, ", "))
		}
	}

	return DescribeResult{
		ItemType: itemType,
		Category: typePlural,
		CueKey:   cueKey,
		Name:     resolvedName,
		Value:    item,
		AllNames: allNames,
	}, nil
}

// notifyScopeWidenedIfLocal emits a grep-able stderr notice when an auto-install
// silently widened --local resolution to merged scope (auto-installs land in
// global config, so the lookup then runs against merged). No-op without --local
// or under --quiet. --global gets no notice by design: the install matches the
// requested scope, so the widened lookup is a no-op in the common case.
func notifyScopeWidenedIfLocal(stderr io.Writer, flags *Flags, didInstall bool) {
	if !didInstall || !flags.Local || flags.Quiet {
		return
	}
	printWarning(stderr, "--local widened to merged scope after registry install")
}

// scopeFromFlags derives the config scope from --local/--global. validateScopeFlags
// rejects the both-set case first, so this treats --global as the winner.
func scopeFromFlags(flags *Flags) config.Scope {
	if flags.Global {
		return config.ScopeGlobal
	}
	if flags.Local {
		return config.ScopeLocal
	}
	return config.ScopeMerged
}

// validateScopeFlags rejects the --local/--global both-set case as a usage error
// (exit 2). Done explicitly because Cobra's MarkFlagsMutuallyExclusive produces
// an untyped error that bypasses FlagErrorFunc and would exit 1.
func validateScopeFlags(flags *Flags) error {
	if flags.Local && flags.Global {
		return usageError(fmt.Errorf("--local and --global are mutually exclusive"))
	}
	return nil
}

func loadConfig(scope config.Scope) (internalcue.LoadResult, error) {
	paths, err := config.ResolvePaths("")
	if err != nil {
		return internalcue.LoadResult{}, fmt.Errorf("resolving config paths: %w", err)
	}

	dirs := paths.ForScope(scope)

	if len(dirs) == 0 {
		switch scope {
		case config.ScopeGlobal:
			return internalcue.LoadResult{}, fmt.Errorf("no global configuration found at %s", paths.Global)
		case config.ScopeLocal:
			return internalcue.LoadResult{}, fmt.Errorf("no local configuration found at %s", paths.Local)
		default:
			return internalcue.LoadResult{}, fmt.Errorf("no configuration found (checked %s and %s)", paths.Global, paths.Local)
		}
	}

	loader := internalcue.NewLoader()
	result, err := loader.Load(dirs)
	if err != nil && errors.Is(err, internalcue.ErrNoCUEFiles) {
		switch scope {
		case config.ScopeGlobal:
			return result, fmt.Errorf("no global configuration found at %s (directory exists but contains no .cue files)", paths.Global)
		case config.ScopeLocal:
			return result, fmt.Errorf("no local configuration found at %s (directory exists but contains no .cue files)", paths.Local)
		default:
			return result, fmt.Errorf("no configuration found (checked %s and %s; directories exist but contain no .cue files)", paths.Global, paths.Local)
		}
	}
	return result, err
}

// loadConfigOrEmpty is loadConfig for fetch-and-emit surfaces (get, describe
// of a name): a missing directory or a directory with no CUE files is an empty
// value, so a registry-only skill can resolve. Invalid CUE still fails.
func loadConfigOrEmpty(scope config.Scope) (internalcue.LoadResult, error) {
	paths, err := config.ResolvePaths("")
	if err != nil {
		return internalcue.LoadResult{}, fmt.Errorf("resolving config paths: %w", err)
	}
	dirs := paths.ForScope(scope)
	if len(dirs) == 0 {
		return internalcue.LoadResult{}, nil
	}
	result, err := internalcue.NewLoader().Load(dirs)
	if err != nil && errors.Is(err, internalcue.ErrNoCUEFiles) {
		return internalcue.LoadResult{}, nil
	}
	return result, err
}

func printVerboseDump(w io.Writer, r DescribeResult, flags *Flags) {
	cat := r.Category
	label := tui.ColorDim.Sprint

	tui.CategoryColor(cat).Fprint(w, r.ItemType)
	fmt.Fprintf(w, ": %s\n", r.Name)
	printSeparator(w)

	configSource := findConfigSource(r.CueKey, r.Name)
	if configSource != "" {
		fmt.Fprintf(w, "%s %s %s\n",
			label("Config:"), configSource,
			tui.Annotate("%s", r.Name))
	}

	origin := orchestration.ExtractOrigin(r.Value)
	if origin != "" {
		fmt.Fprintf(w, "%s %s\n", label("Origin:"), origin)
		cacheDir := deriveCacheDir(origin)
		if cacheDir != "" {
			fmt.Fprintf(w, "%s %s\n", label("Cache:"), cacheDir)
		}
	}

	printMetadataBlock(w, r)

	cueDef := formatCUEDefinition(r.Value)
	if cueDef != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, cueDef)
	}

	fields := orchestration.ExtractUTDFields(r.Value)
	if fields.File != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s\n", label("File:"), fields.File)

		resolvedPath, content, readErr := resolveDescribeFile(fields.File, origin)
		if resolvedPath != "" && resolvedPath != fields.File {
			fmt.Fprintf(w, "%s %s\n", label("Path:"), resolvedPath)
		}

		if readErr != nil {
			fmt.Fprintf(w, "[error: %s]\n", readErr)
		} else if content != "" {
			fmt.Fprintln(w)
			// Only the file body is eligible for styling; every other section
			// stays literal. ensureTrailingNewline keeps the raw (non-decorated)
			// output identical to the prior write-then-newline behaviour.
			body := ensureTrailingNewline(content)
			if shouldStyleMarkdown(sourceFile, fields.File) {
				// Best-effort like the rest of the dump: a write error here is
				// not worth aborting the diagnostic. get propagates its render
				// error instead because its stdout is pipe-clean data.
				_ = tui.RenderMarkdown(w, body, flags.MarkdownStyle())
			} else {
				fmt.Fprint(w, body)
			}
		}
	}

	if fields.Command != "" {
		cmd := fields.Command
		if r.ItemType == "Agent" {
			cmd = partialFillAgentCommand(cmd, r.Value, "")
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s\n", label("Command:"), cmd)
	}

	printSeparator(w)
}

// printMetadataBlock writes a category-specific metadata block. For
// role/context/task, File and Command are zeroed first so they are not rendered
// here — printVerboseDump emits them separately via ExtractUTDFields.
func printMetadataBlock(w io.Writer, r DescribeResult) {
	switch r.ItemType {
	case "Agent":
		writeAgentMetadata(w, decodeAgentValue(r.Value))
	case "Role":
		role := decodeRoleValue(r.Value)
		role.File, role.Command = "", ""
		writeRoleMetadata(w, role)
	case "Context":
		ctx := decodeContextValue(r.Value)
		ctx.File, ctx.Command = "", ""
		writeContextMetadata(w, ctx)
	case "Task":
		task := decodeTaskValue(r.Value)
		task.File, task.Command = "", ""
		writeTaskMetadata(w, task)
	}
}

// findConfigSource determines which config file defines an item. It loads each
// dir separately via LoadSingle because merged CUE values lose per-file
// position information.
func findConfigSource(cueKey, name string) string {
	paths, err := config.ResolvePaths("")
	if err != nil {
		return ""
	}

	loader := internalcue.NewLoader()

	// Local first — it overrides global.
	if paths.LocalExists {
		if v, err := loader.LoadSingle(paths.Local); err == nil {
			item := v.LookupPath(cue.ParsePath(cueKey)).LookupPath(cue.MakePath(cue.Str(name)))
			if item.Exists() {
				if pos := item.Pos(); pos.IsValid() {
					return pos.Filename()
				}
			}
		}
	}

	if paths.GlobalExists {
		if v, err := loader.LoadSingle(paths.Global); err == nil {
			item := v.LookupPath(cue.ParsePath(cueKey)).LookupPath(cue.MakePath(cue.Str(name)))
			if item.Exists() {
				if pos := item.Pos(); pos.IsValid() {
					return pos.Filename()
				}
			}
		}
	}

	return ""
}

func formatCUEDefinition(v cue.Value) string {
	syn := v.Syntax(
		cue.Concrete(false),
		cue.Definitions(true),
		cue.Hidden(true),
		cue.Optional(true),
	)

	b, err := cueformat.Node(syn)
	if err != nil {
		return ""
	}
	return string(b)
}

func resolveDescribeFile(filePath, origin string) (resolvedPath, content string, err error) {
	if filePath == "" {
		return "", "", nil
	}

	if strings.HasPrefix(filePath, "@module/") {
		if origin == "" {
			return "", "", fmt.Errorf("@module/ path requires origin field: %s", filePath)
		}
		resolved, err := orchestration.ResolveModulePath(filePath, origin)
		if err != nil {
			return "", "", err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return resolved, "", err
		}
		return resolved, string(data), nil
	}

	expanded, err := orchestration.ExpandFilePath(filePath)
	if err != nil {
		return "", "", err
	}

	data, readErr := os.ReadFile(expanded)
	if readErr != nil {
		return expanded, "", readErr
	}
	return expanded, string(data), nil
}

// partialFillAgentCommand fills the static {{.bin}} and {{.model}} placeholders,
// leaving runtime ones ({{.prompt}}, {{.role}}, ...) for execution time.
// modelOverride (already resolved by the caller) replaces default_model when
// non-empty. Both paths look the key up in the models map; unknown keys pass
// through as the literal id.
func partialFillAgentCommand(command string, v cue.Value, modelOverride string) string {
	bin := ""
	if f := v.LookupPath(cue.ParsePath("bin")); f.Exists() {
		bin, _ = f.String()
	}

	model := modelOverride
	if model == "" {
		if dm := v.LookupPath(cue.ParsePath("default_model")); dm.Exists() {
			model, _ = dm.String()
		}
	}
	if model != "" {
		if id, ok := internalcue.AgentModels(v)[model]; ok {
			model = id
		}
	}

	result := command
	if bin != "" {
		result = strings.ReplaceAll(result, "{{.bin}}", bin)
	}
	if model != "" {
		result = strings.ReplaceAll(result, "{{.model}}", model)
	}
	return result
}

func deriveCacheDir(origin string) string {
	cacheDir, err := orchestration.GetCUECacheDir()
	if err != nil {
		return ""
	}

	idx := strings.LastIndex(origin, "@")
	if idx == -1 {
		return ""
	}

	modulePath := origin[:idx]
	version := origin[idx:]
	return filepath.Join(cacheDir, "mod", "extract",
		filepath.Dir(modulePath),
		filepath.Base(modulePath)+version)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}
