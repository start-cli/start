package registry

import (
	"context"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/mod/modconfig"
)

// IndexModulePath is the CUE module path for the start library index.
// Major version; resolved to latest canonical version at runtime.
const IndexModulePath = "github.com/p3bot/library/index@v1"

// SchemaModulePath is the CUE module path for the start library schemas.
const SchemaModulePath = "github.com/p3bot/library/schemas@v1"

// IndexEntry represents an entry in the library index.
type IndexEntry struct {
	Module      string   `json:"module"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Version     string   `json:"version,omitempty"`
	Bin         string   `json:"bin,omitempty"`
}

// Index represents the full library discovery index.
type Index struct {
	Agents   map[string]IndexEntry `json:"agents,omitempty"`
	Roles    map[string]IndexEntry `json:"roles,omitempty"`
	Contexts map[string]IndexEntry `json:"contexts,omitempty"`
	Tasks    map[string]IndexEntry `json:"tasks,omitempty"`
	Skills   map[string]IndexEntry `json:"skills,omitempty"`
}

// EffectiveIndexPath returns configured if non-empty, otherwise IndexModulePath.
func EffectiveIndexPath(configured string) string {
	if configured != "" {
		return configured
	}
	return IndexModulePath
}

// FetchIndex fetches and parses the index from the registry. Pass empty
// indexPath to use IndexModulePath; returns the resolved canonical version.
func (c *client) FetchIndex(ctx context.Context, indexPath string) (*Index, string, error) {
	resolvedPath, err := c.ResolveLatestVersion(ctx, EffectiveIndexPath(indexPath))
	if err != nil {
		return nil, "", fmt.Errorf("resolving index version: %w", err)
	}

	result, err := c.Fetch(ctx, resolvedPath)
	if err != nil {
		return nil, "", fmt.Errorf("fetching index module: %w", err)
	}

	idx, err := LoadIndex(result.SourceDir, c.registry)
	if err != nil {
		return nil, "", err
	}

	return idx, resolvedPath, nil
}

// LoadIndex loads and parses the index from a directory.
func LoadIndex(dir string, reg modconfig.Registry) (*Index, error) {
	cctx := cuecontext.New()

	cfg := &load.Config{
		Dir:      dir,
		Package:  "index",
		Registry: reg,
	}

	insts := load.Instances([]string{"."}, cfg)
	if len(insts) == 0 {
		return nil, fmt.Errorf("no CUE instances found in %s", dir)
	}

	inst := insts[0]
	if inst.Err != nil {
		return nil, fmt.Errorf("loading index: %w", inst.Err)
	}

	v := cctx.BuildInstance(inst)
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("building index: %w", err)
	}

	return decodeIndex(v)
}

func decodeIndex(v cue.Value) (*Index, error) {
	idx := &Index{
		Agents:   make(map[string]IndexEntry),
		Roles:    make(map[string]IndexEntry),
		Contexts: make(map[string]IndexEntry),
		Tasks:    make(map[string]IndexEntry),
		Skills:   make(map[string]IndexEntry),
	}

	if err := decodeCategory(v, "agents", idx.Agents); err != nil {
		return nil, err
	}
	if err := decodeCategory(v, "roles", idx.Roles); err != nil {
		return nil, err
	}
	if err := decodeCategory(v, "contexts", idx.Contexts); err != nil {
		return nil, err
	}
	if err := decodeCategory(v, "tasks", idx.Tasks); err != nil {
		return nil, err
	}
	if err := decodeCategory(v, "skills", idx.Skills); err != nil {
		return nil, err
	}

	return idx, nil
}

func decodeCategory(v cue.Value, name string, target map[string]IndexEntry) error {
	catVal := v.LookupPath(cue.ParsePath(name))
	if !catVal.Exists() {
		return nil // category is optional
	}

	iter, err := catVal.Fields()
	if err != nil {
		return fmt.Errorf("iterating %s: %w", name, err)
	}

	for iter.Next() {
		key := iter.Selector().Unquoted()
		var entry IndexEntry
		if err := iter.Value().Decode(&entry); err != nil {
			return fmt.Errorf("decoding %s.%s: %w", name, key, err)
		}
		target[key] = entry
	}

	return nil
}
