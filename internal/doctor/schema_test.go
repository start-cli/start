package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/skills"
)

// testSchemaSet mirrors the production schemas in library/schemas/.
func testSchemaSet(t *testing.T) SchemaSet {
	t.Helper()
	cctx := cuecontext.New()

	schemaSource := `
#UTD: {
	file?:    string
	command?: string
	prompt?:  string
	shell?:   string & !=""
	timeout?: int & >=1 & <=3600
}

#Base: {
	description?: string
	tags?: [...string & =~"^[a-z0-9]+(-[a-z0-9]+)*$"]
	origin?: string
}

#Agent: {
	#Base
	command: string & !=""
	bin?: string & !=""
	default_model?: string
	models?: [string]: string & !=""
}

#Role: {
	#Base
	#UTD
	optional: bool | *false
}

#Context: {
	#Base
	#UTD
	required?: bool
	default?:  bool
}

#Task: {
	#Base
	#UTD
	role?:  string
	agent?: string
}

#Settings: {
	default_agent?: string & !=""
	shell?: string & !=""
	timeout?: int & >0
	library_index?: string & !=""
}

#SkillInstall: {
	origin: string & !=""
	version: string & !=""
}
`

	v := cctx.CompileString(schemaSource)
	if v.Err() != nil {
		t.Fatalf("compiling test schemas: %v", v.Err())
	}

	return SchemaSet{
		Agent:        v.LookupPath(cue.ParsePath("#Agent")),
		Role:         v.LookupPath(cue.ParsePath("#Role")),
		Context:      v.LookupPath(cue.ParsePath("#Context")),
		Task:         v.LookupPath(cue.ParsePath("#Task")),
		Settings:     v.LookupPath(cue.ParsePath("#Settings")),
		SkillInstall: v.LookupPath(cue.ParsePath("#SkillInstall")),
	}
}

func writeSchemasModule(t *testing.T, dir, body string) {
	t.Helper()
	modDir := filepath.Join(dir, "cue.mod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	moduleCue := "module: \"test.example/schemas@v0\"\nlanguage: version: \"v0.16.0\"\n"
	if err := os.WriteFile(filepath.Join(modDir, "module.cue"), []byte(moduleCue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schemas.cue"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSchemas_SkillInstall(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeSchemasModule(t, dir, `package schemas

#Agent: {command: string}
#Role: {}
#Context: {}
#Task: {}
#Settings: {}
#SkillInstall: {
	origin: string & !=""
	version: string & !=""
}
`)
		schemas, err := LoadSchemas(dir, nil)
		if err != nil {
			t.Fatalf("LoadSchemas: %v", err)
		}
		if !schemas.SkillInstall.Exists() {
			t.Fatal("#SkillInstall was not loaded")
		}
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeSchemasModule(t, dir, `package schemas

#Agent: {command: string}
#Role: {}
#Context: {}
#Task: {}
#Settings: {}
`)
		schemas, err := LoadSchemas(dir, nil)
		if err != nil {
			t.Fatalf("LoadSchemas: %v", err)
		}
		if schemas.SkillInstall.Exists() {
			t.Fatal("missing #SkillInstall should not exist")
		}
	})
}

func writeConfigFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSchemaValidation_ValidAgent(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "claude": {
	command: "claude --model {{.model}} {{.prompt}}"
	bin: "claude"
	description: "Claude by Anthropic"
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if section.Name != "Schema Validation" {
		t.Errorf("Name = %q, want %q", section.Name, "Schema Validation")
	}
	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(section.Results))
	}
	if section.Results[0].Status != StatusPass {
		t.Errorf("status = %v, want StatusPass", section.Results[0].Status)
	}
	if section.Results[0].Label != "agents.cue" {
		t.Errorf("label = %q, want %q", section.Results[0].Label, "agents.cue")
	}
}

func TestCheckSchemaValidation_EmptyCommand(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "bad": {
	command: ""
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	found := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "agents.bad") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected StatusWarn for agents.bad, got results: %+v", section.Results)
	}
}

func TestCheckSchemaValidation_InvalidTimeout(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "roles.cue", `
roles: "bad": {
	prompt: "test prompt"
	timeout: 9999
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	found := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "roles.bad") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected StatusWarn for roles.bad, got results: %+v", section.Results)
	}
}

func TestCheckSchemaValidation_ExtraFields(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "myagent": {
	command: "test-cmd {{.prompt}}"
	custom_field: "should be allowed"
	another: 42
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(section.Results))
	}
	if section.Results[0].Status != StatusPass {
		t.Errorf("status = %v, want StatusPass (extra fields should be allowed)", section.Results[0].Status)
	}
}

func TestCheckSchemaValidation_MissingAgentCommandPasses(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "incomplete": {
	description: "no command"
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(section.Results), section.Results)
	}
	if section.Results[0].Status != StatusPass {
		t.Errorf("status = %v, want StatusPass (missing command is incomplete, not a schema issue): %+v", section.Results[0].Status, section.Results[0])
	}
}

func TestUnifyAndValidate_PoliciesIndependent(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	cctx := cuecontext.New()

	incomplete := cctx.CompileString(`{description: "no command"}`)
	if err := incomplete.Err(); err != nil {
		t.Fatal(err)
	}
	extra := cctx.CompileString(`{command: "ok", extra: true}`)
	if err := extra.Err(); err != nil {
		t.Fatal(err)
	}

	t.Run("concrete allows extra", func(t *testing.T) {
		t.Parallel()
		cat := categorySchema{schema: schemas.Agent, requireConcrete: true}
		if err := unifyAndValidate(cat, incomplete); err == nil {
			t.Error("missing command should fail when requireConcrete is set")
		}
		if err := unifyAndValidate(cat, extra); err != nil {
			t.Errorf("extra field should pass when rejectExtra is unset: %v", err)
		}
	})

	t.Run("reject extra allows incomplete", func(t *testing.T) {
		t.Parallel()
		cat := categorySchema{schema: schemas.Agent, rejectExtra: true}
		if err := unifyAndValidate(cat, incomplete); err != nil {
			t.Errorf("missing command should pass when requireConcrete is unset: %v", err)
		}
		if err := unifyAndValidate(cat, extra); err == nil {
			t.Error("extra field should fail when rejectExtra is set")
		}
	})
}

func TestCheckSchemaValidation_InvalidSettings(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "settings.cue", `
settings: {
	shell: ""
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	found := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "settings") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected StatusWarn for settings, got results: %+v", section.Results)
	}
}

func TestCheckSchemaValidation_ValidSettings(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "settings.cue", `
settings: {
	default_agent: "claude"
	shell: "/bin/bash"
	timeout: 120
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(section.Results))
	}
	if section.Results[0].Status != StatusPass {
		t.Errorf("status = %v, want StatusPass", section.Results[0].Status)
	}
}

func TestCheckSchemaValidation_MultipleFiles(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "good": {
	command: "good-cmd"
}
`)
	writeConfigFile(t, tmpDir, "roles.cue", `
roles: "myrole": {
	prompt: "You are helpful"
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 2 {
		t.Fatalf("expected 2 results (one per file), got %d", len(section.Results))
	}
	for _, r := range section.Results {
		if r.Status != StatusPass {
			t.Errorf("file %q: status = %v, want StatusPass", r.Label, r.Status)
		}
	}
}

func TestCheckSchemaValidation_GlobalAndLocal(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "global")
	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeConfigFile(t, globalDir, "agents.cue", `
agents: "claude": {
	command: "claude {{.prompt}}"
}
`)
	writeConfigFile(t, localDir, "roles.cue", `
roles: "reviewer": {
	prompt: "Review code"
}
`)

	paths := config.Paths{
		Global:       globalDir,
		GlobalExists: true,
		Local:        localDir,
		LocalExists:  true,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 2 {
		t.Fatalf("expected 2 results (one per dir), got %d", len(section.Results))
	}
	for _, r := range section.Results {
		if r.Status != StatusPass {
			t.Errorf("file %q: status = %v, want StatusPass", r.Label, r.Status)
		}
	}
}

func TestCheckSchemaValidation_NoConfigDirs(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	paths := config.Paths{
		Global:       filepath.Join(tmpDir, "global"),
		GlobalExists: false,
		Local:        filepath.Join(tmpDir, "local"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(section.Results))
	}
	if section.Results[0].Status != StatusInfo {
		t.Errorf("status = %v, want StatusInfo", section.Results[0].Status)
	}
	if section.Results[0].Label != "No config files to validate" {
		t.Errorf("label = %q, want %q", section.Results[0].Label, "No config files to validate")
	}
}

func TestCheckSchemaValidation_NoRecognisedKeys(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "custom.cue", `
something_else: "not a recognised key"
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result (info), got %d", len(section.Results))
	}
	if section.Results[0].Status != StatusInfo {
		t.Errorf("status = %v, want StatusInfo", section.Results[0].Status)
	}
}

func TestCheckSchemaValidation_SyntaxErrorSkipped(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "bad.cue", `this is not valid cue {{{`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	for _, r := range section.Results {
		if r.Status == StatusWarn || r.Status == StatusFail {
			t.Errorf("syntax error file should be skipped, got status %v: %s", r.Status, r.Message)
		}
	}
}

func TestCheckSchemaValidation_BadTagFormat(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "roles.cue", `
roles: "bad": {
	prompt: "test"
	tags: ["INVALID_TAG"]
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	found := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "roles.bad") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected StatusWarn for bad tag format, got results: %+v", section.Results)
	}
}

func TestCheckSchemaValidation_ValidSkills(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	if err := skills.Upsert(tmpDir, "finding/one-by-one", "github.com/p3bot/library/skills/finding/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := skills.Upsert(tmpDir, "review/pre-commit", "github.com/p3bot/library/skills/review/pre-commit@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	if len(section.Results) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(section.Results), section.Results)
	}
	if section.Results[0].Status != StatusPass {
		t.Errorf("status = %v, want StatusPass: %+v", section.Results[0].Status, section.Results[0])
	}
	if section.Results[0].Label != "skills.cue" {
		t.Errorf("label = %q, want %q", section.Results[0].Label, "skills.cue")
	}
	if section.Name != "Schema Validation" {
		t.Errorf("section = %q, want Schema Validation", section.Name)
	}
}

func TestCheckSchemaValidation_SkillsIssues(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)

	tests := []struct {
		name    string
		content string
		wantKey string
	}{
		{
			name: "missing origin",
			content: `skills: "finding/one-by-one": {
	version: "v1.0.0"
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "missing version",
			content: `skills: "finding/one-by-one": {
	origin: "github.com/p3bot/library/skills/finding/one-by-one@v1"
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "empty origin",
			content: `skills: "finding/one-by-one": {
	origin: ""
	version: "v1.0.0"
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "empty version",
			content: `skills: "finding/one-by-one": {
	origin: "github.com/p3bot/library/skills/finding/one-by-one@v1"
	version: ""
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "dests extra field",
			content: `skills: "finding/one-by-one": {
	origin: "github.com/p3bot/library/skills/finding/one-by-one@v1"
	version: "v1.0.0"
	dests: ["~/.agents/skills"]
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "targets extra field",
			content: `skills: "finding/one-by-one": {
	origin: "github.com/p3bot/library/skills/finding/one-by-one@v1"
	version: "v1.0.0"
	targets: ["claude"]
}
`,
			wantKey: "skills.finding/one-by-one",
		},
		{
			name: "skill-shaped without version",
			content: `skills: "finding/one-by-one": {
	description: "Walk findings one by one"
	file: "@module/SKILL.md"
}
`,
			wantKey: "skills.finding/one-by-one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			writeConfigFile(t, tmpDir, "skills.cue", tt.content)

			paths := config.Paths{
				Global:       tmpDir,
				GlobalExists: true,
				Local:        filepath.Join(tmpDir, "nonexistent"),
				LocalExists:  false,
			}

			section := CheckSchemaValidation(paths, schemas)

			found := false
			for _, r := range section.Results {
				if r.Status == StatusWarn && strings.Contains(r.Message, tt.wantKey) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected StatusWarn for %s, got results: %+v", tt.wantKey, section.Results)
			}
		})
	}
}

func TestCheckSchemaValidation_SkillsAbsentDefinitionSkipped(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	schemas.SkillInstall = cue.Value{}
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: "claude": {
	command: "claude {{.prompt}}"
}
`)
	writeConfigFile(t, tmpDir, "skills.cue", `
skills: "finding/one-by-one": {
	origin: "github.com/p3bot/library/skills/finding/one-by-one@v1"
	version: "v1.0.0"
	dests: ["~/.agents/skills"]
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	for _, r := range section.Results {
		if strings.Contains(r.Label, "skills") || strings.Contains(r.Message, "skills") {
			t.Errorf("missing #SkillInstall should skip skills, got %+v", r)
		}
		if r.Status == StatusWarn || r.Status == StatusFail {
			t.Errorf("absent definition must not fail Schema Validation, got %+v", r)
		}
	}

	if len(section.Results) != 1 || section.Results[0].Status != StatusPass || section.Results[0].Label != "agents.cue" {
		t.Errorf("expected agents.cue pass only, got %+v", section.Results)
	}
}

func TestCheckSchemaValidation_SkillsGlobalAndLocal(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "global")
	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := skills.Upsert(globalDir, "finding/one-by-one", "github.com/p3bot/library/skills/finding/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, localDir, "skills.cue", `
skills: "review/pre-commit": {
	origin: "github.com/p3bot/library/skills/review/pre-commit@v1"
	version: "v1.0.0"
	dests: ["~/.agents/skills"]
}
`)

	paths := config.Paths{
		Global:       globalDir,
		GlobalExists: true,
		Local:        localDir,
		LocalExists:  true,
	}

	section := CheckSchemaValidation(paths, schemas)

	var pass, warn bool
	for _, r := range section.Results {
		if r.Status == StatusPass && r.Label == "skills.cue" {
			pass = true
		}
		if r.Status == StatusWarn && strings.Contains(r.Message, "skills.review/pre-commit") {
			warn = true
		}
	}
	if !pass || !warn {
		t.Errorf("expected global pass and local warn, got %+v", section.Results)
	}
}

func TestCheckSchemaValidation_MixedValidAndInvalid(t *testing.T) {
	t.Parallel()
	schemas := testSchemaSet(t)
	tmpDir := t.TempDir()

	writeConfigFile(t, tmpDir, "agents.cue", `
agents: {
	"good": {
		command: "good-cmd"
	}
	"bad": {
		command: ""
	}
}
`)

	paths := config.Paths{
		Global:       tmpDir,
		GlobalExists: true,
		Local:        filepath.Join(tmpDir, "nonexistent"),
		LocalExists:  false,
	}

	section := CheckSchemaValidation(paths, schemas)

	hasWarn := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "agents.bad") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected StatusWarn for agents.bad, got results: %+v", section.Results)
	}
}
