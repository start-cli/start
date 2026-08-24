package cli

import (
	"testing"
)

const surfaceTestCfg = `{
	agents: {claude: {bin: "claude", command: "{{.bin}} run"}}
	roles: {"go-expert": {prompt: "Go"}}
	contexts: {environment: {prompt: "env"}}
	tasks: {"review/pre-commit": {prompt: "review"}}
}`

func roleScope() resolveScope  { return singleCategoryScope("roles", "role", true) }
func ctxScope() resolveScope   { return singleCategoryScope("contexts", "context", true) }
func agentScope() resolveScope { return singleCategoryScope("agents", "agent", false) }
func taskScope() resolveScope  { return singleCategoryScope("tasks", "task", false) }

func TestComputeWantLive(t *testing.T) {
	t.Parallel()
	r := newTestResolver(buildTestCfg(t, surfaceTestCfg))

	tests := []struct {
		name     string
		surfaces []pendingSurface
		refresh  bool
		want     bool
	}{
		{
			name: "all installed or empty stays cache-gated",
			surfaces: []pendingSurface{
				{"", agentScope()},
				{"go-expert", roleScope()},
				{"environment", ctxScope()},
			},
			want: false,
		},
		{
			name:     "uninstalled role forces live",
			surfaces: []pendingSurface{{"missing-role", roleScope()}},
			want:     true,
		},
		{
			name:     "substring-installed role stays cache-gated",
			surfaces: []pendingSurface{{"expert", roleScope()}}, // matches go-expert
			want:     false,
		},
		{
			name:     "uninstalled task forces live",
			surfaces: []pendingSurface{{"unknown-task", taskScope()}},
			want:     true,
		},
		{
			name: "locator surface is excluded from the union",
			surfaces: []pendingSurface{
				{"./custom.md", roleScope()},
				{"environment", ctxScope()}, // installed
			},
			want: false,
		},
		{
			name: "context sentinels are excluded from the union",
			surfaces: []pendingSurface{
				{"default", ctxScope()},
				{"none", ctxScope()},
				{"environment", ctxScope()},
			},
			want: false,
		},
		{
			name: "any no-installed-match surface forces live regardless of order (uninstalled first)",
			surfaces: []pendingSurface{
				{"unknown-task", taskScope()},
				{"environment", ctxScope()},
			},
			want: true,
		},
		{
			name: "any no-installed-match surface forces live regardless of order (uninstalled last)",
			surfaces: []pendingSurface{
				{"environment", ctxScope()},
				{"unknown-task", taskScope()},
			},
			want: true,
		},
		{
			name:     "refresh forces live even when fully installed",
			surfaces: []pendingSurface{{"go-expert", roleScope()}},
			refresh:  true,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r.flags.Refresh = tt.refresh
			if got := r.computeWantLive(tt.surfaces); got != tt.want {
				t.Errorf("computeWantLive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEnsureTaskRoleLive covers the late task-declared-role liveness wiring
// directly: the decision that the up-front union cannot make because the role
// name is only known after the task resolves. It pins the two guards an inverted
// condition would silently break — the cache-gated/uninstalled case must flip
// live, while installed (exact or substring) and already-live cases must not.
func TestEnsureTaskRoleLive(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, surfaceTestCfg) // roles: go-expert installed

	tests := []struct {
		name      string
		declared  string
		startLive bool
		want      bool
	}{
		{"uninstalled declared role flips cache-gated to live", "missing-role", false, true},
		{"installed declared role stays cache-gated", "go-expert", false, false},
		{"substring-installed declared role stays cache-gated", "expert", false, false},
		{"already live is a no-op", "missing-role", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestResolver(cfg)
			r.wantLive = tt.startLive
			r.ensureTaskRoleLive(tt.declared)
			if r.wantLive != tt.want {
				t.Errorf("ensureTaskRoleLive(%q) with wantLive=%v → %v, want %v",
					tt.declared, tt.startLive, r.wantLive, tt.want)
			}
		})
	}
}

// TestInterpretSurface_DriftGuard pins the single interpretation that both
// resolve()'s dispatch and the up-front union consume. resolve() and
// computeWantLive both call interpretSurface, so a change here that breaks
// classification fails this test rather than silently mis-gating the cache.
func TestInterpretSurface_DriftGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		scope resolveScope
		want  surfaceKind
	}{
		{"./role.md", roleScope(), surfaceLocator},
		{"https://example.test/role.md", roleScope(), surfaceLocator},
		{"", roleScope(), surfaceSkip},
		{"none", ctxScope(), surfaceSkip},
		{"default", ctxScope(), surfaceSkip},
		{"agents:claude", crossCategoryScope(), surfaceName},
		{"expert", roleScope(), surfaceName},
		{"go-expert", roleScope(), surfaceName},
		{"uninstalled-xyz", roleScope(), surfaceName},
		{"grok/auto", agentScope(), surfaceName},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			interp, err := interpretSurface(tt.input, tt.scope)
			if err != nil {
				t.Fatalf("interpretSurface(%q) error = %v", tt.input, err)
			}
			if interp.kind != tt.want {
				t.Errorf("interpretSurface(%q).kind = %v, want %v", tt.input, interp.kind, tt.want)
			}
		})
	}
}

// TestInterpretSurface_SharedByResolveAndUnion asserts resolve() and the union
// act on the same classification: a locator bypasses the index in both, and a
// name with no installed match forces the union live and reaches resolution in
// resolve().
func TestInterpretSurface_SharedByResolveAndUnion(t *testing.T) {
	t.Parallel()
	r := newTestResolver(buildTestCfg(t, surfaceTestCfg))

	// A locator surface: resolve() returns it as a locator; the union excludes it.
	outcome, err := r.resolve("./role.md", roleScope())
	if err != nil || outcome.locator != "./role.md" {
		t.Fatalf("resolve(locator) = (%+v, %v), want locator passthrough", outcome, err)
	}
	if r.computeWantLive([]pendingSurface{{"./role.md", roleScope()}}) {
		t.Error("a locator surface must not force the union live")
	}

	// An uninstalled name: the union forces live; resolve() reaches the lookup
	// (here not-found, since the test resolver has no index).
	if !r.computeWantLive([]pendingSurface{{"uninstalled-xyz", roleScope()}}) {
		t.Error("an uninstalled name must force the union live")
	}
}
