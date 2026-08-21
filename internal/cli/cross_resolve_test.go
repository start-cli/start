package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/registry"
)

func TestResolveCross_ZeroMatches(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)
	r := newTestResolver(cfg)
	_, err := r.resolveCross("nonexistent")
	if err == nil {
		t.Fatal("expected error for zero matches")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want containing 'not found'", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

func TestResolveCross_SingleInstalledExact(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)
	r := newTestResolver(cfg)
	outcome, err := r.resolveCross("claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := outcome.match
	if m.Name != "claude" || m.Category != "agents" || m.Source != ModuleSourceInstalled {
		t.Errorf("match = %+v, want claude/agents/installed", m)
	}
	if r.didInstall {
		t.Error("didInstall should be false for an installed match")
	}
}

func TestResolveCross_AmbiguousSubstringNonTTY(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		tasks: {
			"review/debug": { prompt: "Review debug" }
			"golang/debug": { prompt: "Debug Go code" }
		}
	}`)

	r := newTestResolver(cfg)
	_, err := r.resolveCross("debug")
	if err == nil {
		t.Fatal("expected ambiguity error for non-TTY")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want containing 'ambiguous'", err.Error())
	}
}

func TestResolveCross_SingleInstalledSubstring(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: { "gemini-non-interactive": { description: "Google Gemini agent", bin: "gemini", command: "{{.bin}}" } }
	}`)

	r := newTestResolver(cfg)
	outcome, err := r.resolveCross("gemini")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.match.Name != "gemini-non-interactive" || outcome.match.Category != "agents" {
		t.Errorf("match = %+v, want gemini-non-interactive/agents", outcome.match)
	}
}

func TestResolveCross_CombinedSearchMultipleNonTTY(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			"claude-code": { description: "Claude for coding", bin: "claude", command: "{{.bin}}" }
			"claude-chat": { description: "Claude for chatting", bin: "claude", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	_, err := r.resolveCross("claude")
	if err == nil {
		t.Fatal("expected ambiguity error for multiple matches in non-TTY")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want containing 'ambiguous'", err.Error())
	}
}

// TestResolveCross_ExactWinsOverSubstring asserts the inverted rule: an exact
// whole name that is also a substring of longer names resolves directly, no menu.
func TestResolveCross_ExactWinsOverSubstring(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: {
			claude: { bin: "claude", command: "{{.bin}}" }
			"claude-code": { description: "Claude for coding", bin: "claude", command: "{{.bin}}" }
			"claude-chat": { description: "Claude for chatting", bin: "claude", command: "{{.bin}}" }
		}
	}`)

	r := newTestResolver(cfg)
	outcome, err := r.resolveCross("claude")
	if err != nil {
		t.Fatalf("exact match must win, got: %v", err)
	}
	if outcome.match.Name != "claude" || outcome.match.Category != "agents" {
		t.Errorf("match = %+v, want claude/agents", outcome.match)
	}
}

// TestResolveCross_ExactWinsOverCrossCategorySubstring asserts a lone exact in
// one category wins over a substring neighbour in another category.
func TestResolveCross_ExactWinsOverCrossCategorySubstring(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: { claude: { bin: "claude", command: "{{.bin}}" } }
		roles: { "claude-expert": { prompt: "Claude expert role" } }
	}`)

	r := newTestResolver(cfg)
	outcome, err := r.resolveCross("claude")
	if err != nil {
		t.Fatalf("exact agents:claude must win over substring roles:claude-expert, got: %v", err)
	}
	if outcome.match.Name != "claude" || outcome.match.Category != "agents" {
		t.Errorf("match = %+v, want claude/agents", outcome.match)
	}
}

// TestResolveCross_MultipleExactAcrossCategoriesNonTTY asserts a bare name that
// is an exact match in two categories is a genuine ambiguity.
func TestResolveCross_MultipleExactAcrossCategoriesNonTTY(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: { foo: { bin: "foo", command: "{{.bin}}" } }
		roles: { foo: { prompt: "Foo role" } }
	}`)

	r := newTestResolver(cfg)
	_, err := r.resolveCross("foo")
	if err == nil {
		t.Fatal("expected ambiguity error for exact matches across categories")
	}
	for _, want := range []string{"ambiguous", "agents:foo", "roles:foo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q: %v", want, err)
		}
	}
	if r.didInstall {
		t.Error("didInstall should remain false when ambiguous")
	}
}

// TestResolveCross_AmbiguousAcrossCategories asserts substring matches spanning
// categories produce one list shown as category:name.
func TestResolveCross_AmbiguousAcrossCategories(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		tasks: {
			"review/debug": {prompt: "Review debug"}
			"golang/debug": {prompt: "Debug Go code"}
		}
		roles: {
			"frontend/debug": {prompt: "Frontend debugger"}
			"backend/debug": {prompt: "Backend debugger"}
		}
	}`)

	r := newTestResolver(cfg)
	_, err := r.resolveCross("debug")
	if err == nil {
		t.Fatal("expected ambiguity error spanning categories")
	}
	for _, want := range []string{"ambiguous", "tasks:", "roles:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q: %v", want, err)
		}
	}
}

func TestResolveCross_PrefixMatching(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: { foo: { bin: "foo", command: "{{.bin}}" } }
		roles: { foo: { prompt: "Foo role" } }
	}`)

	r := newTestResolver(cfg)
	outcome, err := r.resolveCross("agents:foo")
	if err != nil {
		t.Fatalf("unexpected error with matching prefix: %v", err)
	}
	if outcome.match.Category != "agents" || outcome.match.Name != "foo" {
		t.Errorf("match = %+v, want foo/agents", outcome.match)
	}
}

func TestResolveCross_PrefixScopesAbsence(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ roles: { "golang/assistant": { prompt: "Go assistant" } } }`)
	r := newTestResolver(cfg)
	_, err := r.resolveCross("contexts:golang/assistant")
	if err == nil {
		t.Fatal("expected 'not found' when the prefix scopes away the only candidate")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found' (no fallback to roles)", err.Error())
	}
}

func TestResolveCross_UnknownPrefix(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	_, err := r.resolveCross("foo:bar")
	if err == nil {
		t.Fatal("expected unknown-category error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown category") || !strings.Contains(msg, `"foo"`) {
		t.Errorf("error should name the unknown category, got: %v", err)
	}
	for _, want := range []string{"agents", "roles", "contexts", "tasks", "skills"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should list valid category %q, got: %v", want, err)
		}
	}
}

// TestResolveCross_AmbiguityRoundTrip feeds each listed candidate back and
// confirms it resolves to one entry — the round-trip property of the address scheme.
func TestResolveCross_AmbiguityRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{
		agents: { foo: { bin: "foo", command: "{{.bin}}" } }
		roles: { foo: { prompt: "Foo role" } }
	}`)

	r := newTestResolver(cfg)
	if _, err := r.resolveCross("foo"); err == nil {
		t.Fatal("expected ambiguity error for cross-category exact matches")
	}
	for _, candidate := range []string{"agents:foo", "roles:foo"} {
		r2 := newTestResolver(cfg)
		outcome, err := r2.resolveCross(candidate)
		if err != nil {
			t.Fatalf("round-trip resolve(%q) error: %v", candidate, err)
		}
		if got := formatAddress(outcome.match.Category, outcome.match.Name); got != candidate {
			t.Errorf("round-trip resolve(%q) returned %q", candidate, got)
		}
	}
}

// TestResolveCross_FilePathBypass verifies a filesystem path bypasses the search
// and is returned for the caller to read directly.
func TestResolveCross_FilePathBypass(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	for _, path := range []string{"./doc.md", "/tmp/doc.md", "~/doc.md"} {
		outcome, err := r.resolveCross(path)
		if err != nil {
			t.Fatalf("resolveCross(%q) error = %v", path, err)
		}
		if outcome.locator != path {
			t.Errorf("resolveCross(%q).locator = %q, want %q", path, outcome.locator, path)
		}
	}
}

// TestResolveCross_RegistryExactInstalls verifies a registry-only exact reaches
// the install path (which fails because the client is nil, proving the path ran).
func TestResolveCross_RegistryExactInstalls(t *testing.T) {
	t.Parallel()

	r := newResolverWithIndex(buildTestCfg(t, `{roles: {}}`), &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {Module: "github.com/test/roles/golang/assistant@v0"},
		},
	})

	_, err := r.resolveCross("golang/assistant")
	if err == nil {
		t.Fatal("expected error from install attempt in exact-registry branch")
	}
	if !strings.Contains(err.Error(), "registry client unavailable") {
		t.Errorf("error = %q, want install path reached", err.Error())
	}
}

// TestResolveCross_CombinedSingleRegistryInstalls forces the substring
// single-registry branch into the install path.
func TestResolveCross_CombinedSingleRegistryInstalls(t *testing.T) {
	t.Parallel()

	r := newResolverWithIndex(buildTestCfg(t, `{roles: {}}`), &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"golang/assistant": {Module: "github.com/test/roles/golang/assistant@v0", Tags: []string{"golang"}},
		},
	})

	// "golang" is a name substring (not an exact whole name), so it reaches the
	// fallback tier and the single registry match installs.
	_, err := r.resolveCross("golang")
	if err == nil {
		t.Fatal("expected error from install attempt in combined-search branch")
	}
	if !strings.Contains(err.Error(), "registry client unavailable") {
		t.Errorf("error = %q, want install path reached", err.Error())
	}
}

// TestResolveCross_CombinedMultipleRegistryNonTTY surfaces multiple registry
// substring matches as an ambiguity without attempting an install.
func TestResolveCross_CombinedMultipleRegistryNonTTY(t *testing.T) {
	t.Parallel()

	r := newResolverWithIndex(buildTestCfg(t, `{roles: {}}`), &registry.Index{
		Roles: map[string]registry.IndexEntry{
			"frontend/debugger": {Module: "github.com/test/roles/frontend/debugger@v0"},
			"backend/debugger":  {Module: "github.com/test/roles/backend/debugger@v0"},
		},
	})

	_, err := r.resolveCross("debugger")
	if err == nil {
		t.Fatal("expected ambiguity error for multiple registry matches in non-TTY")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want containing 'ambiguous'", err.Error())
	}
	if r.didInstall {
		t.Error("didInstall should remain false — no install when ambiguous")
	}
}

// TestResolveCross_Certainty asserts get/describe apply the same certainty split
// the previously divergent path lacked (it discarded the index error).
func TestResolveCross_Certainty(t *testing.T) {
	t.Parallel()

	cfg := buildTestCfg(t, `{ agents: { claude: { bin: "claude", command: "{{.bin}}" } } }`)

	t.Run("reachable index, no match -> not found", func(t *testing.T) {
		r := newResolverWithIndex(cfg, &registry.Index{})
		_, err := r.resolveCross("missing-module")
		if got := ExitCodeFromError(err); got != ExitNotFound {
			t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
		}
	})

	t.Run("unreachable index, no match -> transient", func(t *testing.T) {
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		_, err := r.resolveCross("missing-module")
		if got := ExitCodeFromError(err); got != ExitTransient {
			t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
		}
	})

	t.Run("installed name resolves despite unreachable index", func(t *testing.T) {
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		outcome, err := r.resolveCross("claude")
		if err != nil {
			t.Fatalf("installed exact must resolve offline, got: %v", err)
		}
		if outcome.match.Name != "claude" {
			t.Errorf("match = %+v, want claude", outcome.match)
		}
	})
}

func TestResolveCrossNoInstall_SkillLeaf(t *testing.T) {
	t.Parallel()

	entry := func(name string) registry.IndexEntry {
		return registry.IndexEntry{Module: "github.com/test/skills/" + name + "@v0"}
	}

	t.Run("prefix miss then unique dest leaf", func(t *testing.T) {
		r := newResolverWithIndex(buildTestCfg(t, `{}`), &registry.Index{
			Skills: map[string]registry.IndexEntry{
				"finding/one-by-one": entry("finding/one-by-one"),
			},
		})
		outcome, err := r.resolveCrossNoInstall("skills:one-by-one")
		if err != nil {
			t.Fatalf("unique dest leaf: %v", err)
		}
		if outcome.match.Name != "finding/one-by-one" || outcome.match.Category != "skills" {
			t.Errorf("match = %+v, want finding/one-by-one/skills", outcome.match)
		}
		if outcome.match.Entry.Module != "github.com/test/skills/finding/one-by-one@v0" {
			t.Errorf("registry dest leaf dropped module path: %+v", outcome.match.Entry)
		}
	})

	t.Run("unique dest leaf does not hide prefix ambiguity", func(t *testing.T) {
		r := newResolverWithIndex(buildTestCfg(t, `{}`), &registry.Index{
			Skills: map[string]registry.IndexEntry{
				"one-by-one-extra":   entry("one-by-one-extra"),
				"one-by-one-helper":  entry("one-by-one-helper"),
				"finding/one-by-one": entry("finding/one-by-one"),
			},
		})
		_, err := r.resolveCrossNoInstall("skills:one-by-one")
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("want prefix ambiguity, got %v", err)
		}
	})

	t.Run("installed dest leaf resolves when registry is down", func(t *testing.T) {
		cfg := buildTestCfg(t, `{ skills: { "finding/one-by-one": { origin: "github.com/example/x@v1", version: "v1.0.0" } } }`)
		r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
		r.didFetch = true
		r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
		outcome, err := r.resolveCrossNoInstall("skills:one-by-one")
		if err != nil {
			t.Fatalf("installed dest leaf must resolve offline, got: %v", err)
		}
		if outcome.match.Name != "finding/one-by-one" {
			t.Errorf("match = %+v, want finding/one-by-one", outcome.match)
		}
		if outcome.match.Entry.Module != "github.com/example/x@v1" || outcome.match.Entry.Version != "v1.0.0" {
			t.Errorf("installed dest leaf dropped origin: %+v", outcome.match.Entry)
		}
	})
}

func TestInstallIfRegistry_InstalledIsNoop(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	if err := r.installIfRegistry(ModuleMatch{Name: "foo", Category: "roles", Source: ModuleSourceInstalled}); err != nil {
		t.Fatalf("installIfRegistry(installed) error = %v, want nil", err)
	}
	if r.didInstall {
		t.Error("didInstall should remain false for installed match")
	}
}

func TestInstallIfRegistry_RegistryWithoutClient(t *testing.T) {
	t.Parallel()

	r := newTestResolver(buildTestCfg(t, `{}`))
	err := r.installIfRegistry(ModuleMatch{Name: "foo", Category: "roles", Source: ModuleSourceRegistry})
	if err == nil {
		t.Fatal("expected error when installing registry match without client")
	}
	if !strings.Contains(err.Error(), "registry client unavailable") {
		t.Errorf("error = %q, want containing 'registry client unavailable'", err.Error())
	}
	if r.didInstall {
		t.Error("didInstall should remain false when install fails")
	}
}

func TestParseAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input        string
		wantCategory string
		wantName     string
		wantPrefix   bool
		wantErr      bool
	}{
		{"claude", "", "claude", false, false},
		{"claude/interactive", "", "claude/interactive", false, false},
		{"agents:claude", "agents", "claude", true, false},
		{"agents:claude/interactive", "agents", "claude/interactive", true, false},
		{"contexts:cwd/agents-md", "contexts", "cwd/agents-md", true, false},
		{"skills:workflows/one-by-one", "skills", "workflows/one-by-one", true, false},
		{"foo:bar", "", "", false, true},
		{"agent:claude", "", "", false, true}, // singular typo, not in valid set
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			addr, err := parseAddress(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseAddress(%q) expected error, got %+v", tt.input, addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAddress(%q) unexpected error: %v", tt.input, err)
			}
			if addr.Category != tt.wantCategory {
				t.Errorf("Category = %q, want %q", addr.Category, tt.wantCategory)
			}
			if addr.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", addr.Name, tt.wantName)
			}
			if addr.HasPrefix != tt.wantPrefix {
				t.Errorf("HasPrefix = %v, want %v", addr.HasPrefix, tt.wantPrefix)
			}
		})
	}
}
