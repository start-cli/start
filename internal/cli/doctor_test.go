package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/registry"
)

// cleanupCUECache makes CUE cache files writable before t.TempDir cleanup; CUE
// extracts modules read-only, which otherwise blocks removal.
func cleanupCUECache(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(filepath.Join(dir, ".cache"), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			return os.Chmod(path, 0755)
		})
	})
}

func TestDoctorCommand_Exists(t *testing.T) {
	cmd := NewRootCmd()

	doctorCmd, _, err := cmd.Find([]string{"doctor"})
	if err != nil {
		t.Fatalf("doctor command not found: %v", err)
	}

	if doctorCmd.Use != "doctor" {
		t.Errorf("Use = %q, want %q", doctorCmd.Use, "doctor")
	}

	if doctorCmd.Short == "" {
		t.Error("Short description should not be empty")
	}
}

// Tests below use os.Chdir, so they must not run in parallel.

func TestDoctorCommand_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"doctor"})

	_ = cmd.Execute()

	output := stdout.String()

	if !strings.Contains(output, "start") {
		t.Errorf("output should contain 'start', got: %s", output)
	}
}

func TestDoctorCommand_WithConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cleanupCUECache(t, tmpDir)

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
		default_model: "default"
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

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"doctor"})

	_ = cmd.Execute()

	output := stdout.String()

	expectedSections := []string{
		"Version",
		"Configuration",
		"Settings",
		"Agents",
	}

	for _, section := range expectedSections {
		if !strings.Contains(output, section) {
			t.Errorf("output should contain section %q, got: %s", section, output)
		}
	}
}

func TestDoctorCommand_Verbose(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"doctor", "--verbose"})

	_ = cmd.Execute()

	if stdout.Len() == 0 {
		t.Error("verbose mode should produce output")
	}
}

func TestPrepareDoctor(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	report, err := prepareDoctor(NewRootCmd(), registry.NewClient, nil)
	if err != nil {
		t.Fatalf("prepareDoctor() error = %v", err)
	}

	if len(report.Sections) == 0 {
		t.Error("report should have sections")
	}

	sectionNames := make(map[string]bool)
	for _, s := range report.Sections {
		sectionNames[s.Name] = true
	}

	expectedSections := []string{"Repository", "Version", "Configuration", "Settings", "Environment"}
	for _, name := range expectedSections {
		if !sectionNames[name] {
			t.Errorf("missing section %q", name)
		}
	}
}

func TestPrepareDoctor_WithValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cleanupCUECache(t, tmpDir)

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	config := `
agents: {
	testAgent: {
		bin: "echo"
		command: "{{.bin}} test"
	}
}

roles: {
	testRole: {
		prompt: "Test role"
	}
}

contexts: {
	testContext: {
		prompt: "Test context"
	}
}
`
	configFile := filepath.Join(configDir, "settings.cue")
	if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, tmpDir)

	report, err := prepareDoctor(NewRootCmd(), registry.NewClient, nil)
	if err != nil {
		t.Fatalf("prepareDoctor() error = %v", err)
	}

	sectionNames := make(map[string]bool)
	for _, s := range report.Sections {
		sectionNames[s.Name] = true
	}

	if !sectionNames["Schema Validation"] {
		t.Error("missing Schema Validation section")
	}
	if !sectionNames["Agents"] {
		t.Error("missing Agents section")
	}
	if !sectionNames["Roles"] {
		t.Error("missing Roles section")
	}
	if !sectionNames["Contexts"] {
		t.Error("missing Contexts section")
	}
}

func TestDoctorError(t *testing.T) {
	err := &doctorError{}

	if err.Error() != "issues found" {
		t.Errorf("Error() = %q, want %q", err.Error(), "issues found")
	}

	if errDoctorIssuesFound.Error() != "issues found" {
		t.Errorf("errDoctorIssuesFound.Error() = %q, want %q", errDoctorIssuesFound.Error(), "issues found")
	}
}

func TestDoctorCommand_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"doctor", "--json"})

	_ = cmd.Execute()

	output := stdout.String()
	if output == "" {
		t.Fatal("expected JSON output, got empty string")
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &report); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
	}

	sections, ok := report["sections"]
	if !ok {
		t.Fatal("JSON output missing 'sections' key")
	}

	sectionList, ok := sections.([]any)
	if !ok || len(sectionList) == 0 {
		t.Fatal("'sections' should be a non-empty array")
	}

	firstSection, ok := sectionList[0].(map[string]any)
	if !ok {
		t.Fatal("section should be an object")
	}
	if _, ok := firstSection["name"]; !ok {
		t.Error("section missing 'name' field")
	}
	if _, ok := firstSection["results"]; !ok {
		t.Error("section missing 'results' field")
	}

	results, _ := firstSection["results"].([]any)
	if len(results) > 0 {
		result, _ := results[0].(map[string]any)
		status, _ := result["status"].(string)
		if status == "" {
			t.Error("check result 'status' should be a non-empty string")
		}
	}
}

func TestDoctorCommand_JSONHasFlag(t *testing.T) {
	cmd := NewRootCmd()
	doctorCmd, _, _ := cmd.Find([]string{"doctor"})
	if doctorCmd == nil {
		t.Fatal("doctor command not found")
	}
	if f := doctorCmd.Flags().Lookup("json"); f == nil {
		t.Error("doctor command should have --json flag")
	}
}
