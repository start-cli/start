package modules

import (
	"regexp"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/registry"
)

// Source distinguishes where a gathered candidate was enumerated from. Its
// string values are surfaced verbatim in the resolver's selection menu, so they
// read as "installed" / "registry".
type Source string

const (
	SourceInstalled Source = "installed"
	SourceRegistry  Source = "registry"
)

// Candidate is one enumerated module produced by GatherCandidates before any
// matching, merging, or de-duplication. It carries the index entry
// (description, tags, origin) regardless of source — extracted from the CUE for
// installed candidates, taken from the index for registry ones — so a matcher
// can filter and a display can render by description and tag, not just name.
// Scope records the config scope an installed candidate was found in; it is
// unset for registry candidates.
type Candidate struct {
	Name     string
	Category string
	Source   Source
	Scope    config.Scope
	Entry    registry.IndexEntry
}

// InstalledSource is one installed config value paired with the scope its
// candidates are tagged with. Callers that load local and global config
// separately pass two sources; a caller holding a single merged config
// (local overriding global) passes one and ignores the per-candidate scope.
type InstalledSource struct {
	Config cue.Value
	Scope  config.Scope
}

// MatchMode selects how a candidate name is compared against a query in the
// literal name-only matcher. The exact tier uses ModeExact; the fallback tier
// uses ModeSubstring for a bare term and ModePrefix for a category-qualified
// one. Matching is literal (no regex) and case-insensitive.
type MatchMode int

const (
	ModeExact MatchMode = iota
	ModeSubstring
	ModePrefix
)

// GatherCandidates enumerates every module under the given categories from the
// selected sources, returning the full candidate set tagged by source and
// scope, un-deduplicated and unfiltered. Installed candidates are read from each
// supplied config value (skip an empty value, which yields nothing); registry
// candidates come from index when it is non-nil. This is the single place
// installed and registry candidates are enumerated; merge, de-duplication, and
// matching are policies layered on top by the caller.
func GatherCandidates(categories []string, installed []InstalledSource, index *registry.Index) []Candidate {
	var out []Candidate
	for _, category := range categories {
		for _, src := range installed {
			out = append(out, gatherInstalled(src.Config, category, src.Scope)...)
		}
		if index != nil {
			for name, entry := range indexEntries(index, category) {
				out = append(out, Candidate{Name: name, Category: category, Source: SourceRegistry, Entry: entry})
			}
		}
	}
	return out
}

// gatherInstalled enumerates the installed modules under category in cfg, tagging
// each with scope and extracting its index entry from the CUE. A zero cfg (no
// config loaded) or a missing category yields no candidates.
func gatherInstalled(cfg cue.Value, category string, scope config.Scope) []Candidate {
	catVal := cfg.LookupPath(cue.ParsePath(category))
	if !catVal.Exists() {
		return nil
	}
	iter, err := catVal.Fields()
	if err != nil {
		return nil
	}
	var out []Candidate
	for iter.Next() {
		name := iter.Selector().Unquoted()
		out = append(out, Candidate{
			Name:     name,
			Category: category,
			Source:   SourceInstalled,
			Scope:    scope,
			Entry:    extractIndexEntryFromCUE(iter.Value()),
		})
	}
	return out
}

// indexEntries returns the index map for a category, or nil for an unknown one.
func indexEntries(index *registry.Index, category string) map[string]registry.IndexEntry {
	switch category {
	case "agents":
		return index.Agents
	case "roles":
		return index.Roles
	case "contexts":
		return index.Contexts
	case "tasks":
		return index.Tasks
	case "skills":
		return index.Skills
	default:
		return nil
	}
}

// NameMatches reports whether candidate matches query under mode, comparing
// case-insensitively over the names only. Both operands are lower-cased so the
// slash in a path-shaped name is an ordinary character, not a separator.
func NameMatches(query, candidate string, mode MatchMode) bool {
	q := strings.ToLower(query)
	c := strings.ToLower(candidate)
	switch mode {
	case ModeExact:
		return q == c
	case ModePrefix:
		return strings.HasPrefix(c, q)
	default:
		return strings.Contains(c, q)
	}
}

// MatchByName filters cands to those whose names satisfy the literal name-only
// rule under mode, preserving input order. It is the reusable matcher the
// resolution-style callers layer over the gathered set.
func MatchByName(cands []Candidate, query string, mode MatchMode) []Candidate {
	var out []Candidate
	for _, c := range cands {
		if NameMatches(query, c.Name, mode) {
			out = append(out, c)
		}
	}
	return out
}

// MatchSearch applies the regex/tag search matcher over cands, returning the
// matched candidates in input order. Query terms are regex patterns combined
// with AND; tags (if any) additionally require an OR tag match. A leading
// "category:" prefix scopes the match to that category (an unknown one is a
// usage fault). This is the shared matcher search and install layer over the
// gathered candidates; it matches names, descriptions, and tags.
func MatchSearch(cands []Candidate, query string, tags []string) ([]Candidate, error) {
	category, query, err := SplitCategoryQuery(query)
	if err != nil {
		return nil, err
	}

	terms := ParseSearchPatterns(query)
	if len(terms) == 0 && len(tags) == 0 {
		return nil, nil
	}

	var patterns []*regexp.Regexp
	if len(terms) > 0 {
		patterns, err = CompileSearchTerms(terms)
		if err != nil {
			return nil, err
		}
	}

	var out []Candidate
	for _, c := range cands {
		if category != "" && c.Category != category {
			continue
		}
		if candidateMatches(c, patterns, tags) {
			out = append(out, c)
		}
	}
	return out, nil
}

// candidateMatches applies the three-way search rule to one candidate. The
// branches stay explicit rather than collapsed into a single
// matchesPatterns(...) && matchesAnyTag(...): tags-only mode has nil patterns
// (matchesPatterns reports false) and patterns-only mode has nil tags
// (matchesAnyTag reports false), so a merged expression would wrongly exclude
// both.
func candidateMatches(c Candidate, patterns []*regexp.Regexp, tags []string) bool {
	switch {
	case len(patterns) > 0 && len(tags) > 0:
		return matchesPatterns(c.Name, c.Entry, patterns) && matchesAnyTag(c.Entry.Tags, tags)
	case len(tags) > 0:
		return matchesAnyTag(c.Entry.Tags, tags)
	default:
		return matchesPatterns(c.Name, c.Entry, patterns)
	}
}

// ResultsFromCandidates sorts candidates by category order then name and
// projects them to SearchResult, the element type search and install consume for
// display and JSON.
func ResultsFromCandidates(cands []Candidate) []SearchResult {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Category != cands[j].Category {
			return CategoryOrder(cands[i].Category) < CategoryOrder(cands[j].Category)
		}
		return cands[i].Name < cands[j].Name
	})
	out := make([]SearchResult, len(cands))
	for i, c := range cands {
		out[i] = SearchResult{Category: c.Category, Name: c.Name, Entry: c.Entry}
	}
	return out
}
