package cli

import (
	"errors"

	"github.com/p3bot/start/internal/fault"
	"github.com/p3bot/start/internal/registry"
)

// resolveCross resolves a cross-category identifier (start get, start describe)
// through the unified engine, spanning all library categories. The exact tier
// consults both installed config and the registry across every scoped category,
// so a same-name twin in another category is detected wherever it lives; a lone
// exact resolves directly (installing a registry-only one), and more than one
// falls to selection. A filesystem path bypasses the search and is returned for
// the caller to read directly.
//
// Callers needing clean stdout (e.g. start get piping content) put stderr in the
// stdout slot: newResolver(cfg, flags, stderr, stderr, stdin).
//
// Post-call: when r.cfgStale is true, the module is on disk but r.cfg is not
// refreshed; a caller that then reads r.cfg.Value must call r.reloadConfig
// first, which clears cfgStale (see runGet, runDescribeSearch).
func (r *resolver) resolveCross(query string) (resolveOutcome, error) {
	scope := crossCategoryScope()
	// Single-surface commands have no multi-stage driver, so the liveness union
	// is computed here over the one query: live iff --refresh is set or it has no
	// installed match across the library categories. An installed query stays
	// cache-gated (its cross-category twin detection runs over the cached index);
	// an uninstalled one resolves live so a freshly published module is found.
	r.wantLive = r.computeWantLive([]pendingSurface{{query, scope}})
	return r.resolve(query, scope)
}

// resolveCrossNoInstall is resolveCross without auto-install. Get and describe
// use it so a skill match is never finalised through InstallModule. A skills:
// prefix miss then matches the dest leaf (inventory, then registry).
func (r *resolver) resolveCrossNoInstall(query string) (resolveOutcome, error) {
	scope := crossCategoryScope()
	r.wantLive = r.computeWantLive([]pendingSurface{{query, scope}})
	outcome, err := r.resolveNoInstall(query, scope)
	if err == nil {
		return outcome, nil
	}
	addr, pErr := parseAddress(query)
	if pErr != nil || !addr.HasPrefix || addr.Category != "skills" {
		return outcome, err
	}
	// Dest leaf runs after a prefix miss, not after prefix ambiguity or usage.
	// A down registry is not a miss: try installed inventory only so a unique
	// installed leaf still resolves offline.
	if !errors.Is(err, fault.ErrNotFound) && r.unreachableErr() == nil {
		return outcome, err
	}
	var index *registry.Index
	if errors.Is(err, fault.ErrNotFound) {
		index, _, _ = r.ensureIndex()
	}
	m, lerr := matchSkillLeaf(r.selector(), inventoryFromValue(r.cfg.Value), index, query, scope)
	if lerr == nil {
		return resolveOutcome{match: m}, nil
	}
	if !errors.Is(lerr, fault.ErrNotFound) {
		return resolveOutcome{}, lerr
	}
	return outcome, err
}
