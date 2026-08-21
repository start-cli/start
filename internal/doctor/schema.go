package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/mod/modconfig"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
)

// SchemaSet holds parsed CUE schema definitions for validation.
type SchemaSet struct {
	Agent        cue.Value
	Role         cue.Value
	Context      cue.Value
	Task         cue.Value
	Settings     cue.Value
	SkillInstall cue.Value
}

// LoadSchemas loads CUE schema definitions from a fetched module directory.
func LoadSchemas(dir string, reg modconfig.Registry) (SchemaSet, error) {
	cctx := cuecontext.New()

	cfg := &load.Config{
		Dir:      dir,
		Package:  "schemas",
		Registry: reg,
	}

	insts := load.Instances([]string{"."}, cfg)
	if len(insts) == 0 {
		return SchemaSet{}, fmt.Errorf("no CUE instances found in %s", dir)
	}

	inst := insts[0]
	if inst.Err != nil {
		return SchemaSet{}, fmt.Errorf("loading schemas: %w", inst.Err)
	}

	v := cctx.BuildInstance(inst)
	if err := v.Err(); err != nil {
		return SchemaSet{}, fmt.Errorf("building schemas: %w", err)
	}

	return SchemaSet{
		Agent:        v.LookupPath(cue.ParsePath("#Agent")),
		Role:         v.LookupPath(cue.ParsePath("#Role")),
		Context:      v.LookupPath(cue.ParsePath("#Context")),
		Task:         v.LookupPath(cue.ParsePath("#Task")),
		Settings:     v.LookupPath(cue.ParsePath("#Settings")),
		SkillInstall: v.LookupPath(cue.ParsePath("#SkillInstall")),
	}, nil
}

// categorySchema maps a top-level config key to the corresponding schema.
type categorySchema struct {
	key             string
	schema          cue.Value
	isMap           bool // collection (agents, roles, ...) vs single struct (settings)
	requireConcrete bool // missing required fields fail
	rejectExtra     bool // extra fields fail; otherwise they are ignored
}

// CheckSchemaValidation validates config files against CUE schemas.
func CheckSchemaValidation(paths config.Paths, schemas SchemaSet) SectionResult {
	section := SectionResult{Name: "Schema Validation"}

	categories := []categorySchema{
		{key: internalcue.KeyAgents, schema: schemas.Agent, isMap: true},
		{key: internalcue.KeyRoles, schema: schemas.Role, isMap: true},
		{key: internalcue.KeyContexts, schema: schemas.Context, isMap: true},
		{key: internalcue.KeyTasks, schema: schemas.Task, isMap: true},
		{key: internalcue.KeySkills, schema: schemas.SkillInstall, isMap: true, requireConcrete: true, rejectExtra: true},
		{key: internalcue.KeySettings, schema: schemas.Settings, isMap: false},
	}

	if paths.GlobalExists {
		results := validateConfigDir(paths.Global, categories)
		section.Results = append(section.Results, results...)
	}
	if paths.LocalExists {
		results := validateConfigDir(paths.Local, categories)
		section.Results = append(section.Results, results...)
	}

	if len(section.Results) == 0 {
		section.Results = append(section.Results, CheckResult{
			Status: StatusInfo,
			Label:  "No config files to validate",
		})
	}

	return section
}

func validateConfigDir(dir string, categories []categorySchema) []CheckResult {
	files, err := config.CUEFilesInDir(dir)
	if err != nil || len(files) == 0 {
		return nil
	}

	cctx := cuecontext.New()
	var results []CheckResult

	for _, filePath := range files {
		fileResults := validateSingleFile(cctx, filePath, categories)
		results = append(results, fileResults...)
	}

	return results
}

func validateSingleFile(cctx *cue.Context, filePath string, categories []categorySchema) []CheckResult {
	var results []CheckResult
	fileName := filepath.Base(filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		results = append(results, CheckResult{
			Status:  StatusWarn,
			Label:   fileName,
			Message: fmt.Sprintf("cannot read: %v", err),
		})
		return results
	}

	v := cctx.CompileBytes(data, cue.Filename(filePath))
	if v.Err() != nil {
		// Syntax errors are already caught by the Configuration section.
		return results
	}

	var hasKeys bool
	var hasErrors bool

	for _, cat := range categories {
		if !cat.schema.Exists() {
			continue
		}

		topLevel := v.LookupPath(cue.ParsePath(cat.key))
		if !topLevel.Exists() {
			continue
		}

		hasKeys = true

		if !cat.isMap {
			if err := unifyAndValidate(cat, topLevel); err != nil {
				hasErrors = true
				results = append(results, CheckResult{
					Status:  StatusWarn,
					Label:   fileName,
					Message: fmt.Sprintf("%s: %s", cat.key, internalcue.ErrorSummary(err)),
					Fix:     fmt.Sprintf("Check %s fields match the schema", cat.key),
				})
			}
			continue
		}

		iter, iterErr := topLevel.Fields()
		if iterErr != nil {
			continue
		}

		for iter.Next() {
			entryName := iter.Selector().Unquoted()
			entryValue := iter.Value()

			if err := unifyAndValidate(cat, entryValue); err != nil {
				hasErrors = true
				results = append(results, CheckResult{
					Status:  StatusWarn,
					Label:   fileName,
					Message: fmt.Sprintf("%s.%s: %s", cat.key, entryName, internalcue.ErrorSummary(err)),
					Fix:     fmt.Sprintf("Check %s.%s fields match the %s schema", cat.key, entryName, cat.key),
				})
			}
		}
	}

	if hasKeys && !hasErrors {
		results = append(results, CheckResult{
			Status: StatusPass,
			Label:  fileName,
		})
	}

	return results
}

func unifyAndValidate(cat categorySchema, value cue.Value) error {
	unified := cat.schema.Unify(value)
	var err error
	if cat.requireConcrete {
		err = unified.Validate(cue.Concrete(true))
	} else {
		err = unified.Validate()
	}
	if !cat.rejectExtra {
		return filterAllowedFieldErrors(err)
	}
	return err
}

// filterAllowedFieldErrors drops "field not allowed" errors so configs may carry
// extra fields (open schema), while still surfacing constraint violations.
func filterAllowedFieldErrors(err error) error {
	if err == nil {
		return nil
	}

	var filtered []error
	for _, e := range cueerrors.Errors(err) {
		// CUE lacks structured error codes; string match is the only option.
		if !strings.Contains(e.Error(), "field not allowed") {
			filtered = append(filtered, e)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	return errors.Join(filtered...)
}
