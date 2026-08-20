package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/fault"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/skills"
	"github.com/spf13/cobra"
)

// installedMatcher is the installed-only matchSource behind start uninstall and
// start config remove. It resolves against one scope's loaded config and never
// consults the registry or auto-installs, so removal shares the engine's
// exact→fallback reduction with install/get without inheriting their index
// machinery.
type installedMatcher struct {
	cfg    cue.Value
	stderr io.Writer
	flags  *Flags
}

func (m *installedMatcher) exactCandidates(name string, cats []describeCategory, _ resolveScope) []ModuleMatch {
	var out []ModuleMatch
	for _, cat := range cats {
		out = append(out, collectInstalledFrom(m.cfg, cat.category, name, modeExact)...)
	}
	return mergeMatches(out, nil)
}

func (m *installedMatcher) fallbackCandidates(name string, cats []describeCategory, mode matchMode, scope resolveScope) []ModuleMatch {
	var out []ModuleMatch
	for _, cat := range cats {
		out = append(out, collectInstalledFrom(m.cfg, cat.category, name, mode)...)
	}
	matches := mergeMatches(out, nil)
	debugf(m.stderr, m.flags, dbgResolve, "%s %q: %d installed matches", scope.displayType, name, len(matches))
	return matches
}

func (m *installedMatcher) finalize(mm ModuleMatch) (ModuleMatch, error) { return mm, nil }

// unreachableErr is always nil: removal confirms absence directly from installed
// config, so a zero match is a confirmed not-found, never a transient retry.
func (m *installedMatcher) unreachableErr() error { return nil }

// removalScope maps the --local flag to the single scope removal acts within.
// config.ScopeFromLocal is unsuitable: it returns ScopeMerged for the default
// case, but removal must resolve and write within exactly one scope.
func removalScope(local bool) config.Scope {
	if local {
		return config.ScopeLocal
	}
	return config.ScopeGlobal
}

// removalResolveScope is the resolveScope removal queries run under: all
// library categories, cross-category labelling (category:name in menus), and no
// locator acceptance — a file path or URL cannot name an installed module.
func removalResolveScope() resolveScope {
	return resolveScope{
		categories:    describeCategories,
		crossCategory: true,
		allowLocator:  false,
		displayType:   "module",
	}
}

// loadRemovalConfig loads the selected scope's config for matching and returns
// its configured default_agent. A scope with no config directory or no CUE files
// is not an error: it yields a zero value against which every query resolves to
// not-found.
func loadRemovalConfig(local bool) (cue.Value, string, error) {
	paths, err := config.ResolvePaths("")
	if err != nil {
		return cue.Value{}, "", fmt.Errorf("resolving config paths: %w", err)
	}

	exists := paths.GlobalExists
	if local {
		exists = paths.LocalExists
	}
	if !exists {
		return cue.Value{}, "", nil
	}

	cfg, err := internalcue.NewLoader().LoadSingle(paths.Dir(local))
	if err != nil {
		if errors.Is(err, internalcue.ErrNoCUEFiles) {
			return cue.Value{}, "", nil
		}
		return cue.Value{}, "", err
	}
	return cfg, getDefaultAgentFromConfig(cfg), nil
}

// matchSkillUninstall resolves a skills-prefixed leaf (skills:one-by-one) when
// the engine's prefix rule cannot see it. Called only after matchInstalled
// returns not-found. Exact group/name and a unique bare leaf already resolve
// through matchInstalled.
func matchSkillUninstall(sel *selector, cfg cue.Value, input string, scope resolveScope) (ModuleMatch, error) {
	addr, err := parseAddress(input)
	if err != nil {
		return ModuleMatch{}, err
	}
	if !addr.HasPrefix || addr.Category != "skills" {
		return ModuleMatch{}, notFoundError(fmt.Errorf("module %q not found", input))
	}
	entries := inventoryFromValue(cfg)
	keys := skills.ResolveKey(entries, addr.Name)
	switch len(keys) {
	case 0:
		return ModuleMatch{}, notFoundError(fmt.Errorf("skill %q not found", addr.Name))
	case 1:
		return ModuleMatch{Name: keys[0], Category: "skills", Source: ModuleSourceInstalled}, nil
	default:
		matches := make([]ModuleMatch, len(keys))
		for i, k := range keys {
			matches[i] = ModuleMatch{Name: k, Category: "skills", Source: ModuleSourceInstalled}
		}
		return sel.selectMatch(matches, scope, addr.Name)
	}
}

func inventoryFromValue(cfg cue.Value) map[string]skills.Entry {
	out := map[string]skills.Entry{}
	cat := cfg.LookupPath(cue.ParsePath(internalcue.KeySkills))
	if !cat.Exists() {
		return out
	}
	iter, err := cat.Fields()
	if err != nil {
		return out
	}
	for iter.Next() {
		var e skills.Entry
		if o := iter.Value().LookupPath(cue.ParsePath("origin")); o.Exists() {
			e.Origin, _ = o.String()
		}
		if v := iter.Value().LookupPath(cue.ParsePath("version")); v.Exists() {
			e.Version, _ = v.String()
		}
		out[iter.Selector().Unquoted()] = e
	}
	return out
}

// matchInstalled interprets a removal query and reduces it to a single installed
// module, mirroring resolver.resolve over the installed-only source: a locator
// is rejected, the registry is never consulted, and the fallback floor is zero
// so even a one- or two-character query resolves.
func matchInstalled(sel *selector, src matchSource, input string, scope resolveScope) (ModuleMatch, error) {
	interp, err := interpretSurface(input, scope)
	if err != nil {
		return ModuleMatch{}, err
	}
	switch interp.kind {
	case surfaceLocator:
		return ModuleMatch{}, usageError(fmt.Errorf("a file path or URL cannot name an installed module: %q", input))
	case surfaceSkip:
		return ModuleMatch{}, notFoundError(fmt.Errorf("module %q not found", input))
	}

	outcome, err := sel.match(src, interp.name, interp.cats, interp.mode, scope, 0)
	if err != nil {
		return ModuleMatch{}, err
	}
	return outcome.match, nil
}

// runRemoval is the shared core behind start uninstall and start config remove.
// It resolves each query against installed modules in the selected scope through
// the installed-only matcher, confirms the full set once (unless --force), and
// removes each via the removeConfigItem dispatch seam. Each query is resolved
// independently: a failure on one is reported without aborting the rest.
func runRemoval(cmd *cobra.Command, queries []string, local, force bool) error {
	flags := getFlags(cmd)
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	cfg, defaultAgent, err := loadRemovalConfig(local)
	if err != nil {
		return err
	}

	sel := &selector{stdin: stdin, stdout: stdout, stderr: stderr, flags: flags}
	src := &installedMatcher{cfg: cfg, stderr: stderr, flags: flags}
	scope := removalResolveScope()

	var items []configMatch
	var errs []error
	seen := make(map[string]bool)
	for _, q := range queries {
		match, err := matchInstalled(sel, src, q, scope)
		// skills:one-by-one is not a prefix of workflows/one-by-one, so the
		// engine reports not-found; ResolveKey then matches the unique leaf.
		// Any other engine error (ambiguous prefix, usage) must stand.
		if errors.Is(err, fault.ErrNotFound) {
			if addr, pErr := parseAddress(q); pErr == nil && addr.HasPrefix && addr.Category == "skills" {
				match, err = matchSkillUninstall(sel, src.cfg, q, scope)
			}
		}
		if err != nil {
			errs = append(errs, err)
			if len(queries) > 1 {
				fmt.Fprintf(stdout, "Error removing %q: %v\n", q, err)
			}
			continue
		}
		key := formatAddress(match.Category, match.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, configMatch{Name: match.Name, Category: strings.TrimSuffix(match.Category, "s")})
	}

	if len(items) == 0 {
		return errors.Join(errs...)
	}

	if !force {
		if !isTerminal(stdin) {
			return usageError(fmt.Errorf("--force flag required in non-interactive mode"))
		}
		confirmed, err := confirmConfigRemoval(stdout, stdin, items, local)
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.Join(errs...)
		}
	}

	errs = append(errs, removeResolvedItems(cmd, stdout, stderr, items, local, flags.Quiet, defaultAgent)...)
	return errors.Join(errs...)
}

// removeResolvedItems removes each already-resolved item through the
// removeConfigItem dispatch seam, warning when the configured default_agent is
// removed and reporting each outcome. A failure on one item is reported without
// aborting the rest; the per-item errors are returned for the caller to join.
// Both start uninstall / start config remove (via runRemoval) and the no-arg
// interactive config-remove path share this loop so they warn and report
// identically.
func removeResolvedItems(cmd *cobra.Command, stdout, stderr io.Writer, items []configMatch, local, quiet bool, defaultAgent string) []error {
	var errs []error
	for _, m := range items {
		if err := removeConfigItem(cmd, m, local); err != nil {
			errs = append(errs, fmt.Errorf("removing %s %q: %w", m.Category, m.Name, err))
			fmt.Fprintf(stdout, "Error removing %s %q: %v\n", m.Category, m.Name, err)
			continue
		}
		if m.Category == "agent" && m.Name == defaultAgent {
			printWarning(stderr, "removed agent %q is the configured default_agent; set settings.default_agent to another agent", m.Name)
		}
		if !quiet {
			fmt.Fprintf(stdout, "Removed %s %q\n", m.Category, m.Name)
		}
	}
	return errs
}

// removeConfigEntry deletes one module's config entry through the AST writer,
// preserving comments and pruning an emptied category. It maps the singular
// category label used by the removeConfigItem dispatch to the plural CUE key and
// config filename.
func removeConfigEntry(singularCategory, name string, local bool) error {
	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}
	key := singularCategory + "s"
	filename, ok := internalcue.ConfigFiles[key]
	if !ok {
		return fmt.Errorf("unknown category %q", singularCategory)
	}
	configPath := filepath.Join(paths.Dir(local), filename)
	return modules.RemoveModuleFromConfig(configPath, key, name)
}
