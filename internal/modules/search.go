package modules

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/fault"
	"github.com/p3bot/start/internal/registry"
)

// searchCategories lists the library module categories in display order. It is
// the modules-package source of truth for category-prefix validation, mirroring
// the CLI's describeCategories ordering so error messages match across surfaces.
var searchCategories = []string{"agents", "roles", "contexts", "tasks", "skills"}

// SplitCategoryQuery peels an optional "category:" prefix off a search query so
// install and search honour the same category:name addressing the resolution
// engine uses. With no colon the whole input is the query and category is "".
// With a colon the prefix must name a known category — an unknown one is a usage
// fault (exit 2), matching how get/describe/--role reject a bad category — and
// the remainder becomes the query, scoped to that category. Comparison is
// case-sensitive against the lowercase category names, consistent with the CLI's
// parseAddress. Module names never contain a colon (lowercase kebab-case with
// slashes), so a colon is always a category delimiter, never part of a name.
//
// The CLI also calls this to measure its 3-character floor against the returned
// query (the name only, prefix excluded), keeping search and install consistent
// with the engine's floor rule; MatchSearch re-splits internally so it stays
// correct regardless of any caller-side split.
func SplitCategoryQuery(input string) (category, query string, err error) {
	before, after, ok := strings.Cut(input, ":")
	if !ok {
		return "", input, nil
	}
	if !slices.Contains(searchCategories, before) {
		return "", "", fault.Usage(fmt.Errorf("unknown category %q (valid: %s)", before, strings.Join(searchCategories, ", ")))
	}
	return before, after, nil
}

// SearchResult holds a matched index entry with its category and name.
type SearchResult struct {
	Category string              `json:"category"`
	Name     string              `json:"name"`
	Entry    registry.IndexEntry `json:"entry"`
}

// ParseSearchTerms splits input into unique, lowercased terms (on whitespace and commas).
// Use ParseSearchPatterns instead when terms will be compiled as regex patterns.
func ParseSearchTerms(input string) []string {
	normalized := strings.ReplaceAll(input, ",", " ")
	parts := strings.Fields(normalized)

	seen := make(map[string]bool, len(parts))
	var terms []string
	for _, p := range parts {
		lower := strings.ToLower(p)
		if !seen[lower] {
			seen[lower] = true
			terms = append(terms, lower)
		}
	}
	return terms
}

// ParseSearchPatterns splits input into unique patterns, preserving original case.
// Dedup is case-insensitive but keeps the first casing, so case-sensitive regex
// escapes like \S, \D, \W, \B are not corrupted.
func ParseSearchPatterns(input string) []string {
	normalized := strings.ReplaceAll(input, ",", " ")
	parts := strings.Fields(normalized)

	seen := make(map[string]bool, len(parts))
	var patterns []string
	for _, p := range parts {
		lower := strings.ToLower(p)
		if !seen[lower] {
			seen[lower] = true
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// CompileSearchTerms compiles search terms into case-insensitive regular expressions.
func CompileSearchTerms(terms []string) ([]*regexp.Regexp, error) {
	patterns := make([]*regexp.Regexp, len(terms))
	for i, term := range terms {
		re, err := regexp.Compile("(?i)" + term)
		if err != nil {
			return nil, fmt.Errorf("invalid search pattern %q: %w", term, err)
		}
		patterns[i] = re
	}
	return patterns, nil
}

// ValidateSearchQuery checks query length: empty allowed only with tags, else >= 3 chars total.
func ValidateSearchQuery(terms, tags []string) error {
	totalLen := 0
	for _, t := range terms {
		totalLen += len(t)
	}
	if totalLen > 0 && totalLen < 3 {
		return fmt.Errorf("query must be at least 3 characters")
	}
	if totalLen == 0 && len(tags) == 0 {
		return fmt.Errorf("query must be at least 3 characters")
	}
	return nil
}

// matchesAnyTag reports whether any entry tag case-insensitively equals any filter tag.
func matchesAnyTag(entryTags, filterTags []string) bool {
	for _, ft := range filterTags {
		ftLower := strings.ToLower(ft)
		for _, et := range entryTags {
			if strings.ToLower(et) == ftLower {
				return true
			}
		}
	}
	return false
}

// matchesPatterns reports whether every pattern matches the name, description, or any
// tag (AND across patterns). Returns false for nil patterns.
func matchesPatterns(name string, entry registry.IndexEntry, patterns []*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return false
	}

	for _, pattern := range patterns {
		matched := pattern.MatchString(name) ||
			pattern.MatchString(entry.Description) ||
			slices.ContainsFunc(entry.Tags, pattern.MatchString)
		if !matched {
			return false // AND: every pattern must match something
		}
	}

	return true
}

// extractIndexEntryFromCUE extracts description, tags, and origin from a CUE value
// into an IndexEntry for matching.
func extractIndexEntryFromCUE(v cue.Value) registry.IndexEntry {
	var entry registry.IndexEntry

	if desc := v.LookupPath(cue.ParsePath("description")); desc.Exists() {
		entry.Description, _ = desc.String()
	}

	if tags := v.LookupPath(cue.ParsePath("tags")); tags.Exists() {
		tagIter, err := tags.List()
		if err == nil {
			for tagIter.Next() {
				if s, err := tagIter.Value().String(); err == nil {
					entry.Tags = append(entry.Tags, s)
				}
			}
		}
	}

	if origin := v.LookupPath(cue.ParsePath("origin")); origin.Exists() {
		entry.Module, _ = origin.String()
	}

	return entry
}

// CategoryOrder returns the display order for a category.
func CategoryOrder(category string) int {
	switch category {
	case "agents":
		return 0
	case "roles":
		return 1
	case "contexts":
		return 2
	case "tasks":
		return 3
	case "skills":
		return 4
	default:
		return 5
	}
}
