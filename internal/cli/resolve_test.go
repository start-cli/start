package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"cuelang.org/go/cue/cuecontext"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/registry"
)

func buildTestCfg(t *testing.T, cueStr string) internalcue.LoadResult {
	t.Helper()
	cctx := cuecontext.New()
	v := cctx.CompileString(cueStr)
	if v.Err() != nil {
		t.Fatalf("compiling test CUE: %v", v.Err())
	}
	return internalcue.LoadResult{Value: v}
}

// newTestResolver creates a resolver whose index source returns a nil index, so
// resolution runs offline against installed config only. With no index fetched
// and no index error recorded, an unmatched name resolves to not-found (the
// index-reachable certainty branch).
func newTestResolver(cfg internalcue.LoadResult) *resolver {
	r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
	r.indexSrc = offlineIndexSource{}
	return r
}

// injectedIndexSource returns a pre-loaded index offline, ignoring the live-vs-
// cache-gated decision, so an injected index resolves without a live pull
// regardless of wantLive.
type injectedIndexSource struct {
	index  *registry.Index
	client registry.Client
}

func (s injectedIndexSource) fetch(context.Context, bool) (*registry.Index, registry.Client, error) {
	return s.index, s.client, nil
}

// newResolverWithIndex injects a pre-fetched index so resolution runs offline
// against installed config plus the supplied registry entries.
func newResolverWithIndex(cfg internalcue.LoadResult, index *registry.Index) *resolver {
	r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
	r.indexSrc = injectedIndexSource{index: index}
	return r
}

func TestNameMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query, candidate string
		mode             matchMode
		want             bool
	}{
		{"review", "review", modeExact, true},
		{"Review", "review", modeExact, true}, // case-insensitive
		{"review", "start/review", modeExact, false},
		{"review", "start/review", modeSubstring, true},
		{"foo/bar", "foofoo/barbar", modeSubstring, true}, // slash is literal
		{"review", "jira/item/review", modePrefix, false},
		{"jira", "jira/item/review", modePrefix, true},
		{"jira", "JIRA/item/review", modePrefix, true},
	}
	for _, tt := range tests {
		if got := nameMatches(tt.query, tt.candidate, tt.mode); got != tt.want {
			t.Errorf("nameMatches(%q, %q, %v) = %v, want %v", tt.query, tt.candidate, tt.mode, got, tt.want)
		}
	}
}

func TestMergeMatches(t *testing.T) {
	t.Parallel()

	installed := []ModuleMatch{
		{Name: "claude", Category: "agents", Source: ModuleSourceInstalled},
		{Name: "gemini", Category: "agents", Source: ModuleSourceInstalled},
	}
	reg := []ModuleMatch{
		{Name: "claude", Category: "agents", Source: ModuleSourceRegistry}, // dup, installed wins
		{Name: "openai", Category: "agents", Source: ModuleSourceRegistry},
	}

	merged := mergeMatches(installed, reg)
	if len(merged) != 3 {
		t.Fatalf("mergeMatches returned %d, want 3 (claude de-duplicated)", len(merged))
	}
	// Installed entries sort before registry entries.
	if merged[0].Source != ModuleSourceInstalled || merged[1].Source != ModuleSourceInstalled {
		t.Errorf("installed matches should sort first, got %+v", merged)
	}
	if merged[2].Name != "openai" || merged[2].Source != ModuleSourceRegistry {
		t.Errorf("registry-only match should sort last, got %+v", merged[2])
	}
	for _, m := range merged {
		if m.Name == "claude" && m.Source != ModuleSourceInstalled {
			t.Errorf("claude should be from installed, got %q", m.Source)
		}
	}
}

func TestMergeMatches_Empty(t *testing.T) {
	t.Parallel()
	if got := mergeMatches(nil, nil); len(got) != 0 {
		t.Errorf("expected 0 results for empty inputs, got %d", len(got))
	}
}

// TestMatchByTypedName_CrossCategoryTwin verifies the typed-selection precedence
// on a cross-category menu: a full category:name label disambiguates twins,
// while a bare name shared by two twins is rejected as ambiguous rather than
// silently resolving the first.
func TestMatchByTypedName_CrossCategoryTwin(t *testing.T) {
	t.Parallel()
	scope := crossCategoryScope()
	shown := []ModuleMatch{
		{Name: "claude", Category: "agents", Source: ModuleSourceInstalled},
		{Name: "claude", Category: "roles", Source: ModuleSourceInstalled},
	}

	if _, err := matchByTypedName(shown, scope, "claude"); err == nil {
		t.Error("bare name matching two cross-category twins should be ambiguous, not silently resolved")
	}

	m, err := matchByTypedName(shown, scope, "agents:claude")
	if err != nil {
		t.Fatalf("exact label should resolve, got: %v", err)
	}
	if m.Category != "agents" || m.Name != "claude" {
		t.Errorf("matchByTypedName = %+v, want agents:claude", m)
	}
}

// TestMatchByTypedName_UniqueBareNameWinsOverSubstring verifies an exact bare
// name that is unique among the shown entries beats a substring match.
func TestMatchByTypedName_UniqueBareNameWinsOverSubstring(t *testing.T) {
	t.Parallel()
	scope := crossCategoryScope()
	shown := []ModuleMatch{
		{Name: "claude", Category: "agents", Source: ModuleSourceInstalled},
		{Name: "claude-expert", Category: "roles", Source: ModuleSourceInstalled},
	}

	m, err := matchByTypedName(shown, scope, "claude")
	if err != nil {
		t.Fatalf("unique exact bare name should win over substring, got: %v", err)
	}
	if m.Name != "claude" || m.Category != "agents" {
		t.Errorf("matchByTypedName = %+v, want agents:claude", m)
	}
}

// TestResolve_ShortNameFloorCertainty asserts the floor's certainty split: a
// short, unmatched name is a usage error when the index is reachable (absence
// confirmed), and a transient error when it is unreachable (absence cannot be
// confirmed), mirroring the fallback tier.
func TestResolve_ShortNameFloorCertainty(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)

	t.Run("reachable index -> usage", func(t *testing.T) {
		r := newResolverWithIndex(cfg, &registry.Index{})
		_, err := r.resolve("xy", crossCategoryScope())
		if got := ExitCodeFromError(err); got != ExitUsage {
			t.Errorf("exit code = %d, want %d (usage)", got, ExitUsage)
		}
	})

	t.Run("unreachable index -> transient", func(t *testing.T) {
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		_, err := r.resolve("xy", crossCategoryScope())
		if got := ExitCodeFromError(err); got != ExitTransient {
			t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
		}
	})
}

func TestResolveAgent_ExactMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			claude: { bin: "claude", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	name, err := r.resolveAgent("claude")
	if err != nil {
		t.Fatalf("resolveAgent() error = %v", err)
	}
	if name != "claude" {
		t.Errorf("resolveAgent() = %q, want %q", name, "claude")
	}
}

func TestResolveAgent_SubstringMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			"gemini-non-interactive": { description: "Google Gemini agent", bin: "gemini", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	name, err := r.resolveAgent("gemini")
	if err != nil {
		t.Fatalf("resolveAgent() error = %v", err)
	}
	if name != "gemini-non-interactive" {
		t.Errorf("resolveAgent() = %q, want %q", name, "gemini-non-interactive")
	}
}

func TestResolveAgent_NoMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)
	r := newTestResolver(cfg)
	_, err := r.resolveAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for no match")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want containing 'not found'", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

func TestResolveAgent_MultipleMatches_NonTTY(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			"claude-code": { description: "Claude for coding", bin: "claude", command: "{{.bin}}" }
			"claude-chat": { description: "Claude for chatting", bin: "claude", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	_, err := r.resolveAgent("claude")
	if err == nil {
		t.Fatal("expected error for multiple matches in non-TTY")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want containing 'ambiguous'", err.Error())
	}
	for _, want := range []string{"claude-code", "claude-chat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list %q: %v", want, err)
		}
	}
	if got := ExitCodeFromError(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d (usage)", got, ExitUsage)
	}
}

func TestResolveAgent_Empty(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{agents: {}}`))
	name, err := r.resolveAgent("")
	if err != nil {
		t.Fatalf("resolveAgent('') error = %v", err)
	}
	if name != "" {
		t.Errorf("resolveAgent('') = %q, want empty", name)
	}
}

// TestResolveAgent_LocatorRejected verifies --agent rejects a locator — a
// filesystem path or an http(s) URL: an agent is a structured configuration, not
// a document body.
func TestResolveAgent_LocatorRejected(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{agents: {}}`))
	locators := []string{
		"./agent.cue", "/tmp/agent.cue", "~/agent.cue",
		"https://example.com/agent.cue", "http://example.com/agent.cue",
	}
	for _, loc := range locators {
		_, err := r.resolveAgent(loc)
		if err == nil {
			t.Fatalf("resolveAgent(%q) expected error for locator", loc)
		}
		if got := ExitCodeFromError(err); got != ExitUsage {
			t.Errorf("resolveAgent(%q) exit code = %d, want %d (usage)", loc, got, ExitUsage)
		}
	}
}

func TestResolveAgent_PrefixMatching(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)
	r := newTestResolver(cfg)
	got, err := r.resolveAgent("agents:claude")
	if err != nil {
		t.Fatalf("resolveAgent(agents:claude) error: %v", err)
	}
	if got != "claude" {
		t.Errorf("resolveAgent = %q, want %q", got, "claude")
	}
}

func TestResolveAgent_PrefixMismatchError(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	_, err := r.resolveAgent("roles:assistant")
	if err == nil {
		t.Fatal("expected mismatch error from --agent receiving a roles: address")
	}
	if msg := err.Error(); !strings.Contains(msg, "agents") || !strings.Contains(msg, "roles") {
		t.Errorf("error should name both expected and given category, got: %v", err)
	}
	if got := ExitCodeFromError(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d (usage)", got, ExitUsage)
	}
}

func TestResolveAgent_UnknownPrefixError(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	_, err := r.resolveAgent("foo:bar")
	if err == nil {
		t.Fatal("expected unknown-category error")
	}
	if !strings.Contains(err.Error(), "unknown category") {
		t.Errorf("error should mention 'unknown category', got: %v", err)
	}
	if got := ExitCodeFromError(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d (usage)", got, ExitUsage)
	}
}

func TestResolveRole_ExactMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: { "golang/assistant": { prompt: "You are a Go expert" } } }`)
	r := newTestResolver(cfg)
	name, err := r.resolveRole("golang/assistant")
	if err != nil {
		t.Fatalf("resolveRole() error = %v", err)
	}
	if name != "golang/assistant" {
		t.Errorf("resolveRole() = %q, want %q", name, "golang/assistant")
	}
}

func TestResolveRole_FilePathBypass(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{roles: {}}`))
	for _, path := range []string{"./my-role.md", "/tmp/role.md", "~/roles/test.md"} {
		t.Run(path, func(t *testing.T) {
			name, err := r.resolveRole(path)
			if err != nil {
				t.Fatalf("resolveRole(%q) error = %v", path, err)
			}
			if name != path {
				t.Errorf("resolveRole(%q) = %q, want %q", path, name, path)
			}
		})
	}
}

// TestResolveRole_SubstringMatch verifies a former "short name" now resolves by
// substring fallback: "assistant" matches "golang/assistant".
func TestResolveRole_SubstringMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: { "golang/assistant": { prompt: "You are a Go expert" } } }`)
	r := newTestResolver(cfg)
	name, err := r.resolveRole("assistant")
	if err != nil {
		t.Fatalf("resolveRole() error = %v", err)
	}
	if name != "golang/assistant" {
		t.Errorf("resolveRole() = %q, want %q", name, "golang/assistant")
	}
}

// TestResolveModule_ExactWinsOverSubstring asserts the exact-match-wins rule: an
// exact whole name that is also a substring of longer names resolves directly,
// even without a TTY, with no menu.
func TestResolveModule_ExactWinsOverSubstring(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		roles: {
			"review": { prompt: "General review" }
			"start/review": { prompt: "Start review" }
			"gitlab/pipeline/review": { prompt: "Pipeline review" }
		}
	}`)

	r := newTestResolver(cfg)
	name, err := r.resolveRole("review")
	if err != nil {
		t.Fatalf("resolveRole(review) error = %v (exact match must win)", err)
	}
	if name != "review" {
		t.Errorf("resolveRole(review) = %q, want %q", name, "review")
	}
}

// TestResolveModule_ExactWinsAgent verifies the same for --agent.
func TestResolveModule_ExactWinsAgent(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			"claude": { bin: "claude", command: "{{.bin}}" }
			"claude-code": { bin: "claude", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	name, err := r.resolveAgent("claude")
	if err != nil {
		t.Fatalf("resolveAgent(claude) error = %v (exact match must win)", err)
	}
	if name != "claude" {
		t.Errorf("resolveAgent(claude) = %q, want %q", name, "claude")
	}
}

// TestResolveModule_ThreeCharFloor verifies the floor rejects a short fallback
// query while the exact tier remains exempt.
func TestResolveModule_ThreeCharFloor(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		roles: {
			"ci": { prompt: "CI role" }
			"review": { prompt: "Review role" }
		}
	}`)

	r := newTestResolver(cfg)

	// "rv" is not an exact name and is under three characters -> rejected.
	if _, err := r.resolveRole("rv"); err == nil {
		t.Error("resolveRole(rv) expected too-short error")
	} else if got := ExitCodeFromError(err); got != ExitUsage {
		t.Errorf("resolveRole(rv) exit code = %d, want %d (usage)", got, ExitUsage)
	}

	// "ci" is a complete name, matched by the exact tier despite being two chars.
	name, err := r.resolveRole("ci")
	if err != nil {
		t.Fatalf("resolveRole(ci) error = %v (exact tier is floor-exempt)", err)
	}
	if name != "ci" {
		t.Errorf("resolveRole(ci) = %q, want %q", name, "ci")
	}

	// "roles:ci" resolves via the exact tier despite the two-character name.
	name, err = r.resolveRole("roles:ci")
	if err != nil {
		t.Fatalf("resolveRole(roles:ci) error = %v", err)
	}
	if name != "ci" {
		t.Errorf("resolveRole(roles:ci) = %q, want %q", name, "ci")
	}
}

// TestResolveModule_PrefixVsSubstring distinguishes the qualified prefix mode
// from the bare substring mode: they return different sets.
func TestResolveModule_PrefixVsSubstring(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		roles: {
			"review/code": { prompt: "Review code" }
			"jira/item/review": { prompt: "Jira review" }
		}
	}`)

	// Bare "review" is a substring search: matches both, ambiguous (non-TTY).
	r := newTestResolver(cfg)
	if _, err := r.resolveRole("review"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("resolveRole(review) substring should be ambiguous, got: %v", err)
	}

	// "roles:review" is a prefix search: matches only review/code.
	r = newTestResolver(cfg)
	name, err := r.resolveRole("roles:review")
	if err != nil {
		t.Fatalf("resolveRole(roles:review) error = %v", err)
	}
	if name != "review/code" {
		t.Errorf("resolveRole(roles:review) = %q, want %q (prefix excludes jira/item/review)", name, "review/code")
	}
}

// TestResolveModule_NameOnly verifies resolution targets the name only: a query
// matching only a description or tag is not found.
func TestResolveModule_NameOnly(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		roles: {
			"golang/assistant": {
				description: "An expert debugger for tricky problems"
				tags: ["troubleshooting"]
				prompt: "You are a Go expert"
			}
		}
	}`)

	r := newTestResolver(cfg)
	for _, q := range []string{"debugger", "troubleshooting"} {
		if _, err := r.resolveRole(q); err == nil {
			t.Errorf("resolveRole(%q) should be not-found (name-only match), got nil", q)
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("resolveRole(%q) error = %v, want not-found", q, err)
		}
	}
}

// TestResolveModule_Certainty asserts the exit-code split: an uninstalled name
// is not-found when the index is reachable, transient when it is unreachable,
// and an installed name resolves regardless.
func TestResolveModule_Certainty(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: { "golang/assistant": { prompt: "Go" } } }`)

	t.Run("reachable index, no match -> not found", func(t *testing.T) {
		r := newResolverWithIndex(cfg, &registry.Index{}) // reachable but empty
		_, err := r.resolveRole("missing-role")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		if got := ExitCodeFromError(err); got != ExitNotFound {
			t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
		}
	})

	t.Run("unreachable index, no match -> transient", func(t *testing.T) {
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.index = nil
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		_, err := r.resolveRole("missing-role")
		if err == nil {
			t.Fatal("expected transient error")
		}
		if got := ExitCodeFromError(err); got != ExitTransient {
			t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
		}
	})

	t.Run("installed name resolves despite unreachable index", func(t *testing.T) {
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.index = nil
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		name, err := r.resolveRole("golang/assistant")
		if err != nil {
			t.Fatalf("installed exact must resolve offline, got: %v", err)
		}
		if name != "golang/assistant" {
			t.Errorf("resolveRole = %q, want %q", name, "golang/assistant")
		}
	})
}

// TestResolveModule_RegistryOnlyExactInstalls verifies a registry-only exact
// name reaches the install path (which fails here because the client is nil,
// proving the path executed) rather than being reported not-found.
func TestResolveModule_RegistryOnlyExactInstalls(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: {} }`)
	r := newResolverWithIndex(cfg, &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {Module: "github.com/test/roles/golang/assistant@v0"},
		},
	})

	_, err := r.resolveRole("golang/assistant")
	if err == nil {
		t.Fatal("expected install attempt for registry-only exact")
	}
	if !strings.Contains(err.Error(), "registry client unavailable") {
		t.Errorf("error = %q, want install path reached", err.Error())
	}
}

// TestResolveModule_InstalledExactSkipsRegistry verifies a category-specific
// surface resolves a lone installed exact without consulting the registry: the
// offline index source yields nothing, so the exact must come from installed
// config alone.
func TestResolveModule_InstalledExactSkipsRegistry(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: { "golang/assistant": { prompt: "Go" } } }`)
	r := newTestResolver(cfg)
	name, err := r.resolveRole("golang/assistant")
	if err != nil {
		t.Fatalf("resolveRole() error = %v", err)
	}
	if name != "golang/assistant" {
		t.Errorf("resolveRole() = %q, want %q", name, "golang/assistant")
	}
}

func TestResolveContexts_ExactName(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		contexts: {
			env: { required: true, prompt: "environment" }
			project: { default: true, prompt: "project info" }
		}
	}`)

	r := newTestResolver(cfg)
	resolved, err := r.resolveContexts([]string{"env"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "env" {
		t.Errorf("resolveContexts([env]) = %v, want [env]", resolved)
	}
}

func TestResolveContexts_FilePathBypass(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{contexts: {}}`))
	resolved, err := r.resolveContexts([]string{"./docs/guide.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "./docs/guide.md" {
		t.Errorf("resolveContexts([./docs/guide.md]) = %v, want [./docs/guide.md]", resolved)
	}
}

func TestResolveContexts_DefaultPassthrough(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{contexts: {}}`))
	resolved, err := r.resolveContexts([]string{"default"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "default" {
		t.Errorf("resolveContexts([default]) = %v, want [default]", resolved)
	}
}

func TestResolveContexts_SubstringMatch(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		contexts: {
			"golang-env": { description: "Go development environment", tags: ["golang"], prompt: "Go env context" }
		}
	}`)

	r := newTestResolver(cfg)
	resolved, err := r.resolveContexts([]string{"golang"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "golang-env" {
		t.Errorf("resolveContexts([golang]) = %v, want [golang-env]", resolved)
	}
}

// TestResolveContexts_NotFoundError verifies an explicit term that matches
// nothing is a not-found error (no pass-through).
func TestResolveContexts_NotFoundError(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ contexts: { env: { prompt: "environment" } } }`)
	r := newTestResolver(cfg)
	_, err := r.resolveContexts([]string{"nonexistent"})
	if err == nil {
		t.Fatal("expected not-found error for unresolved context term")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want containing 'not found'", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

// TestResolveContexts_NameOnly verifies a term matching only a tag is not found:
// the score threshold and multi-select behaviour are gone.
func TestResolveContexts_NameOnly(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		contexts: {
			env: { prompt: "basic environment" }
			"golang-env": { description: "Go development environment", tags: ["golang"], prompt: "Go env" }
		}
	}`)

	// "golang" matches the name "golang-env" only (the tag on golang-env is not a
	// resolution dimension), so it resolves to that single context.
	r := newTestResolver(cfg)
	resolved, err := r.resolveContexts([]string{"golang"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "golang-env" {
		t.Errorf("resolveContexts([golang]) = %v, want [golang-env]", resolved)
	}
}

func TestResolveContexts_Mixed(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		contexts: {
			env: { required: true, prompt: "environment" }
			"golang-env": { description: "Go development environment", tags: ["golang"], prompt: "Go env" }
		}
	}`)

	r := newTestResolver(cfg)
	resolved, err := r.resolveContexts([]string{"./custom.md", "default", "env"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"./custom.md", "default", "env"}
	if len(resolved) != len(want) {
		t.Fatalf("resolveContexts() returned %d items, want %d: %v", len(resolved), len(want), resolved)
	}
	for i, w := range want {
		if resolved[i] != w {
			t.Errorf("resolved[%d] = %q, want %q", i, resolved[i], w)
		}
	}
}

func TestResolveContexts_PrefixMatching(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ contexts: { "golang-env": { prompt: "Go environment", tags: ["env"] } } }`)
	r := newTestResolver(cfg)
	resolved, err := r.resolveContexts([]string{"contexts:golang-env"})
	if err != nil {
		t.Fatalf("resolveContexts(contexts:golang-env) error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "golang-env" {
		t.Errorf("resolved = %v, want [golang-env]", resolved)
	}
}

func TestResolveContexts_PrefixMismatchError(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	_, err := r.resolveContexts([]string{"agents:claude"})
	if err == nil {
		t.Fatal("expected mismatch error from -c receiving an agents: address")
	}
	if !strings.Contains(err.Error(), "contexts") {
		t.Errorf("error should name the expected category 'contexts', got: %v", err)
	}
}

// --- Model resolution is out of scope and keeps the search-style match. ---

func TestResolveModelName_ExactMatch(t *testing.T) {
	t.Parallel()

	agent := orchestration.Agent{Models: map[string]string{"sonnet": "claude-sonnet-4-5", "opus": "claude-opus-4-6"}}
	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("sonnet", agent); name != "sonnet" {
		t.Errorf("resolveModelName() = %q, want %q", name, "sonnet")
	}
}

func TestResolveModelName_SubstringMatch(t *testing.T) {
	t.Parallel()

	agent := orchestration.Agent{Models: map[string]string{"sonnet": "claude-sonnet-4-5", "opus": "claude-opus-4-6"}}
	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("son", agent); name != "sonnet" {
		t.Errorf("resolveModelName() = %q, want %q", name, "sonnet")
	}
}

func TestResolveModelName_Passthrough(t *testing.T) {
	t.Parallel()

	agent := orchestration.Agent{Models: map[string]string{"sonnet": "claude-sonnet-4-5"}}
	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("gpt-4o", agent); name != "gpt-4o" {
		t.Errorf("resolveModelName() = %q, want %q (passthrough)", name, "gpt-4o")
	}
}

func TestResolveModelName_MultipleMatches_Passthrough(t *testing.T) {
	t.Parallel()

	agent := orchestration.Agent{Models: map[string]string{"sonnet-4": "claude-sonnet-4", "sonnet-4-5": "claude-sonnet-4-5"}}
	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("sonnet", agent); name != "sonnet" {
		t.Errorf("resolveModelName() = %q, want %q (passthrough on multiple)", name, "sonnet")
	}
}

func TestResolveModelName_NilModels(t *testing.T) {
	t.Parallel()

	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("sonnet", orchestration.Agent{}); name != "sonnet" {
		t.Errorf("resolveModelName() = %q, want %q (passthrough)", name, "sonnet")
	}
}

func TestResolveModelName_Empty(t *testing.T) {
	t.Parallel()

	agent := orchestration.Agent{Models: map[string]string{"sonnet": "claude-sonnet"}}
	r := newTestResolver(internalcue.LoadResult{})
	if name := r.resolveModelName("", agent); name != "" {
		t.Errorf("resolveModelName('') = %q, want empty", name)
	}
}

// TestResolveModelName_ObjectFormAgent chains ExtractAgent with resolveModelName
// to prove --model resolution works against an object-form agent loaded from CUE.
func TestResolveModelName_ObjectFormAgent(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			objform: {
				bin: "objform"
				command: "{{.bin}} --model {{.model}}"
				default_model: "sonnet"
				models: { sonnet: { id: "obj-sonnet-id" }, opus: { id: "obj-opus-id" } }
			}
		}
	}`)

	agent, err := orchestration.ExtractAgent(cfg.Value, "objform")
	if err != nil {
		t.Fatalf("ExtractAgent: %v", err)
	}
	if got := agent.Models["sonnet"]; got != "obj-sonnet-id" {
		t.Errorf("Models[sonnet] = %q, want %q", got, "obj-sonnet-id")
	}

	r := newTestResolver(internalcue.LoadResult{})
	if got := r.resolveModelName("sonnet", agent); got != "sonnet" {
		t.Errorf("resolveModelName(\"sonnet\") = %q, want %q (exact)", got, "sonnet")
	}
	if got := r.resolveModelName("son", agent); got != "sonnet" {
		t.Errorf("resolveModelName(\"son\") = %q, want %q (substring)", got, "sonnet")
	}
}
