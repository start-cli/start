package cli

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/tui"
)

// matchMode and its constants are the resolution layer's names for the shared
// literal name-only match modes in internal/modules. The exact tier always uses
// modeExact; the fallback tier uses modeSubstring for a bare term and modePrefix
// for a category-qualified one. Matching is literal (no regex) and
// case-insensitive throughout.
type matchMode = modules.MatchMode

const (
	modeExact     = modules.ModeExact
	modeSubstring = modules.ModeSubstring
	modePrefix    = modules.ModePrefix
)

// nameMatches reports whether candidate matches query under mode, deferring to
// the shared matcher so every name-only surface compares names identically.
func nameMatches(query, candidate string, mode matchMode) bool {
	return modules.NameMatches(query, candidate, mode)
}

// resolveScope parameterises the unified resolver for one surface: which
// categories it spans, whether it is a cross-category surface (display as
// category:name and consult the registry in the exact tier to detect twins),
// whether a locator (local path or http(s) URL) is accepted, and the noun used
// in messages.
type resolveScope struct {
	categories    []describeCategory
	crossCategory bool
	allowLocator  bool
	displayType   string
}

// resolveOutcome is the result of resolution: either a locator (a local path or
// http(s) URL, read directly with no search) or a resolved module match.
type resolveOutcome struct {
	locator string
	match   ModuleMatch
}

// singleCategoryScope builds a scope for a category-specific surface
// (--role, --agent, start task, --context).
func singleCategoryScope(category, displayType string, allowLocator bool) resolveScope {
	return resolveScope{
		categories:   []describeCategory{*describeCategoryFor(category)},
		allowLocator: allowLocator,
		displayType:  displayType,
	}
}

// crossCategoryScope builds the scope for the cross-category surfaces
// (start get, start describe), spanning all library categories.
func crossCategoryScope() resolveScope {
	return resolveScope{
		categories:    describeCategories,
		crossCategory: true,
		allowLocator:  true,
		displayType:   "module",
	}
}

// surfaceKind classifies how a surface identifier is handled before any lookup.
type surfaceKind int

const (
	surfaceName    surfaceKind = iota // a module name to look up (cats/mode/name set)
	surfaceLocator                    // a local path or http(s) URL, read directly
	surfaceSkip                       // empty or a none/default sentinel — no index touch
)

// surfaceInterpretation is the result of classifying a surface identifier: the
// shared front-half of resolution that both resolve() and the up-front liveness
// union consume, so the two cannot drift on what an identifier means.
type surfaceInterpretation struct {
	kind    surfaceKind
	locator string             // when kind == surfaceLocator
	cats    []describeCategory // when kind == surfaceName: scoped categories
	mode    matchMode          // when kind == surfaceName: fallback match mode
	name    string             // when kind == surfaceName: the bare name
}

// interpretSurface classifies an identifier exactly as resolve() does before it
// looks anything up: an empty value or a context none/default sentinel is a skip
// (the surface never touches the index), a path or http(s) URL is a locator
// (read directly), and anything else is a name whose category prefix scopes the
// lookup and selects prefix vs substring fallback. A malformed category prefix
// returns a usage error.
//
// This is the single source of the locator/prefix/sentinel interpretation. Both
// resolve()'s own dispatch and computeWantLive call it, so a future change to
// what an identifier means updates both at once.
func interpretSurface(input string, scope resolveScope) (surfaceInterpretation, error) {
	// Empty is the universal "use the default" signal. none/default are context
	// sentinels (role's none is normalised to empty upstream); they apply only to
	// the category-specific contexts surface, never to a cross-category get/
	// describe query where "default" or "none" could name a real module.
	if input == "" {
		return surfaceInterpretation{kind: surfaceSkip}, nil
	}
	if scope.displayType == "context" && (isNoneToken(input) || input == "default") {
		return surfaceInterpretation{kind: surfaceSkip}, nil
	}
	if orchestration.IsLocator(input) {
		return surfaceInterpretation{kind: surfaceLocator, locator: input}, nil
	}

	addr, err := parseAddress(input)
	if err != nil {
		return surfaceInterpretation{}, err
	}

	cats := scope.categories
	mode := modeSubstring
	if addr.HasPrefix {
		mode = modePrefix
		if scope.crossCategory {
			// parseAddress already validated the category, so the lookup is non-nil.
			cats = []describeCategory{*describeCategoryFor(addr.Category)}
		} else if addr.Category != scope.categories[0].category {
			return surfaceInterpretation{}, usageError(fmt.Errorf("%s expects category %q, got %q in %q",
				scope.displayType, scope.categories[0].category, addr.Category, input))
		}
	}
	return surfaceInterpretation{kind: surfaceName, cats: cats, mode: mode, name: addr.Name}, nil
}

// resolve turns an identifier into an outcome through the unified match rule:
// locator bypass, category-prefix interpretation, then the shared exact→fallback
// matcher over installed-plus-registry candidates, installing a chosen
// registry-only match. The fallback floor is three characters.
func (r *resolver) resolve(input string, scope resolveScope) (resolveOutcome, error) {
	return r.resolveWith(input, scope, r)
}

// noInstallSource reuses the resolver's match rule but skips finalize, so a
// registry-only skill is not auto-installed through InstallModule.
type noInstallSource struct{ matchSource }

func (s noInstallSource) finalize(m ModuleMatch) (ModuleMatch, error) { return m, nil }

// resolveNoInstall is resolve without installIfRegistry. Get and describe of a
// skill use this so they fetch-and-emit without writing skills.cue or dests.
func (r *resolver) resolveNoInstall(input string, scope resolveScope) (resolveOutcome, error) {
	return r.resolveWith(input, scope, noInstallSource{r})
}

func (r *resolver) resolveWith(input string, scope resolveScope, src matchSource) (resolveOutcome, error) {
	interp, err := interpretSurface(input, scope)
	if err != nil {
		return resolveOutcome{}, err
	}
	switch interp.kind {
	case surfaceLocator:
		if !scope.allowLocator {
			return resolveOutcome{}, usageError(fmt.Errorf("%s does not accept a file path or URL: %q", scope.displayType, input))
		}
		debugf(r.stderr, r.flags, dbgResolve, "%s %q: locator bypass", scope.displayType, input)
		return resolveOutcome{locator: input}, nil
	case surfaceSkip:
		// A sentinel/empty reaching resolve() is a caller bug: resolveSingle and
		// resolveContexts filter these before dispatching. Treat defensively as
		// not-found rather than silently mis-resolving.
		return resolveOutcome{}, notFoundError(fmt.Errorf("%s %q not found", scope.displayType, input))
	}

	return r.selector().match(src, interp.name, interp.cats, interp.mode, scope, 3)
}

// matchSource supplies match candidates and finalisation for the shared matcher,
// decoupling the exact→fallback reduction from where candidates come from. The
// registry-backed resolver merges installed-plus-registry candidates and
// auto-installs on finalize; the installed-only removal path (installedMatcher)
// lists one scope's installed modules and finalizes to identity, never touching
// the registry.
type matchSource interface {
	// exactCandidates returns the merged exact-whole-name candidate set.
	exactCandidates(name string, cats []describeCategory, scope resolveScope) []ModuleMatch
	// fallbackCandidates returns the merged substring/prefix candidate set.
	fallbackCandidates(name string, cats []describeCategory, mode matchMode, scope resolveScope) []ModuleMatch
	// finalize is applied to the single chosen match before it is returned.
	finalize(ModuleMatch) (ModuleMatch, error)
	// unreachableErr is non-nil when the source could not confirm a module's
	// absence (the registry was unreachable), turning a zero-match or sub-floor
	// result into a transient retry error rather than a confirmed not-found.
	unreachableErr() error
}

// match runs the shared exact→fallback reduction over src: the exact-whole-name
// tier first (floor-exempt), then the substring/prefix fallback under mode, each
// reduced to a single decision by reduceMatch. floor bounds the fallback tier
// (the resolver passes 3, removal passes 0); a sub-floor name is a usage error
// unless the source could not confirm absence, in which case it is transient.
func (s *selector) match(src matchSource, name string, cats []describeCategory, mode matchMode, scope resolveScope, floor int) (resolveOutcome, error) {
	exact := src.exactCandidates(name, cats, scope)
	if m, resolved, err := s.reduceMatch(exact, scope, name, src.finalize); resolved || err != nil {
		return resolveOutcome{match: m}, err
	}

	if len(name) < floor {
		if e := src.unreachableErr(); e != nil {
			return resolveOutcome{}, fmt.Errorf("%s %q: registry unavailable: %w", scope.displayType, name, e)
		}
		return resolveOutcome{}, usageError(fmt.Errorf("query %q must be at least %d characters", name, floor))
	}

	fallback := src.fallbackCandidates(name, cats, mode, scope)
	if m, resolved, err := s.reduceMatch(fallback, scope, name, src.finalize); resolved || err != nil {
		return resolveOutcome{match: m}, err
	}
	if e := src.unreachableErr(); e != nil {
		return resolveOutcome{}, fmt.Errorf("%s %q: registry unavailable: %w", scope.displayType, name, e)
	}
	return resolveOutcome{}, notFoundError(fmt.Errorf("%s %q not found", scope.displayType, name))
}

// reduceMatch reduces a candidate set to one finalized decision: zero matches
// report unresolved (resolved=false) for the caller's floor/not-found handling,
// a lone match finalizes directly, and multiple matches go through the selection
// menu (TTY) or the ambiguity error (non-TTY) before finalisation.
func (s *selector) reduceMatch(matches []ModuleMatch, scope resolveScope, query string, finalize func(ModuleMatch) (ModuleMatch, error)) (ModuleMatch, bool, error) {
	switch len(matches) {
	case 0:
		return ModuleMatch{}, false, nil
	case 1:
		m, err := finalize(matches[0])
		return m, err == nil, err
	default:
		selected, err := s.selectMatch(matches, scope, query)
		if err != nil {
			return ModuleMatch{}, false, err
		}
		m, err := finalize(selected)
		return m, err == nil, err
	}
}

// exactCandidates gathers the exact-whole-name tier across the scoped
// categories. A lone installed exact resolves directly without the registry on
// category-specific surfaces (names are unique within a category); cross-category
// surfaces always consult the index to detect a same-name twin in another
// category.
func (r *resolver) exactCandidates(name string, cats []describeCategory, scope resolveScope) []ModuleMatch {
	keys := categoryKeys(cats)
	installedExact := modules.MatchByName(modules.GatherCandidates(keys, r.installedSources(), nil), name, modeExact)

	if !scope.crossCategory && len(installedExact) == 1 {
		debugf(r.stderr, r.flags, dbgResolve, "%s %q: exact installed match", scope.displayType, name)
		return installedExact
	}

	var registryExact []ModuleMatch
	if index, _, _ := r.ensureIndex(); index != nil {
		registryExact = modules.MatchByName(modules.GatherCandidates(keys, nil, index), name, modeExact)
	}
	return mergeMatches(installedExact, registryExact)
}

// fallbackCandidates gathers the substring/prefix tier across the scoped
// categories from installed config plus the registry index.
func (r *resolver) fallbackCandidates(name string, cats []describeCategory, mode matchMode, scope resolveScope) []ModuleMatch {
	keys := categoryKeys(cats)
	installed := modules.MatchByName(modules.GatherCandidates(keys, r.installedSources(), nil), name, mode)

	var reg []ModuleMatch
	if index, _, _ := r.ensureIndex(); index != nil {
		reg = modules.MatchByName(modules.GatherCandidates(keys, nil, index), name, mode)
	}

	matches := mergeMatches(installed, reg)
	debugf(r.stderr, r.flags, dbgResolve, "%s %q: %d installed, %d registry, %d total matches",
		scope.displayType, name, len(installed), len(reg), len(matches))
	return matches
}

// finalize installs a chosen registry-only match (a no-op for an installed one).
func (r *resolver) finalize(m ModuleMatch) (ModuleMatch, error) {
	if err := r.installIfRegistry(m); err != nil {
		return ModuleMatch{}, err
	}
	return m, nil
}

// unreachableErr reports the recorded registry-fetch failure, if any, so the
// matcher can treat a zero-match result as transient rather than confirmed.
func (r *resolver) unreachableErr() error {
	return r.indexErr
}

// selector builds the interactive selection context from the resolver's IO.
func (r *resolver) selector() *selector {
	return &selector{stdin: r.stdin, stdout: r.stdout, stderr: r.stderr, flags: r.flags}
}

// categoryKeys projects the scoped describeCategories to the plain category
// strings the gathering primitive enumerates over.
func categoryKeys(cats []describeCategory) []string {
	keys := make([]string, len(cats))
	for i, cat := range cats {
		keys[i] = cat.category
	}
	return keys
}

// installedSources wraps the resolver's single merged config (local overriding
// global) as the one installed source the gathering primitive enumerates; the
// per-candidate scope tag is irrelevant to resolution.
func (r *resolver) installedSources() []modules.InstalledSource {
	return []modules.InstalledSource{{Config: r.cfg.Value, Scope: config.ScopeMerged}}
}

// collectInstalled returns installed matches under category whose names satisfy
// mode, gathered from the resolver's merged config.
func (r *resolver) collectInstalled(category, query string, mode matchMode) []ModuleMatch {
	return collectInstalledFrom(r.cfg.Value, category, query, mode)
}

// collectInstalledFrom returns matches under category in cfg whose names satisfy
// mode. It is the registry-free installed gather shared by the resolver and the
// installed-only removal matcher, layering the literal name matcher over the
// shared gathering primitive; a zero cfg (no config loaded) yields no matches.
func collectInstalledFrom(cfg cue.Value, category, query string, mode matchMode) []ModuleMatch {
	cands := modules.GatherCandidates([]string{category}, []modules.InstalledSource{{Config: cfg, Scope: config.ScopeMerged}}, nil)
	return modules.MatchByName(cands, query, mode)
}

// mergeMatches de-duplicates by category:name with the installed entry winning,
// and orders installed before registry, then lexically by category:name. There
// is no score-based ordering: resolution does not score.
func mergeMatches(installed, reg []ModuleMatch) []ModuleMatch {
	seen := make(map[string]bool)
	var out []ModuleMatch
	for _, group := range [][]ModuleMatch{installed, reg} {
		for _, m := range group {
			key := formatAddress(m.Category, m.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		iInstalled := out[i].Source == ModuleSourceInstalled
		jInstalled := out[j].Source == ModuleSourceInstalled
		if iInstalled != jInstalled {
			return iInstalled
		}
		return formatAddress(out[i].Category, out[i].Name) < formatAddress(out[j].Category, out[j].Name)
	})
	return out
}

// matchLabel renders a match as the user-facing token that round-trips back to
// it: a bare name on a category-specific surface, category:name on a
// cross-category one.
func matchLabel(m ModuleMatch, scope resolveScope) string {
	if scope.crossCategory {
		return formatAddress(m.Category, m.Name)
	}
	return m.Name
}

// selector owns the interactive reduction of a candidate set to one match — the
// TTY selection menu and the non-TTY ambiguity error. It carries only the IO and
// flags selection needs, so the registry-free removal path and the
// registry-backed resolver share one selection rule without sharing the
// resolver's index machinery.
type selector struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	flags  *Flags
}

// selectMatch reduces more than one match to one: a selection menu on a TTY, an
// error listing the matches otherwise. The listed forms are valid arguments
// that round-trip back to the same entry.
func (s *selector) selectMatch(matches []ModuleMatch, scope resolveScope, query string) (ModuleMatch, error) {
	sort.SliceStable(matches, func(i, j int) bool {
		return matchLabel(matches[i], scope) < matchLabel(matches[j], scope)
	})

	if !isTerminal(s.stdin) {
		labels := make([]string, len(matches))
		for i, m := range matches {
			labels[i] = matchLabel(m, scope)
		}
		return ModuleMatch{}, usageError(fmt.Errorf("ambiguous %s %q matches: %s\nSpecify an exact name, category-qualify it (category:name), or run interactively",
			scope.displayType, query, strings.Join(labels, ", ")))
	}

	displayCount := min(len(matches), maxModuleResults)

	fmt.Fprintf(s.stdout, "Found %d %ss matching %q:\n\n", len(matches), scope.displayType, query)

	maxLabelLen := 0
	for i := range displayCount {
		if l := len(matchLabel(matches[i], scope)); l > maxLabelLen {
			maxLabelLen = l
		}
	}
	for i := range displayCount {
		m := matches[i]
		label := matchLabel(m, scope)
		padding := strings.Repeat(" ", maxLabelLen-len(label)+2)
		sourceColor := tui.ColorRegistry
		if m.Source == ModuleSourceInstalled {
			sourceColor = tui.ColorInstalled
		}
		fmt.Fprintf(s.stdout, "  %2d. %s%s%s\n", i+1, label, padding, sourceColor.Sprint(m.Source))
	}
	if displayCount < len(matches) {
		fmt.Fprintf(s.stdout, "\nShowing %d of %d matches. Refine search for more specific results.\n", displayCount, len(matches))
	}

	fmt.Fprintln(s.stdout)
	fmt.Fprintf(s.stdout, "Select %s: ", tui.Annotate("1-%d", displayCount))

	input, err := bufio.NewReader(s.stdin).ReadString('\n')
	if err != nil {
		return ModuleMatch{}, fmt.Errorf("reading input: %w", err)
	}
	input = strings.TrimSpace(input)

	if choice, err := strconv.Atoi(input); err == nil {
		if choice >= 1 && choice <= displayCount {
			fmt.Fprintln(s.stdout)
			return matches[choice-1], nil
		}
		return ModuleMatch{}, fmt.Errorf("invalid selection: %s (choose 1-%d)", input, displayCount)
	}

	selected, err := matchByTypedName(matches[:displayCount], scope, input)
	if err != nil {
		return ModuleMatch{}, err
	}
	fmt.Fprintln(s.stdout)
	return selected, nil
}

// matchByTypedName selects the one shown entry a typed name uniquely
// identifies, in precedence order: an exact label (category:name on a
// cross-category menu) wins outright; otherwise an exact bare name wins when it
// is unique; otherwise a single substring match over the labels is accepted. A
// bare name shared by two cross-category twins is ambiguous and rejected, so the
// user re-enters the full category:name or picks by number.
func matchByTypedName(shown []ModuleMatch, scope resolveScope, input string) (ModuleMatch, error) {
	inputLower := strings.ToLower(input)
	for _, m := range shown {
		if strings.ToLower(matchLabel(m, scope)) == inputLower {
			return m, nil
		}
	}
	var nameMatches []ModuleMatch
	for _, m := range shown {
		if strings.ToLower(m.Name) == inputLower {
			nameMatches = append(nameMatches, m)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) == 0 {
		var subMatches []ModuleMatch
		for _, m := range shown {
			if strings.Contains(strings.ToLower(matchLabel(m, scope)), inputLower) {
				subMatches = append(subMatches, m)
			}
		}
		if len(subMatches) == 1 {
			return subMatches[0], nil
		}
	}
	return ModuleMatch{}, fmt.Errorf("invalid selection: %s", input)
}

// installIfRegistry auto-installs the match when it is registry-only. On success
// r.autoInstall sets r.didInstall and r.cfgStale; installed matches are a no-op.
func (r *resolver) installIfRegistry(match ModuleMatch) error {
	if match.Source != ModuleSourceRegistry {
		return nil
	}
	return r.autoInstall(r.client, modules.SearchResult{
		Category: match.Category,
		Name:     match.Name,
		Entry:    match.Entry,
	})
}
