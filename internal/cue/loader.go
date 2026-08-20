// Package cue handles CUE configuration loading and validation.
package cue

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueformat "cuelang.org/go/cue/format"
	"cuelang.org/go/cue/load"
	"github.com/p3bot/start/internal/fault"
)

// ErrNoCUEFiles is returned when no CUE files are found in the provided directories.
var ErrNoCUEFiles = errors.New("no CUE files found")

// Loader loads and merges CUE configurations from directories.
type Loader struct {
	ctx *cue.Context
}

// NewLoader creates a new CUE loader.
func NewLoader() *Loader {
	return &Loader{
		ctx: cuecontext.New(),
	}
}

// LoadResult contains the result of loading CUE configuration.
type LoadResult struct {
	Value        cue.Value
	GlobalLoaded bool
	LocalLoaded  bool
}

// Load loads CUE configuration from the specified directories.
// Directories are loaded in order, with later directories taking precedence
// via CUE unification (later values override earlier for matching keys).
// Empty or non-existent directories are skipped.
//
// The caller convention is: dirs[0] = global config, dirs[1] = local config.
// GlobalLoaded/LocalLoaded indicate which of these were successfully loaded.
func (l *Loader) Load(dirs []string) (LoadResult, error) {
	var result LoadResult

	if len(dirs) == 0 {
		return result, fmt.Errorf("no configuration directories provided")
	}

	loaded := make([]bool, len(dirs))

	var values []cue.Value
	for i, dir := range dirs {
		if dir == "" {
			continue
		}

		info, err := os.Stat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("checking directory %s: %w", dir, err)
		}
		if !info.IsDir() {
			return result, fault.UserConfig(fmt.Errorf("%s is not a directory", dir))
		}

		hasCUE, err := HasCUEFiles(dir)
		if err != nil {
			return result, fmt.Errorf("checking for CUE files in %s: %w", dir, err)
		}
		if !hasCUE {
			continue
		}

		v, err := l.loadDir(dir)
		if err != nil {
			return result, fmt.Errorf("loading %s: %w", dir, err)
		}

		values = append(values, v)
		loaded[i] = true
	}

	if len(dirs) > 0 && loaded[0] {
		result.GlobalLoaded = true
	}
	if len(dirs) > 1 && loaded[1] {
		result.LocalLoaded = true
	}

	if len(values) == 0 {
		return result, fmt.Errorf("%w", ErrNoCUEFiles)
	}

	merged, err := l.mergeWithReplacement(values)
	if err != nil {
		return result, fmt.Errorf("merging configurations: %w", err)
	}

	result.Value = merged
	return result, nil
}

// collectionKeys use second-level merge: items are merged additively by name,
// with later values replacing earlier ones for the same item name.
var collectionKeys = map[string]bool{
	KeyAgents:   true,
	KeyRoles:    true,
	KeyContexts: true,
	KeyTasks:    true,
	KeySkills:   true,
}

// mergeWithReplacement merges CUE values with two-level replacement semantics:
// collection items and other fields merge additively by name, but same-named
// entries are fully replaced rather than field-merged. This deliberately differs
// from CUE's native unification, which requires compatible values.
func (l *Loader) mergeWithReplacement(values []cue.Value) (cue.Value, error) {
	if len(values) == 0 {
		return cue.Value{}, fmt.Errorf("no values to merge")
	}
	if len(values) == 1 {
		return values[0], nil
	}

	topLevel := make(map[string]map[string]cue.Value)
	var topLevelOrder []string
	itemOrder := make(map[string][]string)

	for _, v := range values {
		iter, err := v.Fields(cue.All())
		if err != nil {
			return cue.Value{}, fmt.Errorf("iterating fields: %w", err)
		}

		for iter.Next() {
			key := iter.Selector().String()
			fieldValue := iter.Value()

			if _, exists := topLevel[key]; !exists {
				topLevel[key] = make(map[string]cue.Value)
				topLevelOrder = append(topLevelOrder, key)
				itemOrder[key] = nil
			}

			if collectionKeys[key] {
				itemIter, err := fieldValue.Fields(cue.All())
				if err != nil {
					return cue.Value{}, fmt.Errorf("iterating collection %s: %w", key, err)
				}
				for itemIter.Next() {
					itemName := itemIter.Selector().String()
					if _, exists := topLevel[key][itemName]; !exists {
						itemOrder[key] = append(itemOrder[key], itemName)
					}
					topLevel[key][itemName] = itemIter.Value()
				}
			} else {
				if fieldValue.Kind() == cue.StructKind {
					fieldIter, err := fieldValue.Fields(cue.All())
					if err != nil {
						return cue.Value{}, fmt.Errorf("iterating struct %s: %w", key, err)
					}
					for fieldIter.Next() {
						fieldName := fieldIter.Selector().String()
						if _, exists := topLevel[key][fieldName]; !exists {
							itemOrder[key] = append(itemOrder[key], fieldName)
						}
						topLevel[key][fieldName] = fieldIter.Value()
					}
				} else {
					topLevel[key][""] = fieldValue
					itemOrder[key] = []string{""}
				}
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("{\n")

	for _, key := range topLevelOrder {
		items := topLevel[key]
		order := itemOrder[key]

		// Non-struct values are stored under the empty key.
		if len(order) == 1 && order[0] == "" {
			formatted, err := formatValue(items[""])
			if err != nil {
				return cue.Value{}, fmt.Errorf("formatting field %s: %w", key, err)
			}
			sb.WriteString("\t")
			sb.WriteString(key)
			sb.WriteString(": ")
			sb.WriteString(formatted)
			sb.WriteString("\n")
			continue
		}

		sb.WriteString("\t")
		sb.WriteString(key)
		sb.WriteString(": {\n")

		for _, itemName := range order {
			itemValue := items[itemName]
			formatted, err := formatValue(itemValue)
			if err != nil {
				return cue.Value{}, fmt.Errorf("formatting %s.%s: %w", key, itemName, err)
			}
			sb.WriteString("\t\t")
			sb.WriteString(itemName)
			sb.WriteString(": ")
			sb.WriteString(formatted)
			sb.WriteString("\n")
		}

		sb.WriteString("\t}\n")
	}

	sb.WriteString("}")

	merged := l.ctx.CompileString(sb.String())
	if err := merged.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("compiling merged config: %w", err)
	}

	return merged, nil
}

// formatValue formats a CUE value as CUE syntax string.
func formatValue(v cue.Value) (string, error) {
	syn := v.Syntax(
		cue.Final(),
		cue.Concrete(false),
		cue.Definitions(true),
		cue.Hidden(true),
		cue.Optional(true),
	)

	b, err := cueformat.Node(syn)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// LoadSingle loads CUE configuration from a single directory.
func (l *Loader) LoadSingle(dir string) (cue.Value, error) {
	if dir == "" {
		return cue.Value{}, fmt.Errorf("directory path is empty")
	}

	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return cue.Value{}, fmt.Errorf("directory does not exist: %s", dir)
	}
	if err != nil {
		return cue.Value{}, fmt.Errorf("checking directory: %w", err)
	}
	if !info.IsDir() {
		return cue.Value{}, fault.UserConfig(fmt.Errorf("%s is not a directory", dir))
	}

	hasCUE, err := HasCUEFiles(dir)
	if err != nil {
		return cue.Value{}, fmt.Errorf("checking for CUE files: %w", err)
	}
	if !hasCUE {
		return cue.Value{}, fmt.Errorf("%w in %s", ErrNoCUEFiles, dir)
	}

	return l.loadDir(dir)
}

// loadDir loads a CUE instance from a directory.
func (l *Loader) loadDir(dir string) (cue.Value, error) {
	cfg := &load.Config{
		Dir: dir,
		// "*" loads both packaged modules and package-less config files.
		Package: "*",
	}

	insts := load.Instances([]string{"."}, cfg)
	if len(insts) == 0 {
		return cue.Value{}, fmt.Errorf("no instances found in %s", dir)
	}

	inst := insts[0]
	if inst.Err != nil {
		// User-fault: tagged so the mapper returns 78, unlike the internal
		// merged-source compile error in mergeWithReplacement (our bug, stays 1).
		return cue.Value{}, fault.UserConfig(fmt.Errorf("loading instance: %w", inst.Err))
	}

	v := l.ctx.BuildInstance(inst)
	if err := v.Err(); err != nil {
		return cue.Value{}, fault.UserConfig(fmt.Errorf("building instance: %w", err))
	}

	return v, nil
}

// HasCUEFiles checks if a directory contains any .cue files.
func HasCUEFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".cue") {
			return true, nil
		}
	}

	return false, nil
}

// Context returns the underlying CUE context.
func (l *Loader) Context() *cue.Context {
	return l.ctx
}

// IdentifyBrokenFiles compiles each CUE file individually and summarises which
// have errors, for diagnostics when a directory fails to load as a whole.
func IdentifyBrokenFiles(paths []string) string {
	ctx := cuecontext.New()
	var lines []string

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			lines = append(lines, fmt.Sprintf("  %s: %v", path, err))
			continue
		}
		if v := ctx.CompileBytes(data, cue.Filename(path)); v.Err() != nil {
			lines = append(lines, fmt.Sprintf("  %s: %v", path, v.Err()))
		}
	}

	if len(lines) == 0 {
		return "  (files parse individually but fail when combined)"
	}
	return strings.Join(lines, "\n")
}
