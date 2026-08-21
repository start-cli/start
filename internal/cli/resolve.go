package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/p3bot/start/internal/cache"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/registry"
)

// ModuleSourceInstalled and ModuleSourceRegistry are the resolution layer's
// names for the shared modules.Source values, so the registry-free removal
// matcher and the registry-backed resolver tag candidates in the same units as
// the gathering primitive without an import cycle.
const (
	ModuleSourceInstalled = modules.SourceInstalled
	ModuleSourceRegistry  = modules.SourceRegistry
)

// ModuleMatch is one matched module during resolution. It is the shared
// modules.Candidate; resolution ignores its Scope and (for installed matches)
// its Entry, which the gathering primitive populates for the discovery surfaces.
type ModuleMatch = modules.Candidate

// maxModuleResults is the maximum number of results to display in interactive selection.
const maxModuleResults = 20

// offlineRegistryForTests makes newResolver wire the offline index source (a nil
// index, no registry access) in place of the production one. The cross-category
// surfaces (get, describe) consult the registry through a resolver they build
// internally, which the provider seam cannot stub, so the cli test binary sets
// this in TestMain to keep content and scope tests offline. Production never sets
// it; a resolver given an injected index source resolves against it regardless.
var offlineRegistryForTests bool

// resolver performs name-only resolution for module-selecting surfaces. It
// lazily fetches the registry index and tracks whether any installs occurred.
//
// Two install-tracking fields with distinct lifetimes: didInstall is sticky for
// the resolver's life (an install happened at all — drives the --local scope-
// widening notice), while cfgStale flips true on each install and clears on each
// reloadConfig (r.cfg no longer matches disk — drives reload decisions). A
// surface that installs across multiple stages (task: flags → task → task-role)
// must gate every reload on cfgStale, never on the sticky didInstall, or a later
// stage's install is missed once an earlier one already set didInstall.
type resolver struct {
	cfg        internalcue.LoadResult
	flags      *Flags
	stderr     io.Writer
	stdout     io.Writer
	stdin      io.Reader
	index      *registry.Index
	client     registry.Client
	indexErr   error
	didFetch   bool
	didInstall bool
	cfgStale   bool

	// indexSrc is the resolver's sole index-acquisition seam. newResolver wires
	// the production source (the lone registry.NewClient caller) or, in the test
	// binary, the offline source; newResolverWithIndex injects a pre-loaded index.
	indexSrc indexSource

	// wantLive forces ensureIndex to resolve the index live rather than reuse a
	// fresh cache. It is decided once up front by the driver (computeWantLive):
	// true when --refresh is set or any pending surface has no installed match
	// (a module is about to be auto-installed, so the latest index is needed).
	// The late task-declared-role check may flip it true (forceLiveReResolve).
	wantLive bool
}

func newResolver(cfg internalcue.LoadResult, flags *Flags, stdout, stderr io.Writer, stdin io.Reader) *resolver {
	var src indexSource
	if offlineRegistryForTests {
		src = offlineIndexSource{}
	} else {
		src = newProductionIndexSource(flags, stderr)
	}
	return &resolver{
		cfg:      cfg,
		flags:    flags,
		stderr:   stderr,
		stdout:   stdout,
		stdin:    stdin,
		indexSrc: src,
	}
}

// pendingSurface is one flag- or arg-bound surface whose identifier is known
// before resolution begins, paired with the scope it resolves under. The
// liveness union (computeWantLive) interprets each through interpretSurface.
type pendingSurface struct {
	input string
	scope resolveScope
}

// baseSurfaces builds the flag-bound liveness surfaces every resolution driver
// shares: the agent, the role flag (when set and not skipped by a none-token),
// and one surface per --context selector. executeStart passes these straight to
// computeWantLive; executeTask appends its task surface. The late task-declared
// role is excluded — its name is unknown here and ensureTaskRoleLive handles it.
func baseSurfaces(flags *Flags) []pendingSurface {
	agent := ""
	if len(flags.Agent) == 1 {
		agent = flags.Agent[0]
	}
	surfaces := []pendingSurface{{agent, singleCategoryScope("agents", "agent", false)}}
	if flags.Role != "" && !flags.NoRole {
		surfaces = append(surfaces, pendingSurface{flags.Role, singleCategoryScope("roles", "role", true)})
	}
	for _, tag := range flags.Context {
		surfaces = append(surfaces, pendingSurface{tag, singleCategoryScope("contexts", "context", true)})
	}
	return surfaces
}

// computeWantLive decides, once and up front, whether the resolver must resolve
// the index live: true when --refresh is set or any pending surface has no
// installed match (a module is about to be auto-installed, so the whole
// invocation must see the latest index, like start install). A surface that
// bypasses the index — a locator or a sentinel — is excluded, so it never forces
// a spurious live resolve for a co-resolved installed surface.
//
// The union interprets each identifier exactly as resolve() does (via
// interpretSurface) rather than counting raw flag values; a malformed surface is
// skipped here and left for the real resolve to report in order.
func (r *resolver) computeWantLive(surfaces []pendingSurface) bool {
	if r.flags.Refresh {
		return true
	}
	for _, s := range surfaces {
		interp, err := interpretSurface(s.input, s.scope)
		if err != nil {
			continue
		}
		if !r.surfaceHasInstalledMatch(interp) {
			return true
		}
	}
	return false
}

// surfaceHasInstalledMatch reports whether an installed module satisfies the
// interpreted surface under either the exact tier or its fallback (substring/
// prefix) tier — both network-free config lookups. A skip or locator surface
// bypasses the index and so counts as satisfied: it can never force a live
// resolve on its own.
func (r *resolver) surfaceHasInstalledMatch(interp surfaceInterpretation) bool {
	if interp.kind != surfaceName {
		return true
	}
	for _, cat := range interp.cats {
		if len(r.collectInstalled(cat.category, interp.name, modeExact)) > 0 {
			return true
		}
		if len(r.collectInstalled(cat.category, interp.name, interp.mode)) > 0 {
			return true
		}
	}
	return false
}

// forceLiveReResolve arms the next ensureIndex to resolve live, discarding any
// cache-gated index already held. It exists for the one late-bound surface — the
// task-declared role, whose name is unknown when the up-front union runs — and
// may repeat at most one metadata resolve if the cache-gated path had already
// fetched. Keyed off r.wantLive by the caller, so no held-index-liveness field
// is needed.
func (r *resolver) forceLiveReResolve() {
	r.wantLive = true
	r.didFetch = false
	r.index = nil
	r.indexErr = nil
}

// ensureTaskRoleLive runs the late liveness check for the one surface the
// up-front union could not see — the task-declared role, whose name only becomes
// known after the task resolves. When the union already resolved cache-gated and
// the declared role has no installed match, it forces a single live re-resolve
// so a role published within the cache window is still discovered. It is a no-op
// when the union already forced live, when the declared name is malformed, or
// when an installed match already satisfies it.
func (r *resolver) ensureTaskRoleLive(declared string) {
	if r.wantLive {
		return
	}
	interp, err := interpretSurface(declared, singleCategoryScope("roles", "role", true))
	if err != nil || r.surfaceHasInstalledMatch(interp) {
		return
	}
	debugf(r.stderr, r.flags, dbgResolve, "task-declared role %q not installed; forcing live index resolve", declared)
	r.forceLiveReResolve()
}

// resolveAgent resolves the --agent identifier to an installed agent name. An
// agent is a structured configuration, not a document body, so a filesystem
// path is rejected.
func (r *resolver) resolveAgent(name string) (string, error) {
	return r.resolveSingle(name, singleCategoryScope("agents", "agent", false))
}

// resolveRole resolves the --role identifier to an installed role name or a
// locator (local path or http(s) URL).
func (r *resolver) resolveRole(name string) (string, error) {
	return r.resolveSingle(name, singleCategoryScope("roles", "role", true))
}

// resolveSingle resolves a category-specific identifier, returning either the
// resolved module name or the locator (local path or http(s) URL) it bypassed
// to. An empty identifier passes through unchanged (the caller's "use the
// default" signal).
func (r *resolver) resolveSingle(name string, scope resolveScope) (string, error) {
	interp, err := interpretSurface(name, scope)
	if err != nil {
		return "", err
	}
	if interp.kind == surfaceSkip {
		// An empty identifier is the caller's "use the default" signal.
		return "", nil
	}
	outcome, err := r.resolve(name, scope)
	if err != nil {
		return "", err
	}
	if outcome.locator != "" {
		return outcome.locator, nil
	}
	return outcome.match.Name, nil
}

// resolveModelName resolves a model name against agent.Models: exact match,
// then multi-term AND substring match, then passthrough. Model resolution is
// deliberately outside the unified module match rule — its target is an agent's
// model map, not the module sources — so it keeps the search-style match.
func (r *resolver) resolveModelName(name string, agent orchestration.Agent) string {
	if name == "" {
		return ""
	}

	if _, ok := agent.Models[name]; ok {
		debugf(r.stderr, r.flags, dbgResolve, "Model %q: exact match in models map", name)
		return name
	}

	terms := modules.ParseSearchTerms(name)
	if len(terms) == 0 {
		return name
	}

	var matches []string
	for key := range agent.Models {
		keyLower := strings.ToLower(key)
		allMatch := true
		for _, term := range terms {
			if !strings.Contains(keyLower, term) {
				allMatch = false
				break
			}
		}
		if allMatch {
			matches = append(matches, key)
		}
	}

	sort.Strings(matches) // deterministic ordering

	if len(matches) == 1 {
		debugf(r.stderr, r.flags, dbgResolve, "Model %q: match %q", name, matches[0])
		return matches[0]
	}

	if len(matches) > 1 {
		debugf(r.stderr, r.flags, dbgResolve, "Model %q: multiple matches %v, using passthrough", name, matches)
	}

	debugf(r.stderr, r.flags, dbgResolve, "Model %q: passthrough", name)
	return name
}

// resolveContexts resolves each --context term independently through the unified
// match rule. A locator (local path or http(s) URL) is read directly; the
// "default" sentinel passes through unsearched ("none" is consumed upstream and
// never reaches here). Every other term resolves to exactly one context,
// erroring when it matches nothing.
func (r *resolver) resolveContexts(terms []string) ([]string, error) {
	if len(terms) == 0 {
		return nil, nil
	}

	scope := singleCategoryScope("contexts", "context", true)

	var resolved []string
	for _, term := range terms {
		interp, err := interpretSurface(term, scope)
		if err != nil {
			return nil, err
		}
		if interp.kind == surfaceSkip {
			// The only skip token that survives as a real selector is "default",
			// which passes through unsearched for Compose to expand. An empty
			// selector names nothing, and none-tokens are stripped upstream
			// (resolveContextSkip); either way there is nothing to select, so drop.
			if term == "default" {
				debugf(r.stderr, r.flags, dbgResolve, "Context %q: default passthrough", term)
				resolved = append(resolved, term)
			}
			continue
		}

		outcome, err := r.resolve(term, scope)
		if err != nil {
			return nil, err
		}
		if outcome.locator != "" {
			resolved = append(resolved, outcome.locator)
			continue
		}
		resolved = append(resolved, outcome.match.Name)
	}

	return resolved, nil
}

func (r *resolver) autoInstall(client registry.Client, result modules.SearchResult) error {
	if client == nil {
		return fmt.Errorf("registry client unavailable")
	}

	ctx := context.Background()

	paths, err := config.ResolvePaths("")
	if err != nil {
		return fmt.Errorf("resolving config paths: %w", err)
	}

	debugf(r.stderr, r.flags, dbgResolve, "Auto-installing %s from registry", formatAddress(result.Category, result.Name))

	if !r.flags.Quiet {
		fmt.Fprintf(r.stdout, "Installing %s from registry...\n", result.Name)
	}

	version, err := modules.InstallModule(ctx, client, r.index, result, paths.Global)
	if err != nil {
		return err
	}

	if !r.flags.Quiet {
		if version != "" {
			fmt.Fprintf(r.stdout, "Installed %s@%s to global config\n\n", result.Name, version)
		} else {
			fmt.Fprintf(r.stdout, "Installed %s to global config\n\n", result.Name)
		}
	}

	debugf(r.stderr, r.flags, dbgResolve, "Auto-installed %s", formatAddress(result.Category, result.Name))
	r.didInstall = true
	r.cfgStale = true
	return nil
}

// indexSource acquires the registry index for the resolver. Its single
// operation takes the already-decided live-vs-cache-gated signal for the
// invocation and returns the index, the client used (nil when none was built),
// and a soft failure. A registry-unavailable outcome is a nil index with the
// failure returned for the resolver to record in indexErr — never a hard error
// the resolver propagates.
type indexSource interface {
	fetch(ctx context.Context, wantLive bool) (*registry.Index, registry.Client, error)
}

// productionIndexSource holds the live index-acquisition mechanism: effective-
// path computation, the decideCachedIndex cache-gating call, the conditional
// "Fetching registry index..." progress line (stderr), client construction, the
// slow-warning goroutine, FetchIndex, and the best-effort cache write on a live
// resolve. It is the single place in the resolver's dependency graph that calls
// registry.NewClient.
type productionIndexSource struct {
	flags  *Flags
	stderr io.Writer
}

func newProductionIndexSource(flags *Flags, stderr io.Writer) *productionIndexSource {
	return &productionIndexSource{flags: flags, stderr: stderr}
}

// fetch resolves the index under the cache-gating rule. Client construction and
// FetchIndex failures are soft: the index comes back nil with the error returned
// for the resolver to record, never propagated as a hard error.
//
// A cache-gated resolve reuses a fresh cache's canonical version so FetchIndex
// serves from CUE's module cache without a metadata request, while a live
// resolve passes the bare-major path so the latest version is resolved; a stale
// or missing cache always fetches and updates the cache.
func (s *productionIndexSource) fetch(ctx context.Context, wantLive bool) (*registry.Index, registry.Client, error) {
	effectivePath := registry.EffectiveIndexPath(resolveLibraryIndexPath())
	cachedVersion, usedCache := decideCachedIndex(effectivePath, wantLive)
	indexPath := effectivePath
	if usedCache {
		debugf(s.stderr, s.flags, dbgResolve, "Using cached index version: %s", cachedVersion)
		indexPath = cachedVersion
	} else if !s.flags.Quiet {
		fmt.Fprintf(s.stderr, "Fetching registry index...\n")
	}

	client, err := registry.NewClient()
	if err != nil {
		debugf(s.stderr, s.flags, dbgResolve, "Registry unavailable: %v", err)
		return nil, nil, err // soft failure
	}

	const fetchTimeout = 60 * time.Second
	const slowWarning = 10 * time.Second

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	if !s.flags.Quiet {
		go func() {
			select {
			case <-time.After(slowWarning):
				remaining := fetchTimeout - slowWarning
				printWarning(s.stderr, "registry is taking longer than expected, timeout in %d seconds", int(remaining.Seconds()))
			case <-ctx.Done():
			}
		}()
	}

	index, indexVersion, err := client.FetchIndex(ctx, indexPath)
	if err != nil {
		debugf(s.stderr, s.flags, dbgResolve, "Index fetch failed: %v", err)
		return nil, client, err // soft failure
	}
	if !usedCache {
		if err := cache.WriteIndex(indexVersion); err != nil {
			debugf(s.stderr, s.flags, dbgResolve, "Cache write failed: %v", err)
		}
	}

	debugf(s.stderr, s.flags, dbgResolve, "Index fetched: version %s", indexVersion)
	return index, client, nil
}

// offlineIndexSource returns a nil index without any registry access. It backs
// the test-binary offline default (offlineRegistryForTests) that keeps resolver-
// backed surfaces offline.
type offlineIndexSource struct{}

func (offlineIndexSource) fetch(context.Context, bool) (*registry.Index, registry.Client, error) {
	return nil, nil, nil
}

// ensureIndex lazily acquires the registry index through the resolver's index
// source, memoizing the result for the whole invocation. It returns a nil index
// with a nil error when the registry is unavailable (graceful fallback); the
// underlying failure is recorded in r.indexErr so callers can apply the
// certainty split.
//
// Liveness is decided once per resolver (r.wantLive, set up front by the driver)
// and handed to the source, which gates the cache accordingly. All mechanism —
// client construction, fetching, the progress line, the cache write — lives in
// the source; ensureIndex keeps only memoization and the indexErr bookkeeping
// that turns a soft source failure into graceful degradation.
func (r *resolver) ensureIndex() (*registry.Index, registry.Client, error) {
	if r.didFetch {
		return r.index, r.client, r.indexErr
	}
	r.didFetch = true
	r.index, r.client, r.indexErr = r.indexSrc.fetch(context.Background(), r.wantLive)
	return r.index, r.client, nil
}

// resolveLibraryIndexPath returns the library_index setting (empty on unset or
// error); pass the result to registry.EffectiveIndexPath for the module path.
func resolveLibraryIndexPath() string {
	settings, err := loadSettingsForScope(config.ScopeMerged)
	if err != nil {
		return ""
	}
	return settings["library_index"]
}

func (r *resolver) reloadConfig(workingDir string) error {
	cfg, err := loadMergedConfigFromDirWithDebug(r.stdout, r.stderr, r.stdin, workingDir, r.flags)
	if err != nil {
		return fmt.Errorf("reloading configuration: %w", err)
	}
	r.cfg = cfg
	r.cfgStale = false
	return nil
}
