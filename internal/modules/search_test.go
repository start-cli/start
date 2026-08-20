package modules

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/fault"
	"github.com/p3bot/start/internal/registry"
)

// searchIndexResults reproduces the retired SearchIndex over the shared
// gathering primitive plus the regex/tag matcher: enumerate registry candidates
// across all categories, match, then project to sorted SearchResults.
func searchIndexResults(index *registry.Index, query string, tags []string) ([]SearchResult, error) {
	matched, err := MatchSearch(GatherCandidates(searchCategories, nil, index), query, tags)
	if err != nil {
		return nil, err
	}
	return ResultsFromCandidates(matched), nil
}

// searchInstalledResults reproduces the retired SearchInstalledConfig over the
// shared primitive: enumerate one category's installed candidates, match, then
// project to sorted SearchResults.
func searchInstalledResults(cfg cue.Value, category, query string, tags []string) ([]SearchResult, error) {
	matched, err := MatchSearch(GatherCandidates([]string{category}, []InstalledSource{{Config: cfg, Scope: config.ScopeLocal}}, nil), query, tags)
	if err != nil {
		return nil, err
	}
	return ResultsFromCandidates(matched), nil
}

// registryCandidates builds registry-sourced candidates from an index entry map.
func registryCandidates(category string, entries map[string]registry.IndexEntry) []Candidate {
	var out []Candidate
	for name, entry := range entries {
		out = append(out, Candidate{Name: name, Category: category, Source: SourceRegistry, Entry: entry})
	}
	return out
}

func TestParseSearchTerms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"single term", "golang", []string{"golang"}},
		{"two space-separated", "go expert", []string{"go", "expert"}},
		{"csv terms", "go,expert", []string{"go", "expert"}},
		{"mixed delimiters", "go expert,review", []string{"go", "expert", "review"}},
		{"empty string", "", nil},
		{"only commas", ",,,", nil},
		{"only spaces", "   ", nil},
		{"duplicate terms", "go go", []string{"go"}},
		{"case dedup", "Go go GO", []string{"go"}},
		{"leading trailing whitespace", "  go  expert  ", []string{"go", "expert"}},
		{"leading trailing commas", ",go,expert,", []string{"go", "expert"}},
		{"empty csv segments", "go,,expert", []string{"go", "expert"}},
		{"mixed case", "Go Expert", []string{"go", "expert"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSearchTerms(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParseSearchTerms(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSearchPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"single term", "golang", []string{"golang"}},
		{"preserves case", "Go Expert", []string{"Go", "Expert"}},
		{"csv terms", "Go,Expert", []string{"Go", "Expert"}},
		{"mixed delimiters", "Go Expert,Review", []string{"Go", "Expert", "Review"}},
		{"empty string", "", nil},
		{"only commas", ",,,", nil},
		{"only spaces", "   ", nil},
		{"dedup case insensitive keeps first", "Go go GO", []string{"Go"}},
		{"preserves regex escapes", `\Stest \Dfoo`, []string{`\Stest`, `\Dfoo`}},
		{"preserves anchors", "^Home expert$", []string{"^Home", "expert$"}},
		{"preserves character class", "[A-Z]test", []string{"[A-Z]test"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSearchPatterns(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParseSearchPatterns(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchesPatterns(t *testing.T) {
	t.Parallel()

	entry := registry.IndexEntry{
		Module:      "github.com/test/roles/golang/assistant@v0",
		Description: "Go programming expert for code assistance",
		Tags:        []string{"golang", "programming", "expert", "cli"},
	}

	tests := []struct {
		name       string
		moduleName string
		terms      []string
		want       bool
	}{
		{
			name:       "single term matches name and tag",
			moduleName: "golang",
			terms:      []string{"golang"},
			want:       true,
		},
		{
			name:       "term matches description only",
			moduleName: "golang",
			terms:      []string{"assistance"},
			want:       true,
		},
		{
			name:       "term matches tag only",
			moduleName: "golang",
			terms:      []string{"cli"}, // absent from name and description
			want:       true,
		},
		{
			name:       "two terms both match",
			moduleName: "golang",
			terms:      []string{"golang", "expert"},
			want:       true,
		},
		{
			name:       "two terms one fails",
			moduleName: "golang",
			terms:      []string{"golang", "python"},
			want:       false,
		},
		{
			name:       "empty terms",
			moduleName: "golang",
			terms:      nil,
			want:       false,
		},
		{
			name:       "three terms all match",
			moduleName: "golang",
			terms:      []string{"golang", "programming", "code"},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patterns []*regexp.Regexp
			if tt.terms != nil {
				var err error
				patterns, err = CompileSearchTerms(tt.terms)
				if err != nil {
					t.Fatalf("CompileSearchTerms() error: %v", err)
				}
			}
			got := matchesPatterns(tt.moduleName, entry, patterns)
			if got != tt.want {
				t.Errorf("matchesPatterns() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearchIndex(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Agents: map[string]registry.IndexEntry{
			"ai/claude": {
				Module:      "github.com/test/agents/ai/claude@v0",
				Description: "Anthropic Claude AI",
				Tags:        []string{"ai", "llm"},
			},
		},
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {
				Module:      "github.com/test/roles/golang/assistant@v0",
				Description: "Go programming expert",
				Tags:        []string{"golang", "programming"},
			},
			"golang/code-review": {
				Module:      "github.com/test/roles/golang/code-review@v0",
				Description: "Review Go code for quality",
				Tags:        []string{"golang", "review"},
			},
		},
		Contexts: map[string]registry.IndexEntry{
			"cwd/agents-md": {
				Module:      "github.com/test/contexts/cwd/agents-md@v0",
				Description: "Read AGENTS.md file",
				Tags:        []string{"repository", "guidelines"},
			},
		},
		Tasks: map[string]registry.IndexEntry{
			"start/commit": {
				Module:      "github.com/test/tasks/start/commit@v0",
				Description: "Create git commit",
				Tags:        []string{"git", "commit"},
			},
		},
	}

	tests := []struct {
		name      string
		query     string
		wantCount int
		wantFirst string // category/name of first result
	}{
		{
			name:      "find by exact name",
			query:     "claude",
			wantCount: 1,
			wantFirst: "agents/ai/claude",
		},
		{
			name:      "find by partial name",
			query:     "golang",
			wantCount: 2,
			wantFirst: "roles/golang/assistant", // name-ascending: assistant before code-review
		},
		{
			name:      "find by description",
			query:     "programming",
			wantCount: 1,
			wantFirst: "roles/golang/assistant",
		},
		{
			name:      "find by tag",
			query:     "commit",
			wantCount: 1,
			wantFirst: "tasks/start/commit",
		},
		{
			name:      "no matches",
			query:     "nonexistent",
			wantCount: 0,
		},
		{
			name:      "multiple matches",
			query:     "review",
			wantCount: 1,
			wantFirst: "roles/golang/code-review",
		},
		{
			name:      "multi-term AND narrows results",
			query:     "golang review",
			wantCount: 1,
			wantFirst: "roles/golang/code-review",
		},
		{
			name:      "multi-term AND with csv",
			query:     "golang,review",
			wantCount: 1,
			wantFirst: "roles/golang/code-review",
		},
		{
			name:      "multi-term AND no match when one term fails",
			query:     "golang python",
			wantCount: 0,
		},
		{
			name:      "empty query returns nil",
			query:     "",
			wantCount: 0,
		},
		{
			name:      "category prefix scopes to that category",
			query:     "roles:golang",
			wantCount: 2,
			wantFirst: "roles/golang/assistant",
		},
		{
			name:      "category prefix excludes other categories",
			query:     "roles:claude",
			wantCount: 0,
		},
		{
			name:      "category prefix with full path name",
			query:     "agents:ai/claude",
			wantCount: 1,
			wantFirst: "agents/ai/claude",
		},
		{
			name:      "category prefix with empty remainder returns nil",
			query:     "roles:",
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := searchIndexResults(index, tt.query, nil)
			if err != nil {
				t.Fatalf("searchIndexResults() error: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("searchIndexResults() returned %d results, want %d", len(results), tt.wantCount)
			}

			if tt.wantCount > 0 && tt.wantFirst != "" {
				first := results[0].Category + "/" + results[0].Name
				if first != tt.wantFirst {
					t.Errorf("searchIndexResults() first result = %q, want %q", first, tt.wantFirst)
				}
			}
		})
	}
}

func TestCompileSearchTerms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		terms   []string
		wantErr bool
	}{
		{"plain terms", []string{"golang", "expert"}, false},
		{"starts with anchor", []string{"^golang"}, false},
		{"ends with anchor", []string{"expert$"}, false},
		{"dot wildcard", []string{"go.ang"}, false},
		{"star quantifier", []string{"go.*expert"}, false},
		{"plus quantifier", []string{"go.+expert"}, false},
		{"character class", []string{"[gG]olang"}, false},
		{"invalid regex - unclosed bracket", []string{"[unclosed"}, true},
		{"invalid regex - bad repetition", []string{"*invalid"}, true},
		{"mixed valid and invalid", []string{"golang", "[bad"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns, err := CompileSearchTerms(tt.terms)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(patterns) != len(tt.terms) {
				t.Errorf("got %d patterns, want %d", len(patterns), len(tt.terms))
			}
		})
	}
}

func TestSearchIndex_Regex(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Agents: map[string]registry.IndexEntry{
			"ai/claude": {
				Module:      "github.com/test/agents/ai/claude@v0",
				Description: "Anthropic Claude AI",
				Tags:        []string{"ai", "llm"},
			},
		},
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {
				Module:      "github.com/test/roles/golang/assistant@v0",
				Description: "Go programming expert",
				Tags:        []string{"golang", "programming"},
			},
			"golang/code-review": {
				Module:      "github.com/test/roles/golang/code-review@v0",
				Description: "Review Go code for quality",
				Tags:        []string{"golang", "review"},
			},
			"python/assistant": {
				Module:      "github.com/test/roles/python/assistant@v0",
				Description: "Python programming expert",
				Tags:        []string{"python", "programming"},
			},
		},
		Contexts: map[string]registry.IndexEntry{
			"cwd/agents-md": {
				Module:      "github.com/test/contexts/cwd/agents-md@v0",
				Description: "Read AGENTS.md file",
				Tags:        []string{"repository", "guidelines"},
			},
			"home/environment": {
				Module:      "github.com/test/contexts/home/environment@v0",
				Description: "Home environment context",
				Tags:        []string{"home", "environment"},
			},
		},
		Tasks: map[string]registry.IndexEntry{
			"start/commit": {
				Module:      "github.com/test/tasks/start/commit@v0",
				Description: "Create git commit",
				Tags:        []string{"git", "commit"},
			},
		},
	}

	tests := []struct {
		name      string
		query     string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "starts with anchor",
			query:     "^golang",
			wantCount: 2, // golang/assistant and golang/code-review
		},
		{
			name:      "ends with anchor",
			query:     "assistant$",
			wantCount: 2, // golang/assistant and python/assistant
		},
		{
			name:      "dot as any character",
			query:     "go.ang",
			wantCount: 2, // golang in name matches
		},
		{
			name:      "star quantifier",
			query:     "go.*review",
			wantCount: 1, // golang/code-review
		},
		{
			name:      "plus quantifier in description",
			query:     "Go.+expert",
			wantCount: 1, // "Go programming expert" matches
		},
		{
			name:      "anchor no match",
			query:     "^assistant",
			wantCount: 0, // no name starts with "assistant"
		},
		{
			name:      "context starts with home",
			query:     "^home",
			wantCount: 1, // home/environment
		},
		{
			name:      "plain terms still work",
			query:     "claude",
			wantCount: 1,
		},
		{
			name:      "case insensitive regex",
			query:     "^GOLANG",
			wantCount: 2,
		},
		{
			name:    "invalid regex returns error",
			query:   "[unclosed",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := searchIndexResults(index, tt.query, nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("searchIndexResults() error: %v", err)
			}

			if len(results) != tt.wantCount {
				var names []string
				for _, r := range results {
					names = append(names, r.Category+"/"+r.Name)
				}
				t.Errorf("searchIndexResults(%q) returned %d results %v, want %d", tt.query, len(results), names, tt.wantCount)
			}
		})
	}
}

func TestCategoryOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		category string
		want     int
	}{
		{"agents", 0},
		{"roles", 1},
		{"contexts", 2},
		{"tasks", 3},
		{"skills", 4},
		{"unknown", 5},
	}

	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			got := CategoryOrder(tt.category)
			if got != tt.want {
				t.Errorf("CategoryOrder(%q) = %d, want %d", tt.category, got, tt.want)
			}
		})
	}
}

func TestSearchCategory(t *testing.T) {
	t.Parallel()

	entries := map[string]registry.IndexEntry{
		"golang/assistant": {
			Module:      "github.com/test/roles/golang/assistant@v0",
			Description: "Go programming expert",
			Tags:        []string{"golang", "programming"},
		},
		"python/assistant": {
			Module:      "github.com/test/roles/python/assistant@v0",
			Description: "Python programming expert",
			Tags:        []string{"python", "programming"},
		},
	}

	tests := []struct {
		name      string
		terms     []string
		wantCount int
	}{
		{
			name:      "find golang",
			terms:     []string{"golang"},
			wantCount: 1,
		},
		{
			name:      "find programming (both match)",
			terms:     []string{"programming"},
			wantCount: 2,
		},
		{
			name:      "no match",
			terms:     []string{"javascript"},
			wantCount: 0,
		},
		{
			name:      "multi-term AND narrows to one",
			terms:     []string{"golang", "programming"},
			wantCount: 1,
		},
		{
			name:      "multi-term AND no match",
			terms:     []string{"golang", "python"},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := MatchSearch(registryCandidates("roles", entries), strings.Join(tt.terms, " "), nil)
			if err != nil {
				t.Fatalf("MatchSearch() error: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("MatchSearch() returned %d results, want %d", len(results), tt.wantCount)
			}

			for _, r := range results {
				if r.Category != "roles" {
					t.Errorf("MatchSearch() returned result with category %q, want %q", r.Category, "roles")
				}
			}
		})
	}
}

func TestSearchIndex_NilIndex(t *testing.T) {
	t.Parallel()

	results, err := searchIndexResults(nil, "test", nil)
	if err != nil {
		t.Fatalf("searchIndexResults() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("searchIndexResults(nil, ...) returned %d results, want 0", len(results))
	}
}

func TestSearchIndex_EmptyIndex(t *testing.T) {
	t.Parallel()

	index := &registry.Index{}
	results, err := searchIndexResults(index, "test", nil)
	if err != nil {
		t.Fatalf("searchIndexResults() error: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("searchIndexResults(empty, ...) returned %d results, want 0", len(results))
	}
}

// TestSearchIndex_NilMaps tests SearchIndex with non-nil Index but nil category maps.
func TestSearchIndex_NilMaps(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Agents:   nil,
		Roles:    nil,
		Contexts: nil,
		Tasks:    nil,
	}
	results, err := searchIndexResults(index, "test", nil)
	if err != nil {
		t.Fatalf("searchIndexResults() error: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("searchIndexResults(nil maps, ...) returned %d results, want 0", len(results))
	}
}

func TestSearchResultOrdering(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {
				Module:      "github.com/test/roles/golang/assistant@v0",
				Description: "Go programming expert",
				Tags:        []string{"golang"},
			},
			"golang/code-review": {
				Module:      "github.com/test/roles/golang/code-review@v0",
				Description: "Review code quality",
				Tags:        []string{"golang"},
			},
		},
		Tasks: map[string]registry.IndexEntry{
			"golang/test": {
				Module:      "github.com/test/tasks/golang/test@v0",
				Description: "Run Go tests",
				Tags:        []string{"golang", "testing"},
			},
		},
	}

	results, err := searchIndexResults(index, "golang", nil)
	if err != nil {
		t.Fatalf("searchIndexResults() error: %v", err)
	}

	// Deterministic order: category order (roles before tasks) then name ascending.
	want := []string{
		"roles/golang/assistant",
		"roles/golang/code-review",
		"tasks/golang/test",
	}

	got := make([]string, len(results))
	for i, r := range results {
		got[i] = r.Category + "/" + r.Name
	}

	if !slices.Equal(got, want) {
		t.Errorf("searchIndexResults() order = %v, want %v", got, want)
	}
}

func TestSearchInstalledConfig(t *testing.T) {
	t.Parallel()

	cctx := cuecontext.New()
	cfg := cctx.CompileString(`{
		agents: {
			claude: {
				description: "Anthropic Claude AI assistant"
				tags: ["ai", "llm"]
				origin: "github.com/test/agents/claude@v0"
			}
			"gemini-non-interactive": {
				description: "Google Gemini non-interactive mode"
				tags: ["ai", "google"]
			}
		}
		roles: {
			"golang/assistant": {
				description: "Go programming expert"
				tags: ["golang", "programming"]
			}
		}
	}`)

	tests := []struct {
		name      string
		cueKey    string
		category  string
		query     string
		wantCount int
		wantFirst string
	}{
		{
			name:      "find agent by name substring",
			cueKey:    "agents",
			category:  "agents",
			query:     "claude",
			wantCount: 1,
			wantFirst: "claude",
		},
		{
			name:      "find agent by tag",
			cueKey:    "agents",
			category:  "agents",
			query:     "ai",
			wantCount: 2,
		},
		{
			name:      "find agent by description",
			cueKey:    "agents",
			category:  "agents",
			query:     "google",
			wantCount: 1,
			wantFirst: "gemini-non-interactive",
		},
		{
			name:      "find role by name",
			cueKey:    "roles",
			category:  "roles",
			query:     "golang",
			wantCount: 1,
			wantFirst: "golang/assistant",
		},
		{
			name:      "no matches",
			cueKey:    "agents",
			category:  "agents",
			query:     "nonexistent",
			wantCount: 0,
		},
		{
			name:      "missing category returns nil",
			cueKey:    "tasks",
			category:  "tasks",
			query:     "anything",
			wantCount: 0,
		},
		{
			name:      "multi-term AND narrows agents",
			cueKey:    "agents",
			category:  "agents",
			query:     "ai,claude",
			wantCount: 1,
			wantFirst: "claude",
		},
		{
			name:      "multi-term AND no match",
			cueKey:    "agents",
			category:  "agents",
			query:     "claude google",
			wantCount: 0,
		},
		{
			name:      "category prefix matching this category",
			cueKey:    "agents",
			category:  "agents",
			query:     "agents:claude",
			wantCount: 1,
			wantFirst: "claude",
		},
		{
			name:      "category prefix for another category self-skips",
			cueKey:    "agents",
			category:  "agents",
			query:     "roles:golang",
			wantCount: 0,
		},
		{
			name:      "category prefix scoping the role call",
			cueKey:    "roles",
			category:  "roles",
			query:     "roles:golang",
			wantCount: 1,
			wantFirst: "golang/assistant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := searchInstalledResults(cfg, tt.category, tt.query, nil)
			if err != nil {
				t.Fatalf("SearchInstalledConfig() error: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("SearchInstalledConfig() returned %d results, want %d", len(results), tt.wantCount)
			}

			if tt.wantFirst != "" && len(results) > 0 {
				if results[0].Name != tt.wantFirst {
					t.Errorf("SearchInstalledConfig() first result = %q, want %q", results[0].Name, tt.wantFirst)
				}
			}

			for _, r := range results {
				if r.Category != tt.category {
					t.Errorf("result %q has category %q, want %q", r.Name, r.Category, tt.category)
				}
			}
		})
	}
}

// An unknown category prefix is a usage fault on both search surfaces, so the
// CLI maps it to exit 2 the way get/describe/--role reject a bad category.
func TestSearchCategoryPrefixUnknownCategory(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {Description: "Go programming expert"},
		},
	}
	cfg := cuecontext.New().CompileString(`{
		roles: {"golang/assistant": {description: "Go programming expert"}}
	}`)

	t.Run("SearchIndex", func(t *testing.T) {
		_, err := searchIndexResults(index, "bogus:golang", nil)
		if !errors.Is(err, fault.ErrUsage) {
			t.Errorf("searchIndexResults() error = %v, want fault.ErrUsage", err)
		}
	})

	t.Run("SearchInstalledConfig", func(t *testing.T) {
		_, err := searchInstalledResults(cfg, "roles", "bogus:golang", nil)
		if !errors.Is(err, fault.ErrUsage) {
			t.Errorf("SearchInstalledConfig() error = %v, want fault.ErrUsage", err)
		}
	})
}

func TestExtractIndexEntryFromCUE(t *testing.T) {
	t.Parallel()

	cctx := cuecontext.New()

	tests := []struct {
		name            string
		cueStr          string
		wantDescription string
		wantTags        []string
		wantModule      string
	}{
		{
			name: "full entry",
			cueStr: `{
				description: "Go programming expert"
				tags: ["golang", "programming"]
				origin: "github.com/test/roles/golang@v0"
			}`,
			wantDescription: "Go programming expert",
			wantTags:        []string{"golang", "programming"},
			wantModule:      "github.com/test/roles/golang@v0",
		},
		{
			name: "description only",
			cueStr: `{
				description: "Simple entry"
			}`,
			wantDescription: "Simple entry",
			wantTags:        nil,
			wantModule:      "",
		},
		{
			name: "empty struct",
			cueStr: `{
				prompt: "some prompt"
			}`,
			wantDescription: "",
			wantTags:        nil,
			wantModule:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := cctx.CompileString(tt.cueStr)
			entry := extractIndexEntryFromCUE(v)

			if entry.Description != tt.wantDescription {
				t.Errorf("Description = %q, want %q", entry.Description, tt.wantDescription)
			}

			if len(entry.Tags) != len(tt.wantTags) {
				t.Errorf("Tags = %v, want %v", entry.Tags, tt.wantTags)
			} else {
				for i, tag := range entry.Tags {
					if tag != tt.wantTags[i] {
						t.Errorf("Tags[%d] = %q, want %q", i, tag, tt.wantTags[i])
					}
				}
			}

			if entry.Module != tt.wantModule {
				t.Errorf("Module = %q, want %q", entry.Module, tt.wantModule)
			}
		})
	}
}

func TestValidateSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		terms   []string
		tags    []string
		wantErr bool
	}{
		{"no tags no query", nil, nil, true},
		{"no tags short query", []string{"ab"}, nil, true},
		{"no tags valid query", []string{"abc"}, nil, false},
		{"tags no query", nil, []string{"golang"}, false},
		{"tags short query", []string{"ab"}, []string{"golang"}, true},
		{"tags valid query", []string{"abc"}, []string{"golang"}, false},
		{"no tags multi-term short total", []string{"a", "b"}, nil, true},
		{"no tags multi-term valid total", []string{"ab", "c"}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSearchQuery(tt.terms, tt.tags)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSearchQuery(%v, %v) error = %v, wantErr %v", tt.terms, tt.tags, err, tt.wantErr)
			}
		})
	}
}

func TestMatchesAnyTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entryTags  []string
		filterTags []string
		want       bool
	}{
		{"exact match", []string{"golang", "ai"}, []string{"golang"}, true},
		{"case insensitive", []string{"Golang", "AI"}, []string{"golang"}, true},
		{"no match", []string{"golang", "ai"}, []string{"python"}, false},
		{"multiple filter tags OR", []string{"golang"}, []string{"python", "golang"}, true},
		{"empty entry tags", nil, []string{"golang"}, false},
		{"empty filter tags", []string{"golang"}, nil, false},
		{"both empty", nil, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAnyTag(tt.entryTags, tt.filterTags)
			if got != tt.want {
				t.Errorf("matchesAnyTag(%v, %v) = %v, want %v", tt.entryTags, tt.filterTags, got, tt.want)
			}
		})
	}
}

func TestSearchIndex_TagFiltering(t *testing.T) {
	t.Parallel()

	index := &registry.Index{
		Agents: map[string]registry.IndexEntry{
			"ai/claude": {
				Module:      "github.com/test/agents/ai/claude@v0",
				Description: "Anthropic Claude AI",
				Tags:        []string{"ai", "llm"},
			},
		},
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {
				Module:      "github.com/test/roles/golang/assistant@v0",
				Description: "Go programming expert",
				Tags:        []string{"golang", "programming"},
			},
			"golang/code-review": {
				Module:      "github.com/test/roles/golang/code-review@v0",
				Description: "Review Go code for quality",
				Tags:        []string{"golang", "review"},
			},
			"python/assistant": {
				Module:      "github.com/test/roles/python/assistant@v0",
				Description: "Python programming expert",
				Tags:        []string{"python", "programming"},
			},
		},
		Tasks: map[string]registry.IndexEntry{
			"start/commit": {
				Module:      "github.com/test/tasks/start/commit@v0",
				Description: "Create git commit",
				Tags:        []string{"git", "commit"},
			},
		},
	}

	t.Run("tag only filtering", func(t *testing.T) {
		results, err := searchIndexResults(index, "", []string{"golang"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results for tag 'golang', got %d", len(results))
		}
		for _, r := range results {
			if r.Category != "roles" {
				t.Errorf("expected category 'roles', got %q", r.Category)
			}
		}
	})

	t.Run("multiple tags OR semantics", func(t *testing.T) {
		results, err := searchIndexResults(index, "", []string{"ai", "git"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results for tags 'ai,git', got %d", len(results))
		}
	})

	t.Run("tags combined with query", func(t *testing.T) {
		results, err := searchIndexResults(index, "programming", []string{"golang"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 result for query 'programming' + tag 'golang', got %d", len(results))
		}
		if len(results) > 0 && results[0].Name != "golang/assistant" {
			t.Errorf("expected 'golang/assistant', got %q", results[0].Name)
		}
	})

	t.Run("case insensitive tag matching", func(t *testing.T) {
		results, err := searchIndexResults(index, "", []string{"GOLANG"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results for tag 'GOLANG', got %d", len(results))
		}
	})

	t.Run("no matching tags returns empty", func(t *testing.T) {
		results, err := searchIndexResults(index, "", []string{"nonexistent"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for tag 'nonexistent', got %d", len(results))
		}
	})

	t.Run("query with non-matching tag returns empty", func(t *testing.T) {
		results, err := searchIndexResults(index, "golang", []string{"ai"})
		if err != nil {
			t.Fatalf("searchIndexResults() error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for query 'golang' + tag 'ai', got %d", len(results))
		}
	})
}

func TestSearchInstalledConfig_TagFiltering(t *testing.T) {
	t.Parallel()

	cctx := cuecontext.New()
	cfg := cctx.CompileString(`{
		agents: {
			claude: {
				description: "Anthropic Claude AI assistant"
				tags: ["ai", "llm"]
			}
			gemini: {
				description: "Google Gemini"
				tags: ["ai", "google"]
			}
		}
	}`)

	t.Run("tag only", func(t *testing.T) {
		results, err := searchInstalledResults(cfg, "agents", "", []string{"ai"})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results for tag 'ai', got %d", len(results))
		}
	})

	t.Run("tag narrows query", func(t *testing.T) {
		results, err := searchInstalledResults(cfg, "agents", "ai", []string{"google"})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if len(results) > 0 && results[0].Name != "gemini" {
			t.Errorf("expected 'gemini', got %q", results[0].Name)
		}
	})
}
