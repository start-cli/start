package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/doctor"
	"github.com/p3bot/start/internal/registry"
)

func TestValidateDeriveRepoURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "default index path",
			input: "github.com/p3bot/library/index@v1",
			want:  "https://github.com/p3bot/library",
		},
		{
			name:  "canonical version",
			input: "github.com/p3bot/library/index@v1.0.1",
			want:  "https://github.com/p3bot/library",
		},
		{
			name:  "custom org",
			input: "github.com/example/custom-library/index@v0",
			want:  "https://github.com/example/custom-library",
		},
		{
			name:    "non-index subpath rejected",
			input:   "github.com/myorg/custom/registry@v0",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateDeriveRepoURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateDeriveRepoURL(%q) expected error, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateDeriveRepoURL(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("validateDeriveRepoURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateCacheDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/p3bot/library", "p3bot-library"},
		{"https://github.com/example/custom-library", "example-custom-library"},
		{"https://github.com/myorg/my-modules", "myorg-my-modules"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := validateCacheDirName(tt.input)
			if got != tt.want {
				t.Errorf("validateCacheDirName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateGitTagPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		category string
		name     string
		want     string
	}{
		{"agents", "claude", "agents/claude/"},
		{"roles", "golang", "roles/golang/"},
		{"tasks", "review/architecture", "tasks/review/architecture/"},
		{"contexts", "environment", "contexts/environment/"},
	}
	for _, tt := range tests {
		t.Run(tt.category+"/"+tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateGitTagPrefix(tt.category, tt.name)
			if got != tt.want {
				t.Errorf("validateGitTagPrefix(%q, %q) = %q, want %q", tt.category, tt.name, got, tt.want)
			}
		})
	}
}

func TestValidateTagVersions(t *testing.T) {
	t.Parallel()
	tags := []string{
		"agents/claude/v0.0.1",
		"agents/claude/v0.0.2",
		"agents/claude/v0.1.0",
		"agents/gemini/v0.1.0",
		"roles/golang/v0.1.0",
		"index/v0.1.8",
		"index/not-semver",
	}

	tests := []struct {
		prefix string
		want   []string
	}{
		{
			prefix: "agents/claude/",
			want:   []string{"v0.0.1", "v0.0.2", "v0.1.0"},
		},
		{
			prefix: "agents/gemini/",
			want:   []string{"v0.1.0"},
		},
		{
			prefix: "index/",
			want:   []string{"v0.1.8"},
		},
		{
			prefix: "tasks/missing/",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			t.Parallel()
			got := validateTagVersions(tags, tt.prefix)
			if len(got) != len(tt.want) {
				t.Fatalf("validateTagVersions(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("validateTagVersions(%q)[%d] = %q, want %q", tt.prefix, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidateLatestTagVersion(t *testing.T) {
	t.Parallel()
	tags := []string{
		"agents/claude/v0.0.1",
		"agents/claude/v0.0.2",
		"agents/claude/v0.1.0",
		"roles/golang/v0.2.0",
	}

	tests := []struct {
		prefix string
		want   string
	}{
		{"agents/claude/", "v0.1.0"},
		{"roles/golang/", "v0.2.0"},
		{"missing/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			t.Parallel()
			got := validateLatestTagVersion(tags, tt.prefix)
			if got != tt.want {
				t.Errorf("validateLatestTagVersion(%q) = %q, want %q", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestIndexVersionFromPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"github.com/p3bot/library/index@v1.0.1", "v1.0.1"},
		{"github.com/p3bot/library/index@v1", ""}, // major only — not canonical
		{"github.com/p3bot/library/index@v1.0.0", "v1.0.0"},
		{"no-version-here", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := indexVersionFromPath(tt.input)
			if got != tt.want {
				t.Errorf("indexVersionFromPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIndexEntryCount(t *testing.T) {
	t.Parallel()
	idx := makeTestRegistryIndex(3, 2, 4, 1)
	got := indexEntryCount(idx)
	want := 10
	if got != want {
		t.Errorf("indexEntryCount() = %d, want %d", got, want)
	}
}

func TestValidateFindFSModules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, p := range []string{
		"agents/claude/cue.mod",
		"agents/gemini/cue.mod",
		"agents/docs",
		"tasks/review/arch/cue.mod",
	} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("agents", func(t *testing.T) {
		got := validateFindFSModules("agents", dir)
		want := []string{"claude", "gemini"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("validateFindFSModules(agents) = %v, want %v", got, want)
		}
	})

	t.Run("tasks nested", func(t *testing.T) {
		got := validateFindFSModules("tasks", dir)
		want := []string{"review/arch"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("validateFindFSModules(tasks) = %v, want %v", got, want)
		}
	})

	t.Run("missing category", func(t *testing.T) {
		got := validateFindFSModules("roles", dir)
		if len(got) != 0 {
			t.Errorf("validateFindFSModules(roles) = %v, want empty", got)
		}
	})
}

func TestValidateIsStale(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@test.com")
	mustGit(t, dir, "config", "user.name", "Test")

	modDir := filepath.Join(dir, "agents", "claude")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "agent.cue"), []byte("package agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "initial")
	mustGit(t, dir, "tag", "agents/claude/v0.1.0")

	t.Run("not stale after tag", func(t *testing.T) {
		stale, err := validateIsStale(dir, "agents/claude/v0.1.0", "agents/claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stale {
			t.Error("expected not stale immediately after tag")
		}
	})

	if err := os.WriteFile(filepath.Join(modDir, "agent.cue"), []byte("package agent\n// updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "update agent")

	t.Run("stale after content change", func(t *testing.T) {
		stale, err := validateIsStale(dir, "agents/claude/v0.1.0", "agents/claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !stale {
			t.Error("expected stale after content change post-tag")
		}
	})

	t.Run("different path not stale", func(t *testing.T) {
		stale, err := validateIsStale(dir, "agents/claude/v0.1.0", "agents/other")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stale {
			t.Error("expected not stale for unrelated path")
		}
	})
}

// writeUsesModuleFixture creates a buildable, schema-less CUE module under
// cacheDir/<category>/<name> whose module-value file is `body`, mirroring the
// clone layout doctor validate walks.
func writeUsesModuleFixture(t *testing.T, cacheDir, category, name, body string) {
	t.Helper()
	moduleDir := filepath.Join(cacheDir, category, name)
	modDir := filepath.Join(moduleDir, "cue.mod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	moduleCue := "module: \"test.example/" + name + "@v0\"\nlanguage: version: \"v0.15.1\"\n"
	if err := os.WriteFile(filepath.Join(modDir, "module.cue"), []byte(moduleCue), 0o644); err != nil {
		t.Fatal(err)
	}
	singular := strings.TrimSuffix(category, "s")
	if err := os.WriteFile(filepath.Join(moduleDir, singular+".cue"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadModuleUses exercises the clone-loader against schema-less fixtures,
// including its failure modes. A build or descent failure surfaces as an error,
// which the per-module walk converts to a distinguishable "could not read uses"
// issue rather than propagating.
func TestLoadModuleUses(t *testing.T) {
	t.Parallel()

	t.Run("reads uses under module value", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeUsesModuleFixture(t, dir, "tasks", "publish", `package task

task: {
	description: "Publish"
	uses: ["contexts:start/library/publishing", "roles:go-expert"]
}
`)
		got, err := loadModuleUses(dir, "tasks", "publish", nil)
		if err != nil {
			t.Fatalf("loadModuleUses: %v", err)
		}
		want := []string{"contexts:start/library/publishing", "roles:go-expert"}
		if !stringSlicesEqual(got, want) {
			t.Errorf("uses = %v, want %v", got, want)
		}
	})

	t.Run("no uses field is empty, not error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeUsesModuleFixture(t, dir, "roles", "plain", `package role

role: {
	description: "Plain role"
}
`)
		got, err := loadModuleUses(dir, "roles", "plain", nil)
		if err != nil {
			t.Fatalf("loadModuleUses: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("uses = %v, want empty", got)
		}
	})

	t.Run("missing module value errors", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeUsesModuleFixture(t, dir, "tasks", "wrongkey", `package task

other: {
	description: "Not under the module key"
}
`)
		if _, err := loadModuleUses(dir, "tasks", "wrongkey", nil); err == nil {
			t.Error("expected error when module value is absent at both descent keys")
		}
	})

	t.Run("unbuildable CUE errors", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeUsesModuleFixture(t, dir, "tasks", "broken", `package task

task: {
	description: "Unterminated
`)
		if _, err := loadModuleUses(dir, "tasks", "broken", nil); err == nil {
			t.Error("expected error for unbuildable module CUE")
		}
	})
}

func TestValidateOneModule_CurrentIndexVersion(t *testing.T) {
	t.Parallel()

	const (
		category = "skills"
		name     = "example"
		named    = "v1.2.0"
	)
	prefix := validateGitTagPrefix(category, name)

	t.Run("older unpublished tag does not fail when named version is tagged and published", func(t *testing.T) {
		t.Parallel()
		dir := initValidateModuleRepo(t, category, name, "package skill\n")
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "commit", "-m", "initial")
		mustGit(t, dir, "tag", prefix+"v1.0.0")
		mustGit(t, dir, "tag", prefix+named)

		got := runValidateOneModule(t, category, name, named, []string{named}, []string{
			prefix + "v1.0.0",
			prefix + named,
		}, dir)
		if got.status != validateModulePass {
			t.Fatalf("status = %v issues = %v, want pass", got.status, got.issues)
		}
	})

	t.Run("named version tagged but not published fails", func(t *testing.T) {
		t.Parallel()
		got := runValidateOneModule(t, category, name, named, nil, []string{prefix + named}, t.TempDir())
		if got.status != validateModuleFail {
			t.Fatal("expected fail when named version is not on the registry")
		}
		if !issuesContain(got.issues, "index version "+named+" is not published on the registry") {
			t.Fatalf("issues = %v, want index version %s not published", got.issues, named)
		}
	})

	t.Run("latest published version with no git tag still fails", func(t *testing.T) {
		t.Parallel()
		got := runValidateOneModule(t, category, name, named, []string{named}, nil, t.TempDir())
		if got.status != validateModuleFail {
			t.Fatal("expected fail when latest published version has no git tag")
		}
		if !issuesContain(got.issues, "has no git tag") {
			t.Fatalf("issues = %v, want missing git tag for latest published", got.issues)
		}
	})

	t.Run("newer unpublished tag is not a never-published fail when HEAD matches named tag", func(t *testing.T) {
		t.Parallel()
		dir := initValidateModuleRepo(t, category, name, "package skill\n")
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "commit", "-m", "initial")
		mustGit(t, dir, "tag", prefix+named)
		mustGit(t, dir, "tag", prefix+"v1.3.0")

		got := runValidateOneModule(t, category, name, named, []string{named}, []string{
			prefix + named,
			prefix + "v1.3.0",
		}, dir)
		if issuesContain(got.issues, "never published") || issuesContain(got.issues, "is not published") {
			t.Fatalf("issues = %v, newer unpublished tag must not fail as unpublished", got.issues)
		}
		if got.status != validateModulePass {
			t.Fatalf("status = %v issues = %v, want pass when HEAD matches named tag", got.status, got.issues)
		}
	})

	t.Run("newer unpublished tag with drifted HEAD fails content-changed against named tag", func(t *testing.T) {
		t.Parallel()
		dir := initValidateModuleRepo(t, category, name, "package skill\n")
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "commit", "-m", "initial")
		mustGit(t, dir, "tag", prefix+named)

		modFile := filepath.Join(dir, category, name, "skill.cue")
		if err := os.WriteFile(modFile, []byte("package skill\n// drifted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "commit", "-m", "drift")
		mustGit(t, dir, "tag", prefix+"v1.3.0")

		got := runValidateOneModule(t, category, name, named, []string{named}, []string{
			prefix + named,
			prefix + "v1.3.0",
		}, dir)
		if got.status != validateModuleFail {
			t.Fatal("expected fail when HEAD drifted from the named tag")
		}
		if issuesContain(got.issues, "never published") || issuesContain(got.issues, "is not published") {
			t.Fatalf("issues = %v, must not fail as unpublished", got.issues)
		}
		want := "content changed since " + prefix + named
		if !issuesContain(got.issues, want) {
			t.Fatalf("issues = %v, want %q", got.issues, want)
		}
	})
}

func initValidateModuleRepo(t *testing.T, category, name, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@test.com")
	mustGit(t, dir, "config", "user.name", "Test")
	modDir := filepath.Join(dir, category, name)
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "skill.cue"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runValidateOneModule(t *testing.T, category, name, version string, published, tags []string, cacheDir string) validateModuleResult {
	t.Helper()
	module := "github.com/p3bot/library/" + category + "/" + name + "@v1"
	entry := registry.IndexEntry{Module: module, Version: version}
	idx := &registry.Index{
		Skills: map[string]registry.IndexEntry{name: entry},
	}
	stub := newRegistryStub(idx, "")
	stub.SetVersions(module, published, nil)
	return validateOneModule(context.Background(), stub, idx, category, name, entry, tags, cacheDir)
}

func issuesContain(issues []string, substr string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, substr) {
			return true
		}
	}
	return false
}

func TestValidateOneModule_SkillsSkipUses(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	moduleDir := filepath.Join(cacheDir, "skills", "workflows", "one-by-one")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "skill.cue"), []byte("package onebyone\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := &registry.Index{
		Skills: map[string]registry.IndexEntry{
			"workflows/one-by-one": {
				Module:  "github.com/p3bot/library/skills/workflows/one-by-one@v1",
				Version: stubVersion,
			},
		},
	}
	got := validateOneModule(context.Background(), newRegistryStub(idx, ""), idx, "skills", "workflows/one-by-one", idx.Skills["workflows/one-by-one"], nil, cacheDir)
	for _, issue := range got.issues {
		if strings.Contains(issue, "uses") {
			t.Fatalf("skills must skip uses check, got %q", issue)
		}
	}
}

func TestValidateSkillLeafUniqueness(t *testing.T) {
	t.Parallel()
	entries := map[string]registry.IndexEntry{
		"workflows/one-by-one": {},
		"review/one-by-one":    {},
		"review/pre-commit":    {},
	}

	issues := validateSkillLeafUniqueness("workflows/one-by-one", entries)
	if len(issues) != 1 || !strings.Contains(issues[0], "review/one-by-one") {
		t.Fatalf("collision = %v", issues)
	}
	if issues := validateSkillLeafUniqueness("review/pre-commit", entries); len(issues) != 0 {
		t.Fatalf("unique leaf = %v", issues)
	}
}

// TestValidateUsesReferences exercises the index-resolver: a resolvable entry
// passes, while malformed and unresolvable entries each yield one issue.
func TestValidateUsesReferences(t *testing.T) {
	t.Parallel()

	idx := &registry.Index{
		Contexts: map[string]registry.IndexEntry{
			"start/library/publishing": {Module: "github.com/p3bot/library/contexts/start/library/publishing@v1"},
		},
		Roles: map[string]registry.IndexEntry{
			"go-expert": {Module: "github.com/p3bot/library/roles/go-expert@v1"},
		},
	}

	tests := []struct {
		name      string
		uses      []string
		wantCount int
		wantMsg   string
	}{
		{
			name:      "resolves",
			uses:      []string{"contexts:start/library/publishing", "roles:go-expert"},
			wantCount: 0,
		},
		{
			name:      "case-insensitive whole-name match",
			uses:      []string{"contexts:START/library/Publishing"},
			wantCount: 0,
		},
		{
			name:      "missing colon is malformed",
			uses:      []string{"publishing"},
			wantCount: 1,
			wantMsg:   "not a fully-qualified",
		},
		{
			name:      "unknown category is invalid",
			uses:      []string{"widgets:foo"},
			wantCount: 1,
			wantMsg:   "invalid uses reference",
		},
		{
			name:      "absent module does not resolve",
			uses:      []string{"contexts:does/not/exist"},
			wantCount: 1,
			wantMsg:   "resolves to no module",
		},
		{
			name:      "no substring or prefix match",
			uses:      []string{"contexts:start/library"},
			wantCount: 1,
			wantMsg:   "resolves to no module",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			issues := validateUsesReferences("declaring-module", tt.uses, idx)
			if len(issues) != tt.wantCount {
				t.Fatalf("issues = %v, want %d", issues, tt.wantCount)
			}
			if tt.wantMsg != "" {
				if !strings.Contains(issues[0], tt.wantMsg) {
					t.Errorf("issue %q missing %q", issues[0], tt.wantMsg)
				}
				if !strings.Contains(issues[0], "declaring-module") {
					t.Errorf("issue %q does not name the declaring module", issues[0])
				}
			}
		})
	}
}

func TestPrintValidateStats(t *testing.T) {
	t.Parallel()

	t.Run("all pass", func(t *testing.T) {
		t.Parallel()
		cats := []validateCatResult{
			{name: "agents", modules: []validateModuleResult{
				{status: validateModulePass},
				{status: validateModulePass},
			}},
		}
		var buf bytes.Buffer
		hasFailure := printValidateStats(&buf, cats)
		if hasFailure {
			t.Error("expected no failure for all-pass")
		}
		out := buf.String()
		if !strings.Contains(out, "Checked:") {
			t.Errorf("output missing 'Checked:': %q", out)
		}
	})

	t.Run("with failures", func(t *testing.T) {
		t.Parallel()
		cats := []validateCatResult{
			{name: "agents", modules: []validateModuleResult{
				{status: validateModulePass},
				{status: validateModuleFail},
			}},
		}
		var buf bytes.Buffer
		hasFailure := printValidateStats(&buf, cats)
		if !hasFailure {
			t.Error("expected hasFailure=true")
		}
	})
}

func TestPrintValidateModulesDefault(t *testing.T) {
	t.Parallel()
	cats := []validateCatResult{
		{
			name: "agents",
			modules: []validateModuleResult{
				{name: "claude", version: "v0.1.0", status: validateModulePass},
				{name: "gemini", version: "v0.1.0", status: validateModulePass},
			},
		},
		{
			name: "contexts",
			modules: []validateModuleResult{
				{name: "environment", version: "v0.1.0", status: validateModulePass},
				{name: "project", version: "v0.1.0", status: validateModuleFail, issues: []string{"stale: content changed"}},
			},
		},
	}

	var buf bytes.Buffer
	printValidateModules(&buf, cats, false)
	out := buf.String()

	if !strings.Contains(out, "2/2 OK") {
		t.Errorf("expected agents 2/2 OK in output: %q", out)
	}
	if !strings.Contains(out, "1/2 FAIL") {
		t.Errorf("expected contexts 1/2 FAIL in output: %q", out)
	}
	if !strings.Contains(out, "project") {
		t.Errorf("expected failing module 'project' listed in output: %q", out)
	}
	if strings.Contains(out, "claude") {
		t.Errorf("passing module 'claude' should not appear in default output: %q", out)
	}
}

func TestPrintValidateModulesVerbose(t *testing.T) {
	t.Parallel()
	cats := []validateCatResult{
		{
			name: "agents",
			modules: []validateModuleResult{
				{name: "claude", version: "v0.1.0", status: validateModulePass},
				{name: "gemini", version: "v0.2.0", status: validateModuleFail, issues: []string{"index version mismatch"}},
			},
		},
	}

	var buf bytes.Buffer
	printValidateModules(&buf, cats, true)
	out := buf.String()

	if !strings.Contains(out, "claude") {
		t.Errorf("verbose output should include passing module 'claude': %q", out)
	}
	if !strings.Contains(out, "gemini") {
		t.Errorf("verbose output should include failing module 'gemini': %q", out)
	}
	if !strings.Contains(out, "index version mismatch") {
		t.Errorf("verbose output should include issue detail: %q", out)
	}
}

func TestValidateError(t *testing.T) {
	t.Parallel()
	err := &validateError{}
	if IsSilentError(err) != true {
		t.Error("validateError should be a silent error")
	}
	if err.Error() == "" {
		t.Error("validateError.Error() should not be empty")
	}
}

// The --force gate shields public infrastructure: without it the command prints
// guidance and does no registry work; with it the command reaches prerequisites.
func TestDoctorValidateGate(t *testing.T) {
	t.Run("without --force shows guidance and does no registry work", func(t *testing.T) {
		_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

		out, err := captureText(t, stub, "doctor", "validate")
		if err != nil {
			t.Fatalf("gate path should return nil, got %v", err)
		}
		if !strings.Contains(out, "Run with --force to proceed.") {
			t.Errorf("expected gate guidance in output, got: %q", out)
		}
		// The gate returns before getProvider(cmd)(), so no client is built.
		if stub.providerCalls != 0 {
			t.Errorf("gate must not construct a registry client; providerCalls = %d, want 0", stub.providerCalls)
		}
	})

	t.Run("with --force passes the gate and reaches prerequisites", func(t *testing.T) {
		_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
		// Empty PATH makes the first post-gate prerequisite (git) fail offline,
		// proving the gate was passed without network or clone work.
		t.Setenv("PATH", "")

		out, err := captureText(t, stub, "doctor", "validate", "--force")
		if err == nil {
			t.Fatal("expected the git-prerequisite error past the gate, got nil")
		}
		if !strings.Contains(err.Error(), "git not found") {
			t.Errorf("expected git-not-found prerequisite error past the gate, got: %v", err)
		}
		if strings.Contains(out, "Run with --force to proceed.") {
			t.Errorf("gate guidance must not print when --force is set, got: %q", out)
		}
	})
}

// makeTestRegistryIndex creates a *registry.Index with n stub entries per category.
func makeTestRegistryIndex(agents, roles, contexts, tasks int) *registry.Index {
	idx := &registry.Index{
		Agents:   make(map[string]registry.IndexEntry, agents),
		Roles:    make(map[string]registry.IndexEntry, roles),
		Contexts: make(map[string]registry.IndexEntry, contexts),
		Tasks:    make(map[string]registry.IndexEntry, tasks),
	}
	for i := range agents {
		idx.Agents[fmt.Sprintf("agent%d", i)] = registry.IndexEntry{Version: "v0.1.0"}
	}
	for i := range roles {
		idx.Roles[fmt.Sprintf("role%d", i)] = registry.IndexEntry{Version: "v0.1.0"}
	}
	for i := range contexts {
		idx.Contexts[fmt.Sprintf("ctx%d", i)] = registry.IndexEntry{Version: "v0.1.0"}
	}
	for i := range tasks {
		idx.Tasks[fmt.Sprintf("task%d", i)] = registry.IndexEntry{Version: "v0.1.0"}
	}
	return idx
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestValidateWalkModules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, p := range []string{
		"claude/cue.mod",
		"review/arch/cue.mod",
		"review/perf/cue.mod",
		"docs",
	} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var found []string
	if err := validateWalkModules(dir, "", func(rel string) {
		found = append(found, rel)
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"claude":      true,
		"review/arch": true,
		"review/perf": true,
	}
	if len(found) != len(want) {
		t.Fatalf("found %v, want keys of %v", found, want)
	}
	for _, f := range found {
		if !want[f] {
			t.Errorf("unexpected module found: %q", f)
		}
	}
}

// Major-only version paths (e.g. @v0) return nil without touching the client.
func TestValidateCheckIndexVersionExistsNoop(t *testing.T) {
	t.Parallel()
	client, err := registry.NewClient()
	if err != nil {
		t.Skipf("skipping: cannot create registry client: %v", err)
	}
	ctx := context.Background()
	paths := []string{
		"github.com/p3bot/library/index@v1",
		"github.com/p3bot/library/index@v2",
		"github.com/p3bot/library/index",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			if err := validateCheckIndexVersionExists(ctx, client, path); err != nil {
				t.Errorf("validateCheckIndexVersionExists(%q) = %v, want nil", path, err)
			}
		})
	}
}

func TestPrintValidateStatsOutput(t *testing.T) {
	t.Parallel()
	cats := []validateCatResult{
		{name: "agents", modules: []validateModuleResult{
			{status: validateModulePass},
			{status: validateModulePass},
			{status: validateModuleFail},
		}},
		{name: "roles", modules: []validateModuleResult{
			{status: validateModulePass},
		}},
	}

	var buf bytes.Buffer
	printValidateStats(&buf, cats)
	out := buf.String()

	for _, want := range []string{"Checked:", "Pass:", "Fail:"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats output missing %q: %q", want, out)
		}
	}
}

func TestOutputValidateJSON(t *testing.T) {
	t.Parallel()

	indexSection := doctor.SectionResult{
		Name: "Index",
		Results: []doctor.CheckResult{
			{Status: doctor.StatusPass, Label: "Valid", Message: "v0.1.8"},
		},
	}

	cats := []validateCatResult{
		{name: "agents", modules: []validateModuleResult{
			{name: "ai/claude", version: "v0.2.0", status: validateModulePass},
			{name: "ai/gemini", version: "v0.1.0", status: validateModuleFail, issues: []string{"index version mismatch"}},
		}},
		{name: "roles", modules: []validateModuleResult{
			{name: "golang", version: "v0.1.0", status: validateModulePass},
		}},
	}

	var buf bytes.Buffer
	err := outputValidateJSON(&buf, indexSection, cats)
	if err != nil {
		t.Fatalf("outputValidateJSON failed: %v", err)
	}

	var result ValidateResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(result.Index.Checks) != 1 {
		t.Fatalf("expected 1 index check, got %d", len(result.Index.Checks))
	}
	if result.Index.Checks[0].Status != "pass" {
		t.Errorf("expected index check status 'pass', got %q", result.Index.Checks[0].Status)
	}
	if result.Index.Checks[0].Message != "v0.1.8" {
		t.Errorf("expected index check message 'v0.1.8', got %q", result.Index.Checks[0].Message)
	}

	if len(result.Categories) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(result.Categories))
	}
	if result.Categories[0].Name != "agents" {
		t.Errorf("expected first category 'agents', got %q", result.Categories[0].Name)
	}
	if len(result.Categories[0].Modules) != 2 {
		t.Fatalf("expected 2 agent modules, got %d", len(result.Categories[0].Modules))
	}
	if result.Categories[0].Modules[1].Status != "fail" {
		t.Errorf("expected gemini status 'fail', got %q", result.Categories[0].Modules[1].Status)
	}
	if len(result.Categories[0].Modules[1].Issues) != 1 {
		t.Errorf("expected 1 issue for gemini, got %d", len(result.Categories[0].Modules[1].Issues))
	}

	if result.Stats.Checked != 3 {
		t.Errorf("expected 3 checked, got %d", result.Stats.Checked)
	}
	if result.Stats.Pass != 2 {
		t.Errorf("expected 2 pass, got %d", result.Stats.Pass)
	}
	if result.Stats.Fail != 1 {
		t.Errorf("expected 1 fail, got %d", result.Stats.Fail)
	}
}

func TestOutputValidateJSONNilCategories(t *testing.T) {
	t.Parallel()

	indexSection := doctor.SectionResult{
		Name: "Index",
		Results: []doctor.CheckResult{
			{Status: doctor.StatusFail, Label: "Unreachable", Message: "cannot resolve"},
		},
	}

	var buf bytes.Buffer
	err := outputValidateJSON(&buf, indexSection, nil)
	if err != nil {
		t.Fatalf("outputValidateJSON failed: %v", err)
	}

	var result ValidateResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(result.Index.Checks) != 1 {
		t.Fatalf("expected 1 index check, got %d", len(result.Index.Checks))
	}
	if result.Index.Checks[0].Status != "fail" {
		t.Errorf("expected 'fail', got %q", result.Index.Checks[0].Status)
	}
	if len(result.Categories) != 0 {
		t.Errorf("expected empty categories, got %d", len(result.Categories))
	}
	if result.Stats.Checked != 0 {
		t.Errorf("expected 0 checked, got %d", result.Stats.Checked)
	}
}

func TestValidateHasFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cats []validateCatResult
		want bool
	}{
		{
			name: "all pass",
			cats: []validateCatResult{
				{name: "agents", modules: []validateModuleResult{
					{status: validateModulePass},
				}},
			},
			want: false,
		},
		{
			name: "has failure",
			cats: []validateCatResult{
				{name: "agents", modules: []validateModuleResult{
					{status: validateModulePass},
					{status: validateModuleFail},
				}},
			},
			want: true,
		},
		{
			name: "empty",
			cats: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validateHasFailure(tt.cats)
			if got != tt.want {
				t.Errorf("validateHasFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}
