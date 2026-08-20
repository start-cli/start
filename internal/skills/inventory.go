package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/modules"
)

// Entry is one skills.cue inventory record: origin and version only.
type Entry struct {
	Origin  string
	Version string
}

// InventoryPath returns the skills.cue path in a config directory.
func InventoryPath(configDir string) string {
	return filepath.Join(configDir, internalcue.ConfigFiles[internalcue.KeySkills])
}

// InstallCommand is the no-`--agent` rematerialise for key. Local must be
// set or the write hits the global inventory.
func InstallCommand(key string, local bool) string {
	cmd := "start install skills:" + key
	if local {
		cmd += " --local"
	}
	return cmd
}

// Load reads skills.cue from configDir. A missing file is an empty inventory.
func Load(configDir string) (map[string]Entry, error) {
	path := InventoryPath(configDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, fmt.Errorf("reading skills inventory: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]Entry{}, nil
	}

	v := cuecontext.New().CompileBytes(data, cue.Filename(path))
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("loading skills inventory: %w", err)
	}
	cat := v.LookupPath(cue.ParsePath(internalcue.KeySkills))
	if !cat.Exists() {
		return map[string]Entry{}, nil
	}
	iter, err := cat.Fields()
	if err != nil {
		return nil, fmt.Errorf("iterating skills inventory: %w", err)
	}
	out := make(map[string]Entry)
	for iter.Next() {
		key := iter.Selector().Unquoted()
		var e Entry
		if o := iter.Value().LookupPath(cue.ParsePath("origin")); o.Exists() {
			e.Origin, _ = o.String()
		}
		if ver := iter.Value().LookupPath(cue.ParsePath("version")); ver.Exists() {
			e.Version, _ = ver.String()
		}
		out[key] = e
	}
	return out, nil
}

// Upsert writes origin and version for key (group/name) into skills.cue.
func Upsert(configDir, key, origin, version string) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	content := &ast.StructLit{
		Elts: []ast.Decl{
			&ast.Field{Label: ast.NewIdent("origin"), Value: ast.NewString(origin)},
			&ast.Field{Label: ast.NewIdent("version"), Value: ast.NewString(version)},
		},
	}
	return modules.UpsertConfigModule(InventoryPath(configDir), internalcue.KeySkills, key, content)
}

// Remove drops the inventory entry for key.
func Remove(configDir, key string) error {
	return modules.RemoveModuleFromConfig(InventoryPath(configDir), internalcue.KeySkills, key)
}

// ResolveKey returns inventory keys matching query: a case-insensitive exact
// key first, otherwise keys whose leaf equals query. Zero, one, or many.
func ResolveKey(entries map[string]Entry, query string) []string {
	q := strings.ToLower(query)
	var exact, leaves []string
	for k := range entries {
		kl := strings.ToLower(k)
		if kl == q {
			exact = append(exact, k)
		}
		if strings.ToLower(Leaf(k)) == q {
			leaves = append(leaves, k)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return leaves
}

// ConflictingKeys returns names other than name whose leaf matches name's
// leaf. Dests are leaf-only, so two keys that share a leaf cannot both be
// installed. Comparison is case-insensitive.
func ConflictingKeys(names []string, name string) []string {
	leaf := strings.ToLower(Leaf(name))
	if leaf == "" {
		return nil
	}
	var out []string
	for _, k := range names {
		if strings.EqualFold(k, name) {
			continue
		}
		if strings.ToLower(Leaf(k)) == leaf {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
