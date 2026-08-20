package skills

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/p3bot/agentdex"
	"github.com/p3bot/start/internal/fault"
)

// Dest is one resolved skills root (not including the skill leaf).
type Dest struct {
	AgentID string
	Root    string
}

// Catalog is an agentdex-backed path authority for skill install and describe.
type Catalog struct {
	idx *agentdex.Index
}

// Open constructs a catalog using workingDir for local path expansion.
// Extra options (WithCatalogDir, WithLookPath, WithBinPaths, WithEnvLookup)
// are the test seams; production callers pass none.
func Open(workingDir string, opts ...agentdex.Option) (*Catalog, error) {
	all := make([]agentdex.Option, 0, len(opts)+1)
	if workingDir != "" {
		all = append(all, agentdex.WithWorkingDir(workingDir))
	}
	all = append(all, opts...)
	idx, err := agentdex.Open(all...)
	if err != nil {
		return nil, mapCatalogErr(err)
	}
	return &Catalog{idx: idx}, nil
}

// Detected returns Found agents that have a skills concept in this scope.
func (c *Catalog) Detected(ctx context.Context, local bool) ([]agentdex.Agent, error) {
	res, err := c.idx.Agents.List(ctx, agentdex.AgentQuery{
		Installed: true,
		Enrich:    agentdex.EnrichNone,
	})
	if err != nil {
		return nil, mapCatalogErr(err)
	}
	var out []agentdex.Agent
	for _, a := range res.Items {
		if a.Detection.Found && HasSkillsConcept(a, local) {
			out = append(out, a)
		}
	}
	return out, nil
}

// Lookup resolves one agentdex catalog id. Found is not required.
func (c *Catalog) Lookup(ctx context.Context, id string) (agentdex.Agent, error) {
	detail, err := c.idx.Agents.Get(ctx, id, agentdex.AgentGetQuery{Enrich: agentdex.EnrichNone})
	if err != nil {
		if errors.Is(err, agentdex.ErrAgentUnknown) {
			return agentdex.Agent{}, fault.Usage(fmt.Errorf("unknown agentdex id %q", id))
		}
		return agentdex.Agent{}, mapCatalogErr(err)
	}
	return detail.Agent, nil
}

// ListRoots returns dest roots for this invocation without requiring any.
// Empty agentIDs uses every detected agent's Primary for the scope.
// Named ids use Native if set, else Shared. Destinations are deduplicated
// by cleaned absolute path. An empty detected set yields no dests.
func (c *Catalog) ListRoots(ctx context.Context, agentIDs []string, local bool) ([]Dest, error) {
	if len(agentIDs) == 0 {
		return c.detectedPrimaries(ctx, local)
	}
	return c.namedRoots(ctx, agentIDs, local)
}

// ResolveRoots is ListRoots plus the install requirement: with no named
// agents, at least one dest must exist.
func (c *Catalog) ResolveRoots(ctx context.Context, agentIDs []string, local bool) ([]Dest, error) {
	dests, err := c.ListRoots(ctx, agentIDs, local)
	if err != nil {
		return nil, err
	}
	if len(agentIDs) == 0 && len(dests) == 0 {
		return nil, fault.Usage(fmt.Errorf("no skill-capable agent detected; install a skill-capable binary or pass --agent"))
	}
	return dests, nil
}

// UninstallRoots returns the unique Primary, Native, and Shared roots of
// every detected agent for the scope. Catalog failure fails closed.
// An empty detected set yields no dests (missing dests are fine on uninstall).
func (c *Catalog) UninstallRoots(ctx context.Context, local bool) ([]Dest, error) {
	detected, err := c.Detected(ctx, local)
	if err != nil {
		return nil, err
	}
	var dests []Dest
	for _, a := range detected {
		sc := scopeOf(a, local)
		for _, p := range []string{sc.Primary.Path, sc.Native.Path, sc.Agents.Path} {
			if p != "" {
				dests = append(dests, Dest{AgentID: a.ID, Root: p})
			}
		}
	}
	return dedupeDests(dests)
}

func (c *Catalog) detectedPrimaries(ctx context.Context, local bool) ([]Dest, error) {
	detected, err := c.Detected(ctx, local)
	if err != nil {
		return nil, err
	}
	var dests []Dest
	for _, a := range detected {
		p := scopeOf(a, local).Primary.Path
		if p == "" {
			continue
		}
		dests = append(dests, Dest{AgentID: a.ID, Root: p})
	}
	return dedupeDests(dests)
}

func (c *Catalog) namedRoots(ctx context.Context, ids []string, local bool) ([]Dest, error) {
	var dests []Dest
	for _, id := range ids {
		a, err := c.Lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		if !HasSkillsConcept(a, local) {
			return nil, fault.Usage(fmt.Errorf("agent %q is not skill-capable", id))
		}
		root, ok := namedRoot(scopeOf(a, local))
		if !ok {
			return nil, fault.Usage(fmt.Errorf("agent %q has no writable skills path", id))
		}
		dests = append(dests, Dest{AgentID: a.ID, Root: root})
	}
	return dedupeDests(dests)
}

// HasSkillsConcept reports whether the agent defines skills roots in this scope.
func HasSkillsConcept(a agentdex.Agent, local bool) bool {
	return skillScopeDefined(scopeOf(a, local))
}

func skillScopeDefined(sc agentdex.SkillsScope) bool {
	return sc.Agents.Path != "" || sc.Native.Path != "" || sc.Primary.Path != "" || len(sc.Alternatives) > 0
}

func scopeOf(a agentdex.Agent, local bool) agentdex.SkillsScope {
	if local {
		return a.Detection.Skills.Local
	}
	return a.Detection.Skills.Global
}

func namedRoot(sc agentdex.SkillsScope) (string, bool) {
	if sc.Native.Path != "" {
		return sc.Native.Path, true
	}
	if sc.Agents.Path != "" {
		return sc.Agents.Path, true
	}
	return "", false
}

func dedupeDests(dests []Dest) ([]Dest, error) {
	seen := make(map[string]bool, len(dests))
	out := make([]Dest, 0, len(dests))
	for _, d := range dests {
		abs, err := filepath.Abs(filepath.Clean(d.Root))
		if err != nil {
			return nil, fmt.Errorf("resolving skills path %q: %w", d.Root, err)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		d.Root = abs
		out = append(out, d)
	}
	return out, nil
}

func mapCatalogErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentdex.ErrCatalogUnavailable) || errors.Is(err, agentdex.ErrCatalogInvalid) {
		return fmt.Errorf("agent catalog unavailable: cannot resolve skill paths. Print the skill with 'start get skills:<group>/<name>' and place it manually in the agent's skills directory")
	}
	return err
}
