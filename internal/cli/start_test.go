package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/tui"
)

// chdir changes to the given directory and registers a cleanup to restore the original.
func chdir(t *testing.T, dir string) {
	t.Helper()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("changing to dir %s: %v", dir, err)
	}
}

// isolateConfigEnv points HOME, XDG_CONFIG_HOME, and XDG_CACHE_HOME at a fresh
// temp dir and registers the read-only-cache cleanup, without writing any seed
// config. It returns the temp dir so callers can build their own config under
// it. This is the single definition of environment isolation shared by both
// the seeded tests (via setupStartTestConfig) and the from-scratch
// config-workflow tests, so a test produces the same result run alone as in
// the full suite. Routing a test through it is not behaviour-neutral: it also
// isolates HOME and points XDG_CACHE_HOME at an empty cache, so migrate a test
// onto it only when it is order-dependent or leaks, not on cosmetic grounds.
func isolateConfigEnv(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	// os.UserCacheDir prefers XDG_CACHE_HOME over $HOME/.cache, so redirect it
	// too; call-count tests depend on the cache being absent.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	// CUE module cache writes read-only files; make them writable before cleanup.
	t.Cleanup(func() {
		_ = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})

	return tmpDir
}

// setupStartTestConfig creates a minimal CUE config for start command testing.
func setupStartTestConfig(t *testing.T) string {
	t.Helper()
	tmpDir := isolateConfigEnv(t)

	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: {
			default: "echo-model"
		}
	}
}

roles: {
	assistant: {
		prompt: "You are a helpful assistant."
	}
}

contexts: {
	env: {
		required: true
		prompt: "Environment context"
	}
	project: {
		default: true
		prompt: "Project context"
	}
}

tasks: {
	"test-task": {
		role: "assistant"
		prompt: """
			Test task prompt.
			Instructions: {{.instructions}}
			"""
	}
}

settings: {
	default_agent: "echo"
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return tmpDir
}

// Note: Tests below use os.Chdir (process-global state). Do not add t.Parallel()
// to any test that calls os.Chdir — it will cause data races on the working directory.

func TestExecuteStart_DryRun(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		IncludeDefaults: true,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "Dry Run") {
		t.Errorf("Expected 'Dry Run' in output, got:\n%s", output)
	}

	if !strings.Contains(output, "echo") {
		t.Errorf("Expected agent 'echo' in output")
	}

	if !strings.Contains(output, "assistant") {
		t.Errorf("Expected role 'assistant' in output")
	}
}

func TestExecuteStart_NoRole(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true, NoRole: true}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		IncludeDefaults: true,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "Dry Run") {
		t.Errorf("Expected 'Dry Run' in output, got:\n%s", output)
	}

	if !strings.Contains(output, "echo") {
		t.Errorf("Expected agent 'echo' in output")
	}

	if !strings.Contains(output, "env") {
		t.Errorf("Expected context 'env' in output")
	}

	if strings.Contains(output, "You are a helpful assistant") {
		t.Errorf("Expected no role content in output, got:\n%s", output)
	}

	if strings.Contains(output, "assistant") {
		t.Errorf("Expected no role name 'assistant' in output, got:\n%s", output)
	}

	// The opt-out must be explicit: a skipped line naming the flag that caused it.
	if !strings.Contains(output, "skipped") || !strings.Contains(output, roleSkipReason) {
		t.Errorf("Expected skipped role line naming %q, got:\n%s", roleSkipReason, output)
	}
}

func TestExecuteTask_NoRole(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true, NoRole: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", "focus on testing")
	if err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "test-task") {
		t.Errorf("Expected task name in output")
	}

	if !strings.Contains(output, "focus on testing") {
		t.Errorf("Expected instructions in output")
	}

	// Task has role: "assistant" configured, which the role-skip state must suppress.
	if strings.Contains(output, "You are a helpful assistant") {
		t.Errorf("Expected no role content with role skip, got:\n%s", output)
	}

	// The opt-out must be explicit: a skipped line naming the flag that caused it.
	if !strings.Contains(output, "skipped") || !strings.Contains(output, roleSkipReason) {
		t.Errorf("Expected skipped role line naming %q, got:\n%s", roleSkipReason, output)
	}
}

func TestExecuteTask_MissingTaskRole(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	// Task references a role that does not exist.
	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: {
			default: "echo-model"
		}
	}
}

roles: {}

tasks: {
	"test-task": {
		role: "missing-role"
		prompt: "Test task prompt."
	}
}

settings: {
	default_agent: "echo"
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", "")
	if err == nil {
		t.Fatal("Expected error for missing task role, got nil")
	}

	// The resolver should have been invoked and failed (no registry in tests).
	if !strings.Contains(err.Error(), "missing-role") {
		t.Errorf("Expected error to mention %q, got: %v", "missing-role", err)
	}
}

// writeNoRolesConfig writes a config with no roles defined and chdirs into it,
// for exercising the SectionNone role-header path end to end.
func writeNoRolesConfig(t *testing.T) {
	t.Helper()
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: { default: "echo-model" }
	}
}

contexts: {
	env: { required: true, prompt: "Environment context" }
}

tasks: {
	"test-task": { prompt: "Test task prompt." }
}

settings: { default_agent: "echo" }
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, tmpDir)
}

// writeNoContextsConfig writes a config with no contexts defined and chdirs into
// it, for exercising the SectionNone and SectionSkipped context-header paths end
// to end. It keeps a role and task so only the context section is empty.
func writeNoContextsConfig(t *testing.T) {
	t.Helper()
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: { default: "echo-model" }
	}
}

roles: {
	assistant: { prompt: "You are a helpful assistant." }
}

tasks: {
	"test-task": { role: "assistant", prompt: "Test task prompt." }
}

settings: { default_agent: "echo" }
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, tmpDir)
}

// TestExecuteStart_NoContextsConfig asserts the start header reports
// "Context: none" when no contexts are configured, guarding the call site
// against silent rendering.
func TestExecuteStart_NoContextsConfig(t *testing.T) {
	writeNoContextsConfig(t)

	flags := &Flags{DryRun: true}
	selection := orchestration.ContextSelection{IncludeRequired: true, IncludeDefaults: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, ""); err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Context: none") {
		t.Errorf("Expected 'Context: none' in output, got:\n%s", output)
	}
	if strings.Contains(output, "Context: skipped") {
		t.Errorf("no-context state must not read as a skip, got:\n%s", output)
	}
}

// TestExecuteTask_NoContextsConfig mirrors the start case for the task header.
func TestExecuteTask_NoContextsConfig(t *testing.T) {
	writeNoContextsConfig(t)

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", ""); err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Context: none") {
		t.Errorf("Expected 'Context: none' in output, got:\n%s", output)
	}
}

// TestExecuteStart_ContextNoneOptOut asserts that --context none with no explicit
// selectors and no default contexts reports the deliberate opt-out, naming the
// flag, distinct from the plain no-contexts line.
func TestExecuteStart_ContextNoneOptOut(t *testing.T) {
	writeNoContextsConfig(t)

	flags := &Flags{DryRun: true, NoImplicitContexts: true}
	selection := orchestration.ContextSelection{IncludeRequired: true, IncludeDefaults: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, ""); err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Context: skipped") {
		t.Errorf("Expected 'Context: skipped' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "via --context none") {
		t.Errorf("Expected opt-out reason 'via --context none' in output, got:\n%s", output)
	}
	if strings.Contains(output, "Context: none") {
		t.Errorf("opt-out must not read as the neutral none line, got:\n%s", output)
	}
}

// TestExecuteTask_ContextNoneOptOut mirrors the start opt-out case for the task
// header, guarding the task path's SuppressImplicit wiring.
func TestExecuteTask_ContextNoneOptOut(t *testing.T) {
	writeNoContextsConfig(t)

	flags := &Flags{DryRun: true, NoImplicitContexts: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", ""); err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Context: skipped") {
		t.Errorf("Expected 'Context: skipped' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "via --context none") {
		t.Errorf("Expected opt-out reason 'via --context none' in output, got:\n%s", output)
	}
	if strings.Contains(output, "Context: none") {
		t.Errorf("opt-out must not read as the neutral none line, got:\n%s", output)
	}
}

// TestExecuteStart_NoRolesConfig asserts the start header reports "Role: none"
// when no roles are configured, guarding the call site against silent rendering.
func TestExecuteStart_NoRolesConfig(t *testing.T) {
	writeNoRolesConfig(t)

	flags := &Flags{DryRun: true}
	selection := orchestration.ContextSelection{IncludeRequired: true, IncludeDefaults: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, ""); err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Role: none") {
		t.Errorf("Expected 'Role: none' in output, got:\n%s", output)
	}
	if strings.Contains(output, "skipped") {
		t.Errorf("no-role state must not read as a skip, got:\n%s", output)
	}
}

// TestExecuteTask_NoRolesConfig mirrors the start case for the task header.
func TestExecuteTask_NoRolesConfig(t *testing.T) {
	writeNoRolesConfig(t)

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	if err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", ""); err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Role: none") {
		t.Errorf("Expected 'Role: none' in output, got:\n%s", output)
	}
}

// TestExecuteStart_RoleFailureRendersTable drives a required-role failure through
// printComposeError and asserts the diagnostic role row still renders — the
// section is SectionListed, not collapsed to "Role: none".
func TestExecuteStart_RoleFailureRendersTable(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	// A required (non-optional) role referencing a missing file fails resolution.
	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: { default: "echo-model" }
	}
}

roles: {
	broken: { file: "./does-not-exist.md" }
}

settings: { default_agent: "echo" }
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}
	selection := orchestration.ContextSelection{IncludeRequired: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err == nil {
		t.Fatal("Expected error for missing required role, got nil")
	}

	output := stdout.String()
	if !strings.Contains(output, "broken") {
		t.Errorf("Expected failing role 'broken' in diagnostic table, got:\n%s", output)
	}
	if strings.Contains(output, "Role: none") {
		t.Errorf("Role failure must not collapse to 'Role: none', got:\n%s", output)
	}
}

func TestExecuteTask_AmbiguousTaskRole(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	// Two roles share the short name "assistant" and a task references it; the
	// resolver should return an actionable "ambiguous" error.
	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
		default_model: "default"
		models: {
			default: "echo-model"
		}
	}
}

roles: {
	"golang/assistant": {
		file: ""
		description: "Go assistant"
	}
	"personal/assistant": {
		file: ""
		description: "Personal assistant"
	}
}

tasks: {
	"test-task": {
		role: "assistant"
		prompt: "Test task prompt."
	}
}

settings: {
	default_agent: "echo"
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, tmpDir)

	flags := &Flags{DryRun: true, Quiet: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", "")
	if err == nil {
		t.Fatal("Expected error for ambiguous task role, got nil")
	}

	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("Expected ambiguous error, got: %v", err)
	}
}

// TestResolver_CfgStaleReloadSurfacesModule covers the cfgStale lifecycle the
// task surface's reload gates rely on: a fresh resolver is not stale, and
// reloadConfig both clears the flag and surfaces a module written to disk after
// the resolver's config was first loaded — the visibility an auto-installed
// task-declared role needs before ComposeWithRole runs. The executeTask gating
// that consumes this (reload iff cfgStale) is not exercised here: a successful
// auto-install needs the live registry, which the offline test binary lacks.
func TestResolver_CfgStaleReloadSurfacesModule(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "settings.cue")

	discard := io.Discard
	stdin := strings.NewReader("")
	flags := &Flags{}

	// Initial config has no roles, as before an install.
	if err := os.WriteFile(configFile, []byte("roles: {}\nsettings: {default_agent: \"echo\"}\n"), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := loadMergedConfigFromDirWithDebug(discard, discard, stdin, tmpDir, flags)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	r := newResolver(cfg, flags, discard, discard, stdin)
	if r.cfgStale {
		t.Fatal("fresh resolver should not be cfgStale")
	}
	if _, err := r.resolveRole("assistant"); err == nil {
		t.Fatal("role should be unresolvable before it is installed")
	}

	// Simulate an auto-install: the role lands on disk and the install marks the
	// cached config stale.
	r.cfgStale = true
	if err := os.WriteFile(configFile, []byte("roles: {assistant: {prompt: \"You are helpful.\"}}\nsettings: {default_agent: \"echo\"}\n"), 0o644); err != nil {
		t.Fatalf("rewriting config: %v", err)
	}

	if err := r.reloadConfig(tmpDir); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if r.cfgStale {
		t.Error("reloadConfig should clear cfgStale")
	}
	got, err := r.resolveRole("assistant")
	if err != nil {
		t.Fatalf("role should resolve after reload: %v", err)
	}
	if got != "assistant" {
		t.Errorf("resolveRole = %q, want %q", got, "assistant")
	}
}

func TestExecuteStart_ContextSelection(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}

	tests := []struct {
		name        string
		selection   orchestration.ContextSelection
		wantContext string
	}{
		{
			name: "required and default",
			selection: orchestration.ContextSelection{
				IncludeRequired: true,
				IncludeDefaults: true,
			},
			wantContext: "env",
		},
		{
			name: "required only",
			selection: orchestration.ContextSelection{
				IncludeRequired: true,
				IncludeDefaults: false,
			},
			wantContext: "env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)

			err := executeStart(stdout, stderr, strings.NewReader(""), flags, tt.selection, "")
			if err != nil {
				t.Fatalf("executeStart() error = %v", err)
			}

			output := stdout.String()
			if !strings.Contains(output, tt.wantContext) {
				t.Errorf("Expected context %q in output", tt.wantContext)
			}
		})
	}
}

func TestExecuteTask_DryRun(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "test-task", "focus on testing")
	if err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "test-task") {
		t.Errorf("Expected task name in output")
	}

	if !strings.Contains(output, "focus on testing") {
		t.Errorf("Expected instructions in output")
	}

	if !strings.Contains(output, "Dry Run") {
		t.Errorf("Expected 'Dry Run' in output")
	}
}

// TestExecuteTask_RemoteLocator drives the task surface end-to-end with an
// http(s) task body: the remote content is fetched, {{.instructions}} is
// substituted, and the locator is shown as the task name. The dry-run path still
// performs the fetch, matching how a local task file is read.
func TestExecuteTask_RemoteLocator(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "Remote task body.\nInstructions: {{.instructions}}\n")
	}))
	defer srv.Close()

	flags := &Flags{DryRun: true, NoRole: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	url := srv.URL + "/task.md"
	if err := executeTask(stdout, stderr, strings.NewReader(""), flags, url, "focus on testing"); err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, url) {
		t.Errorf("expected the locator %q as the task name, got:\n%s", url, output)
	}
	if !strings.Contains(output, "Remote task body.") {
		t.Errorf("expected fetched task body in output, got:\n%s", output)
	}
	if !strings.Contains(output, "focus on testing") {
		t.Errorf("expected {{.instructions}} substitution in output, got:\n%s", output)
	}
}

func TestPrintDryRunSummary(t *testing.T) {
	buf := new(bytes.Buffer)

	agent := orchestration.Agent{
		Name:         "test-agent",
		DefaultModel: "test-model",
	}

	result := orchestration.ComposeResult{
		Role:     "You are a test assistant.",
		RoleName: "test-role",
		Prompt:   "Test prompt content",
		Contexts: []orchestration.Context{
			{Name: "ctx1", File: "file1.md", Default: true},
			{Name: "ctx2", File: "file2.md"},
		},
		RoleResolutions: []orchestration.RoleResolution{
			{Name: "test-role", Status: "loaded", File: "test-role.md"},
		},
		RoleOutcome:    orchestration.SectionOutcome{State: orchestration.SectionListed},
		ContextOutcome: orchestration.SectionOutcome{State: orchestration.SectionListed},
	}

	printDryRunSummary(buf, agent, "", "", result, "/tmp/test-dir")

	output := buf.String()

	expectedStrings := []string{
		"Dry Run",
		"test-agent",
		"test-role",
		"Context:",
		"ctx1",
		"ctx2",
		"/tmp/test-dir",
		"role.md",
		"prompt.md",
		"command.txt",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected %q in output", expected)
		}
	}
}

func TestPrintContentPreview(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		maxLines      int
		wantTruncated bool
	}{
		{
			name:          "fewer lines than limit shows no count",
			text:          "line1\nline2",
			maxLines:      5,
			wantTruncated: false,
		},
		{
			name:          "lines within threshold shows all",
			text:          "line1\nline2\nline3\nline4\nline5\nline6",
			maxLines:      3,
			wantTruncated: false,
		},
		{
			name:          "lines beyond threshold truncates",
			text:          "line1\nline2\nline3\nline4\nline5\nline6\nline7",
			maxLines:      3,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := new(bytes.Buffer)
			printContentPreview(buf, "Test", tui.ColorDim, tt.text, tt.maxLines)
			output := buf.String()

			if tt.wantTruncated {
				if !strings.Contains(output, fmt.Sprintf("(%d lines)", tt.maxLines)) {
					t.Errorf("Expected truncated header with line count, got: %s", output)
				}
				if !strings.Contains(output, "... (") {
					t.Errorf("Expected '... (X more lines)' suffix, got: %s", output)
				}
			} else {
				if strings.Contains(output, "lines)") {
					t.Errorf("Expected no line count for short content, got: %s", output)
				}
			}
		})
	}
}

func TestTaskResolution(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	taskScope := singleCategoryScope("tasks", "task", false)

	t.Run("exact match", func(t *testing.T) {
		r := newTestResolver(cfg)
		outcome, err := r.resolve("test-task", taskScope)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.match.Name != "test-task" {
			t.Errorf("resolve(test-task) = %q, want %q", outcome.match.Name, "test-task")
		}
	})

	t.Run("substring match", func(t *testing.T) {
		r := newTestResolver(cfg)
		outcome, err := r.resolve("test", taskScope)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.match.Name != "test-task" {
			t.Errorf("resolve(test) = %q, want %q", outcome.match.Name, "test-task")
		}
	})

	t.Run("no match", func(t *testing.T) {
		r := newTestResolver(cfg)
		_, err := r.resolve("nonexistent", taskScope)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("resolve(nonexistent) error = %v, want not-found", err)
		}
	})
}

// TestTaskResolution_AmbiguousSubstring verifies a bare term matching several
// tasks is a non-TTY ambiguity error (multi-term AND is a search concern, gone
// from resolution).
func TestTaskResolution_AmbiguousSubstring(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate from global config
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} test"
	}
}

tasks: {
	"review-code": {
		prompt: "Review code"
	}
	"review-docs": {
		prompt: "Review documentation"
	}
	"review-tests": {
		prompt: "Review tests"
	}
}

settings: {
	default_agent: "echo"
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, tmpDir)

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	r := newTestResolver(cfg)
	_, err = r.resolve("review", singleCategoryScope("tasks", "task", false))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("resolve(review) error = %v, want ambiguity (matches review-code/docs/tests)", err)
	}
	for _, want := range []string{"review-code", "review-docs", "review-tests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error should list %q, got: %v", want, err)
		}
	}
}

// TestTaskResolution_ExactMatchWins asserts the inverted rule (the project-03
// deferral): an exact whole-name task that is also a substring of a longer task
// resolves and runs directly with no menu, on a non-TTY stdin. Both the
// review/start-review and start/start-review collisions are covered.
func TestTaskResolution_ExactMatchWins(t *testing.T) {
	cases := []struct {
		name  string
		exact string
		other string
	}{
		{"review", "review", "start/review"},
		{"start", "start", "start/review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Setenv("HOME", tmpDir)
			t.Setenv("XDG_CONFIG_HOME", tmpDir)

			configDir := filepath.Join(tmpDir, ".start")
			if err := os.MkdirAll(configDir, 0755); err != nil {
				t.Fatalf("creating config dir: %v", err)
			}

			config := fmt.Sprintf(`
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} test"
	}
}

tasks: {
	%q: {
		prompt: "Exact task"
	}
	%q: {
		prompt: "Longer task"
	}
}

settings: {
	default_agent: "echo"
}
`, tc.exact, tc.other)
			configFile := filepath.Join(configDir, "settings.cue")
			if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			chdir(t, tmpDir)

			// --dry-run captures the resolved task without reaching execution.
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			flags := &Flags{DryRun: true, Quiet: true}
			err := executeTask(stdout, stderr, strings.NewReader(""), flags, tc.exact, "")
			if err != nil {
				t.Fatalf("exact match must resolve directly, got: %v", err)
			}
			out := stdout.String()
			if !strings.Contains(out, "Task: "+tc.exact+"\n") {
				t.Errorf("expected resolved task %q in dry-run output, got:\n%s", tc.exact, out)
			}
			if strings.Contains(out, tc.other) {
				t.Errorf("exact match must not resolve the longer task %q, got:\n%s", tc.other, out)
			}
		})
	}
}

func TestTaskResolution_NoTasksDefined(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate from global config
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	config := `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} test"
	}
}

settings: {
	default_agent: "echo"
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, tmpDir)

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	r := newTestResolver(cfg)
	_, err = r.resolve("anything", singleCategoryScope("tasks", "task", false))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("resolve(anything) with no tasks = %v, want not-found", err)
	}
}

func TestExecuteStart_FilePathRole(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	roleContent := "You are a file-based role for testing."
	roleFile := filepath.Join(tmpDir, "test-role.md")
	if err := os.WriteFile(roleFile, []byte(roleContent), 0644); err != nil {
		t.Fatalf("writing role file: %v", err)
	}

	flags := &Flags{
		DryRun: true,
		Role:   "./test-role.md",
	}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		IncludeDefaults: true,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "./test-role.md") {
		t.Errorf("Expected file path in role output, got:\n%s", output)
	}

	if !strings.Contains(output, "file-based role") {
		t.Errorf("Expected role content in output, got:\n%s", output)
	}
}

func TestExecuteStart_FilePathContext(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	ctxContent := "File-based context content for testing."
	ctxFile := filepath.Join(tmpDir, "test-context.md")
	if err := os.WriteFile(ctxFile, []byte(ctxContent), 0644); err != nil {
		t.Fatalf("writing context file: %v", err)
	}

	flags := &Flags{
		DryRun:  true,
		Context: []string{"./test-context.md"},
	}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		Tags:            flags.Context,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "./test-context.md") {
		t.Errorf("Expected file path in context output, got:\n%s", output)
	}
}

func TestExecuteStart_MixedContextOrder(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "first.md"), []byte("First file context"), 0644); err != nil {
		t.Fatalf("writing first.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "last.md"), []byte("Last file context"), 0644); err != nil {
		t.Fatalf("writing last.md: %v", err)
	}

	// Mixed order: file, config tag (default), file
	flags := &Flags{
		DryRun:  true,
		Context: []string{"./first.md", "default", "./last.md"},
	}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		Tags:            flags.Context,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}

	output := stdout.String()

	// Order must be preserved: first.md before project (default) before last.md.
	firstIdx := strings.Index(output, "./first.md")
	projectIdx := strings.Index(output, "project")
	lastIdx := strings.Index(output, "./last.md")

	if firstIdx == -1 {
		t.Error("Expected ./first.md in output")
	}
	if projectIdx == -1 {
		t.Error("Expected project (default context) in output")
	}
	if lastIdx == -1 {
		t.Error("Expected ./last.md in output")
	}

	if firstIdx != -1 && projectIdx != -1 && lastIdx != -1 {
		if firstIdx >= projectIdx || projectIdx >= lastIdx {
			t.Errorf("Context order not preserved: first.md(%d) < project(%d) < last.md(%d)",
				firstIdx, projectIdx, lastIdx)
		}
	}
}

func TestExecuteTask_FilePathTask(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	taskContent := "File-based task prompt for testing."
	taskFile := filepath.Join(tmpDir, "test-task.md")
	if err := os.WriteFile(taskFile, []byte(taskContent), 0644); err != nil {
		t.Fatalf("writing task file: %v", err)
	}

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "./test-task.md", "")
	if err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "./test-task.md") {
		t.Errorf("Expected file path in task output, got:\n%s", output)
	}

	if !strings.Contains(output, "File-based task prompt") {
		t.Errorf("Expected task content in output, got:\n%s", output)
	}
}

func TestExecuteTask_FilePathWithInstructions(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	taskContent := "Review this code.\nInstructions: {{.instructions}}"
	taskFile := filepath.Join(tmpDir, "review-task.md")
	if err := os.WriteFile(taskFile, []byte(taskContent), 0644); err != nil {
		t.Fatalf("writing task file: %v", err)
	}

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "./review-task.md", "focus on security")
	if err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "focus on security") {
		t.Errorf("Expected instructions to be substituted, got:\n%s", output)
	}

	if strings.Contains(output, "{{.instructions}}") {
		t.Errorf("Template placeholder was not substituted, got:\n%s", output)
	}
}

func TestExecuteTask_FilePathMissing(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{DryRun: true}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := executeTask(stdout, stderr, strings.NewReader(""), flags, "./nonexistent.md", "")

	if err == nil {
		t.Error("Expected error for missing file")
		return
	}

	if !strings.Contains(err.Error(), "./nonexistent.md") {
		t.Errorf("Error should contain file path: %v", err)
	}
}

func TestTaskCommand_Metadata(t *testing.T) {
	cmd := NewRootCmd()

	taskCmd, _, err := cmd.Find([]string{"task"})
	if err != nil {
		t.Fatalf("task command not found: %v", err)
	}

	if taskCmd.Use != "task [name] [instructions ...]" {
		t.Errorf("Use = %q, want %q", taskCmd.Use, "task [name] [instructions ...]")
	}

	if taskCmd.Short == "" {
		t.Error("Short description should not be empty")
	}

	if !strings.Contains(taskCmd.Long, "{{.instructions}}") {
		t.Error("Long description should mention the {{.instructions}} placeholder")
	}
}

// runTaskDryRun runs `start task ...` with --dry-run through the root command and
// returns combined stdout. ./ paths resolve against the caller's chdir'd tmpDir.
func runTaskDryRun(t *testing.T, in string, args ...string) string {
	t.Helper()
	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(in))
	cmd.SetArgs(append([]string{"task"}, append(args, "--dry-run")...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task command error: %v", err)
	}
	return stdout.String()
}

// TestRunTask_MultipleInstructionSegments verifies trailing positionals after
// the task name are resolved as instruction segments and seam-joined with one
// blank line, mixing file paths and literals in order.
func TestRunTask_MultipleInstructionSegments(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// First file ends in a newline, second does not; the seam must still
	// collapse to exactly one blank line between segments.
	if err := os.WriteFile(filepath.Join(tmpDir, "intro.md"), []byte("checklist intro\n"), 0600); err != nil {
		t.Fatalf("writing intro.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "body.md"), []byte("checklist body"), 0600); err != nil {
		t.Fatalf("writing body.md: %v", err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "two files and a literal",
			args: []string{"test-task", "./intro.md", "./body.md", "follow it exactly"},
			want: "checklist intro\n\nchecklist body\n\nfollow it exactly",
		},
		{
			name: "literal before file",
			args: []string{"test-task", "lead in", "./body.md"},
			want: "lead in\n\nchecklist body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := runTaskDryRun(t, "", tt.args...)
			// test-task's body is "Instructions: {{.instructions}}"; assert the
			// composed segments substituted into the prompt body itself, not
			// merely the informational "Instructions:" summary section.
			body := dryRunPromptBody(t, output)
			want := "Instructions: " + tt.want
			if !strings.Contains(body, want) {
				t.Errorf("prompt body should contain substituted instructions %q, got:\n%s", want, body)
			}
		})
	}
}

// TestRunTask_FilePathTaskWithInstructionSegments verifies a file-path task name
// is read as the body while trailing positionals compose the instructions.
func TestRunTask_FilePathTaskWithInstructionSegments(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	taskBody := "Review this code.\nInstructions: {{.instructions}}"
	if err := os.WriteFile(filepath.Join(tmpDir, "review-task.md"), []byte(taskBody), 0600); err != nil {
		t.Fatalf("writing review-task.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "extra.md"), []byte("extra detail"), 0600); err != nil {
		t.Fatalf("writing extra.md: %v", err)
	}

	output := runTaskDryRun(t, "", "./review-task.md", "be thorough", "./extra.md")

	if !strings.Contains(output, "be thorough\n\nextra detail") {
		t.Errorf("output should contain composed instructions, got:\n%s", output)
	}
	if strings.Contains(output, "{{.instructions}}") {
		t.Errorf("task placeholder should be substituted, got:\n%s", output)
	}
}

// TestRunTask_AppendsInstructionsWhenNoPlaceholder verifies the end-to-end
// append path: a task body with no {{.instructions}} placeholder gets the
// composed instructions appended after the body with one blank line. Previously
// such instructions were silently dropped. Asserted on the exact prompt body so
// the seam spacing is pinned, not just substring presence.
func TestRunTask_AppendsInstructionsWhenNoPlaceholder(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "plain-task.md"), []byte("Plain task body with no placeholder."), 0600); err != nil {
		t.Fatalf("writing plain-task.md: %v", err)
	}

	output := runTaskDryRun(t, "", "./plain-task.md", "do the thing", "and the other thing")

	body := dryRunPromptBody(t, output)
	const want = "Plain task body with no placeholder.\n\ndo the thing\n\nand the other thing"
	if !strings.Contains(body, want) {
		t.Errorf("prompt body should append instructions after a placeholder-less task body, want substring %q, got:\n%s", want, body)
	}
}

// TestRunTask_UnreadableInstructionsFile verifies an unreadable instruction
// segment aborts with the reading-instructions-file error and launches nothing.
func TestRunTask_UnreadableInstructionsFile(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"task", "test-task", "ok text", "./nonexistent-instr.md", "--dry-run"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unreadable instructions file")
	}
	if !strings.Contains(err.Error(), `reading instructions "./nonexistent-instr.md"`) {
		t.Errorf("error = %q, want it to name the unreadable file", err.Error())
	}
	if strings.Contains(stdout.String(), "Dry Run") {
		t.Errorf("nothing should launch on a read failure, got:\n%s", stdout.String())
	}
}

// TestRunTask_StdinPrecedence pins that piped stdin supplies instructions only
// when the name is the sole positional; any instruction positional overrides it.
func TestRunTask_StdinPrecedence(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	t.Run("piped stdin used when only the name is given", func(t *testing.T) {
		output := runTaskDryRun(t, "instructions from stdin\n", "test-task")
		if !strings.Contains(output, "instructions from stdin") {
			t.Errorf("expected piped stdin as instructions, got:\n%s", output)
		}
	})

	t.Run("instruction positional overrides piped stdin", func(t *testing.T) {
		output := runTaskDryRun(t, "STDIN_SHOULD_NOT_APPEAR", "test-task", "arg instructions win")
		if !strings.Contains(output, "arg instructions win") {
			t.Errorf("expected positional instructions, got:\n%s", output)
		}
		if strings.Contains(output, "STDIN_SHOULD_NOT_APPEAR") {
			t.Errorf("piped stdin should be ignored when an instruction positional is given, got:\n%s", output)
		}
	})
}

func TestExecuteStart_FilePathContextMissing(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	flags := &Flags{
		DryRun:  true,
		Context: []string{"./missing-context.md"},
	}

	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		Tags:            flags.Context,
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	// Missing context files should not cause fatal error; show ○ status instead.
	err := executeStart(stdout, stderr, strings.NewReader(""), flags, selection, "")
	if err != nil {
		t.Fatalf("executeStart() should not fail for missing context file: %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "./missing-context.md") {
		t.Errorf("Expected missing file path in output, got:\n%s", output)
	}
}

func TestGetConfiguredAgents(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		agents: {
			claude: {
				bin: "claude"
				command: "{{.bin}}"
				description: "Anthropic Claude"
			}
			copilot: {
				bin: "gh"
				command: "{{.bin}} copilot"
				description: "GitHub Copilot"
			}
			aider: {
				bin: "aider"
				command: "{{.bin}}"
			}
		}
	}`)

	choices, err := getConfiguredAgents(cfg.Value)
	if err != nil {
		t.Fatalf("getConfiguredAgents() unexpected error: %v", err)
	}
	if len(choices) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(choices))
	}
	if choices[0].Name != "claude" {
		t.Errorf("expected first agent 'claude', got %q", choices[0].Name)
	}
	if choices[0].Description != "Anthropic Claude" {
		t.Errorf("expected description 'Anthropic Claude', got %q", choices[0].Description)
	}
	if choices[1].Name != "copilot" {
		t.Errorf("expected second agent 'copilot', got %q", choices[1].Name)
	}
	if choices[2].Name != "aider" {
		t.Errorf("expected third agent 'aider', got %q", choices[2].Name)
	}
	if choices[2].Description != "" {
		t.Errorf("expected empty description for aider, got %q", choices[2].Description)
	}
}

func TestGetConfiguredAgents_Empty(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		roles: {
			assistant: { prompt: "hello" }
		}
	}`)

	choices, err := getConfiguredAgents(cfg.Value)
	if err != nil {
		t.Fatalf("getConfiguredAgents() unexpected error: %v", err)
	}
	if len(choices) != 0 {
		t.Errorf("expected 0 agents, got %d", len(choices))
	}
}

func TestPromptAgentSelection_ByNumber(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("2\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	selected, err := promptAgentSelection(&buf, reader, choices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "copilot" {
		t.Errorf("expected 'copilot', got %q", selected)
	}
}

func TestPromptAgentSelection_ByExactName(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("copilot\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	selected, err := promptAgentSelection(&buf, reader, choices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "copilot" {
		t.Errorf("expected 'copilot', got %q", selected)
	}
}

func TestPromptAgentSelection_ByExactNameCaseInsensitive(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("CLAUDE\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	selected, err := promptAgentSelection(&buf, reader, choices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "claude" {
		t.Errorf("expected 'claude', got %q", selected)
	}
}

func TestPromptAgentSelection_BySubstring(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("cop\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	selected, err := promptAgentSelection(&buf, reader, choices)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "copilot" {
		t.Errorf("expected 'copilot', got %q", selected)
	}
}

func TestPromptAgentSelection_InvalidNumber(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("5\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	_, err := promptAgentSelection(&buf, reader, choices)
	if err == nil {
		t.Fatal("expected error for out-of-range number")
	}
	if !strings.Contains(err.Error(), "invalid selection") {
		t.Errorf("expected 'invalid selection' in error, got: %v", err)
	}
}

func TestPromptAgentSelection_AmbiguousSubstring(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("c\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	_, err := promptAgentSelection(&buf, reader, choices)
	if err == nil {
		t.Fatal("expected error for ambiguous substring")
	}
	if !strings.Contains(err.Error(), "invalid selection") {
		t.Errorf("expected 'invalid selection' in error, got: %v", err)
	}
}

func TestPromptAgentSelection_EmptyInput(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("\n"))

	choices := []agentChoice{
		{Name: "claude", Description: "Anthropic Claude"},
		{Name: "copilot", Description: "GitHub Copilot"},
	}

	_, err := promptAgentSelection(&buf, reader, choices)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "no selection provided") {
		t.Errorf("expected 'no selection provided' in error, got: %v", err)
	}
}

func TestPromptSetDefault_Yes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("y\n"))

	result := promptSetDefault(&buf, reader, "claude")
	if !result {
		t.Error("expected true for 'y' input")
	}
}

func TestPromptSetDefault_No(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("n\n"))

	result := promptSetDefault(&buf, reader, "claude")
	if result {
		t.Error("expected false for 'n' input")
	}
}

func TestBuildExecutionEnv_SingleAgent_AutoSelect(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		agents: {
			echo: {
				bin: "echo"
				command: "{{.bin}} hello"
			}
		}
	}`)

	flags := &Flags{}
	var buf bytes.Buffer
	r := strings.NewReader("")

	env, err := buildExecutionEnv(cfg, t.TempDir(), "", flags, &buf, io.Discard, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Agent.Name != "echo" {
		t.Errorf("expected agent 'echo', got %q", env.Agent.Name)
	}
}

func TestBuildExecutionEnv_DefaultAgentSet(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		settings: {
			default_agent: "copilot"
		}
		agents: {
			claude: {
				bin: "claude"
				command: "{{.bin}}"
			}
			copilot: {
				bin: "gh"
				command: "{{.bin}} copilot"
			}
		}
	}`)

	flags := &Flags{}
	var buf bytes.Buffer
	r := strings.NewReader("")

	env, err := buildExecutionEnv(cfg, t.TempDir(), "", flags, &buf, io.Discard, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Agent.Name != "copilot" {
		t.Errorf("expected agent 'copilot', got %q", env.Agent.Name)
	}
}

func TestBuildExecutionEnv_MultipleAgents_NonTTY(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		agents: {
			claude: {
				bin: "claude"
				command: "{{.bin}}"
			}
			copilot: {
				bin: "gh"
				command: "{{.bin}} copilot"
			}
		}
	}`)

	flags := &Flags{}
	var buf bytes.Buffer
	r := strings.NewReader("") // non-TTY: falls back to first agent

	env, err := buildExecutionEnv(cfg, t.TempDir(), "", flags, &buf, io.Discard, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.Agent.Name != "claude" {
		t.Errorf("expected first agent 'claude', got %q", env.Agent.Name)
	}
	if !strings.Contains(buf.String(), "Using agent") {
		t.Errorf("expected non-TTY fallback message, got: %q", buf.String())
	}
}

// ensureIndex cache behaviour tests

func TestEnsureIndex_FreshCacheSkipsFetchMessage(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// Seed a fresh cache with a canonical version matching the default index module.
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cacheDir := filepath.Join(tmpDir, "start")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheContent := fmt.Sprintf(
		"index_updated: %q\nindex_version: %q\n",
		time.Now().Format(time.RFC3339),
		"github.com/p3bot/library/index@v1.0.0",
	)
	if err := os.WriteFile(filepath.Join(cacheDir, "cache.cue"), []byte(cacheContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	if strings.Contains(stderr.String(), "Fetching registry index") {
		t.Errorf("fresh cache should skip 'Fetching registry index...' message, got:\n%s", stderr.String())
	}
}

func TestEnsureIndex_StaleCacheShowsFetchMessage(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// Seed a stale cache (48 hours old).
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cacheDir := filepath.Join(tmpDir, "start")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	cacheContent := fmt.Sprintf(
		"index_updated: %q\nindex_version: %q\n",
		staleTime,
		"github.com/p3bot/library/index@v1.0.0",
	)
	if err := os.WriteFile(filepath.Join(cacheDir, "cache.cue"), []byte(cacheContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	if !strings.Contains(stderr.String(), "Fetching registry index") {
		t.Errorf("stale cache should show 'Fetching registry index...' message, got:\n%s", stderr.String())
	}
}

func TestEnsureIndex_MissingCacheShowsFetchMessage(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// No cache file exists — XDG_CACHE_HOME points to empty temp dir.
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	if !strings.Contains(stderr.String(), "Fetching registry index") {
		t.Errorf("missing cache should show 'Fetching registry index...' message, got:\n%s", stderr.String())
	}
}

func TestEnsureIndex_QuietSuppressesFetchMessage(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// No cache — but Quiet flag suppresses the message.
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{Quiet: true}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	if strings.Contains(stderr.String(), "Fetching registry index") {
		t.Errorf("Quiet mode should suppress 'Fetching registry index...' message, got:\n%s", stderr.String())
	}
}

func TestEnsureIndex_MismatchedModuleShowsFetchMessage(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// Seed a fresh cache but with a different module path than the default.
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cacheDir := filepath.Join(tmpDir, "start")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheContent := fmt.Sprintf(
		"index_updated: %q\nindex_version: %q\n",
		time.Now().Format(time.RFC3339),
		"github.com/other/module/index@v0.1.0",
	)
	if err := os.WriteFile(filepath.Join(cacheDir, "cache.cue"), []byte(cacheContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	if !strings.Contains(stderr.String(), "Fetching registry index") {
		t.Errorf("mismatched module cache should show 'Fetching registry index...' message, got:\n%s", stderr.String())
	}
}

func TestEnsureIndex_FreshCacheNotRewritten(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	// Seed a fresh cache with a known timestamp.
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cacheDir := filepath.Join(tmpDir, "start")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedTime := time.Now().Add(-2 * time.Hour)
	cacheContent := fmt.Sprintf(
		"index_updated: %q\nindex_version: %q\n",
		seedTime.Format(time.RFC3339),
		"github.com/p3bot/library/index@v1.0.0",
	)
	cachePath := filepath.Join(cacheDir, "cache.cue")
	if err := os.WriteFile(cachePath, []byte(cacheContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadMergedConfigFromDir("")
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	stderr := new(bytes.Buffer)
	flags := &Flags{}
	r := newResolver(cfg, flags, io.Discard, stderr, strings.NewReader(""))
	r.indexSrc = newProductionIndexSource(flags, stderr) // drive the real mechanism, overriding the offline default

	_, _, _ = r.ensureIndex()

	// Read the cache file back — the timestamp should not have been updated.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cache file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, seedTime.Format(time.RFC3339)) {
		t.Errorf("fresh cache was rewritten (timestamp changed):\n%s", content)
	}
}

// TestRunStart_PipedStdin verifies that piped stdin is consumed as the prompt
// text and that only required contexts are included (matching `start prompt`).
func TestRunStart_PipedStdin(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("hi\n"))
	cmd.SetArgs([]string{"--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "hi") {
		t.Errorf("piped prompt text not found in output:\n%s", output)
	}

	// Required context "env" should be loaded (✓).
	envLoaded := false
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "env") && strings.Contains(line, "✓") {
			envLoaded = true
			break
		}
	}
	if !envLoaded {
		t.Errorf("required context 'env' should be loaded, got:\n%s", output)
	}

	// Default context "project" should be shown as skipped (○), matching
	// `start prompt` behaviour.
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "project") {
			if !strings.Contains(line, "○") {
				t.Errorf("default context 'project' should be skipped, got line: %s", line)
			}
			if strings.Contains(line, "✓") {
				t.Errorf("default context 'project' should not be loaded, got line: %s", line)
			}
		}
	}
}

// TestRunStart_PipedStdinHonoursFlags verifies that persistent flags
// (--context, --role none) are still honoured when stdin is piped.
func TestRunStart_PipedStdinHonoursFlags(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("review this"))
	cmd.SetArgs([]string{"--dry-run", "--role", "none", "--context", "project"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "review this") {
		t.Errorf("piped text missing from output:\n%s", output)
	}

	// --role none should suppress role content.
	if strings.Contains(output, "You are a helpful assistant") {
		t.Errorf("--role none should suppress role content, got:\n%s", output)
	}

	// --context project should load the default context even though piped
	// stdin uses IncludeDefaults: false.
	projectLoaded := false
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "project") && strings.Contains(line, "✓") {
			projectLoaded = true
			break
		}
	}
	if !projectLoaded {
		t.Errorf("--context project should load the context, got:\n%s", output)
	}
}

// TestRunStart_EmptyPipedStdin verifies that empty piped stdin falls back to
// the normal start flow (defaults included).
func TestRunStart_EmptyPipedStdin(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("   \n"))
	cmd.SetArgs([]string{"--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	// Default context "project" should be loaded in the normal flow.
	projectLoaded := false
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "project") && strings.Contains(line, "✓") {
			projectLoaded = true
			break
		}
	}
	if !projectLoaded {
		t.Errorf("empty piped stdin should fall back to normal start with defaults, got:\n%s", output)
	}
}

// TestRunTask_PipedStdinBecomesInstructions verifies that piped stdin is used
// as the task's {{.instructions}} when no second positional arg is given.
func TestRunTask_PipedStdinBecomesInstructions(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("piped-task-instructions"))
	cmd.SetArgs([]string{"task", "test-task", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	// The test-task template renders "Instructions: {{.instructions}}".
	// Piped content should land there.
	if !strings.Contains(output, "piped-task-instructions") {
		t.Errorf("piped stdin should appear as task instructions, got:\n%s", output)
	}
}

// TestRunTask_ArgWinsOverPipedStdin verifies that a positional instructions
// arg short-circuits piped stdin.
func TestRunTask_ArgWinsOverPipedStdin(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("PIPED_SHOULD_NOT_APPEAR"))
	cmd.SetArgs([]string{"task", "test-task", "arg-instructions-win", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "arg-instructions-win") {
		t.Errorf("positional instructions arg should appear in output, got:\n%s", output)
	}
	if strings.Contains(output, "PIPED_SHOULD_NOT_APPEAR") {
		t.Errorf("piped stdin should be ignored when an instructions arg is given, got:\n%s", output)
	}
}

// TestRunTask_PipedStdinHonoursFlags verifies that persistent flags
// (--role none, --context) are still honoured when stdin is piped to
// `start task`. Parallel to TestRunStart_PipedStdinHonoursFlags.
func TestRunTask_PipedStdinHonoursFlags(t *testing.T) {
	tmpDir := setupStartTestConfig(t)
	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader("piped instructions"))
	cmd.SetArgs([]string{"task", "test-task", "--dry-run", "--role", "none", "--context", "project"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "piped instructions") {
		t.Errorf("piped instructions missing from output:\n%s", output)
	}

	// --role none should suppress role content (task has role: "assistant" configured).
	if strings.Contains(output, "You are a helpful assistant") {
		t.Errorf("--role none should suppress role content, got:\n%s", output)
	}

	// --context project should load the default context even though tasks
	// use IncludeDefaults: false by default.
	projectLoaded := false
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "project") && strings.Contains(line, "✓") {
			projectLoaded = true
			break
		}
	}
	if !projectLoaded {
		t.Errorf("--context project should load the context, got:\n%s", output)
	}
}
