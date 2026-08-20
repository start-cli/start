package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/modules"
	"github.com/p3bot/start/internal/registry"
)

func TestPrintSearchSections(t *testing.T) {
	t.Parallel()

	results := []modules.SearchResult{
		{Category: "roles", Name: "golang", Entry: registry.IndexEntry{Description: "Go programming expert"}},
		{Category: "tasks", Name: "pre-commit-review", Entry: registry.IndexEntry{Description: "Review staged changes"}},
	}

	t.Run("single section output", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "local", Path: "./.start", Results: results},
		}
		printSearchSections(&buf, sections, false, nil)
		out := buf.String()

		if !strings.Contains(out, "local") {
			t.Error("missing section label")
		}
		if !strings.Contains(out, "./.start") {
			t.Error("missing section path")
		}
		if !strings.Contains(out, "roles:") {
			t.Error("missing roles category")
		}
		if !strings.Contains(out, "tasks:") {
			t.Error("missing tasks category")
		}
		if !strings.Contains(out, "golang") {
			t.Error("missing golang result")
		}
		if !strings.Contains(out, "pre-commit-review") {
			t.Error("missing pre-commit-review result")
		}
	})

	t.Run("multiple sections with blank line separator", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "local", Path: "./.start", Results: results},
			{Label: "registry", Results: results, ShowInstalled: true},
		}
		printSearchSections(&buf, sections, false, nil)
		out := buf.String()

		if !strings.Contains(out, "local") {
			t.Error("missing local section label")
		}
		if !strings.Contains(out, "registry") {
			t.Error("missing registry section label")
		}

		// Sections should be separated by a blank line
		localIdx := strings.Index(out, "local")
		registryIdx := strings.Index(out, "registry")
		between := out[localIdx:registryIdx]
		if !strings.Contains(between, "\n\n") {
			t.Error("sections should be separated by blank line")
		}
	})

	t.Run("empty sections omitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "local", Path: "./.start", Results: nil},
			{Label: "registry", Results: results},
		}
		printSearchSections(&buf, sections, false, nil)
		out := buf.String()

		if strings.Contains(out, "./.start") {
			t.Error("empty local section should be omitted")
		}
		if !strings.Contains(out, "registry") {
			t.Error("missing registry section")
		}
	})

	t.Run("installed markers only in registry section", func(t *testing.T) {
		t.Parallel()
		installed := map[string]bool{
			"roles/golang": true,
		}
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "local", Path: "./.start", Results: results},
			{Label: "registry", Results: results, ShowInstalled: true},
		}
		printSearchSections(&buf, sections, false, installed)
		out := buf.String()

		registryIdx := strings.Index(out, "registry")
		localPart := out[:registryIdx]
		registryPart := out[registryIdx:]

		if strings.Contains(localPart, "★") {
			t.Error("local section should not have installed markers")
		}
		if !strings.Contains(registryPart, "★") {
			t.Error("registry section should have installed marker for golang")
		}
	})

	t.Run("verbose shows tags and module paths", func(t *testing.T) {
		t.Parallel()
		verboseResults := []modules.SearchResult{
			{
				Category: "roles",
				Name:     "golang",
				Entry:    registry.IndexEntry{Description: "Go expert", Module: "github.com/test/roles/golang@v0", Tags: []string{"go", "programming"}},
			},
		}
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "registry", Results: verboseResults},
		}
		printSearchSections(&buf, sections, true, nil)
		out := buf.String()

		if !strings.Contains(out, "Module:") {
			t.Error("verbose mode should show module path")
		}
		if !strings.Contains(out, "github.com/test/roles/golang@v0") {
			t.Error("verbose mode should show actual module path")
		}
		if !strings.Contains(out, "Tags:") {
			t.Error("verbose mode should show tags")
		}
		if !strings.Contains(out, "go, programming") {
			t.Error("verbose mode should show actual tags")
		}
	})

	t.Run("non-verbose hides tags and module paths", func(t *testing.T) {
		t.Parallel()
		verboseResults := []modules.SearchResult{
			{
				Category: "roles",
				Name:     "golang",
				Entry:    registry.IndexEntry{Description: "Go expert", Module: "github.com/test/roles/golang@v0", Tags: []string{"go"}},
			},
		}
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "registry", Results: verboseResults},
		}
		printSearchSections(&buf, sections, false, nil)
		out := buf.String()

		if strings.Contains(out, "Module:") {
			t.Error("non-verbose should not show module path")
		}
		if strings.Contains(out, "Tags:") {
			t.Error("non-verbose should not show tags")
		}
	})

	t.Run("category order is agents roles contexts tasks skills", func(t *testing.T) {
		t.Parallel()
		allCatResults := []modules.SearchResult{
			{Category: "skills", Name: "one-by-one", Entry: registry.IndexEntry{Description: "Walk findings"}},
			{Category: "contexts", Name: "env", Entry: registry.IndexEntry{Description: "Environment"}},
			{Category: "agents", Name: "claude", Entry: registry.IndexEntry{Description: "Claude AI"}},
			{Category: "tasks", Name: "review", Entry: registry.IndexEntry{Description: "Code review"}},
			{Category: "roles", Name: "golang", Entry: registry.IndexEntry{Description: "Go expert"}},
		}
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "test", Results: allCatResults},
		}
		printSearchSections(&buf, sections, false, nil)
		out := buf.String()

		agentsIdx := strings.Index(out, "agents:")
		rolesIdx := strings.Index(out, "roles:")
		tasksIdx := strings.Index(out, "tasks:")
		contextsIdx := strings.Index(out, "contexts:")
		skillsIdx := strings.Index(out, "skills:")

		if agentsIdx < 0 || rolesIdx < 0 || contextsIdx < 0 || tasksIdx < 0 || skillsIdx < 0 {
			t.Fatalf("missing category header in %q", out)
		}
		if agentsIdx > rolesIdx || rolesIdx > contextsIdx || contextsIdx > tasksIdx || tasksIdx > skillsIdx {
			t.Errorf("categories in wrong order: agents=%d roles=%d contexts=%d tasks=%d skills=%d",
				agentsIdx, rolesIdx, contextsIdx, tasksIdx, skillsIdx)
		}
	})

	t.Run("items indented under category", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		sections := []searchSection{
			{Label: "test", Results: []modules.SearchResult{
				{Category: "roles", Name: "golang", Entry: registry.IndexEntry{Description: "Go expert"}},
			}},
		}
		printSearchSections(&buf, sections, false, nil)

		lines := strings.SplitSeq(buf.String(), "\n")
		for line := range lines {
			if strings.Contains(line, "golang") {
				if !strings.HasPrefix(line, "    ") {
					t.Errorf("item should be indented with 4 spaces, got: %q", line)
				}
			}
			if strings.Contains(line, "roles:") {
				if !strings.HasPrefix(line, "  ") {
					t.Errorf("category should be indented with 2 spaces, got: %q", line)
				}
			}
		}
	})
}

func TestSearchCommandJSON_ShortQueryError(t *testing.T) {
	// With --json, a short query returns error immediately instead of prompting
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"search", "go", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for short query with --json")
	}
	if !strings.Contains(err.Error(), "3 characters") {
		t.Errorf("error should mention 3 characters, got: %v", err)
	}
	// Should not have produced any JSON output
	if stdout.Len() > 0 {
		t.Errorf("expected no output on error, got: %s", stdout.String())
	}
}

func TestSearchCommandJSON_WithConfigResults(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	globalDir := filepath.Join(tmpDir, "start")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `roles: {
	"golang": {
		description: "Go programming expert"
		prompt: "You are a Go expert."
	}
}`
	if err := os.WriteFile(filepath.Join(globalDir, "config.cue"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"search", "golang", "--json"})

	// May fail due to registry unavailability but should still output JSON
	_ = cmd.Execute()

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		t.Fatal("expected JSON output even if registry unavailable")
	}

	var sections []map[string]any
	if err := json.Unmarshal([]byte(output), &sections); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
	}

	// Should find the global config result at minimum
	if len(sections) == 0 {
		t.Error("expected at least one section (global config) in JSON output")
	}

	for _, section := range sections {
		if _, ok := section["label"]; !ok {
			t.Error("section missing 'label' field")
		}
		if _, ok := section["results"]; !ok {
			t.Error("section missing 'results' field")
		}
	}
}

func TestSearchCommandJSON_NoResults(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	chdir(t, tmpDir)

	cmd := NewRootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"search", "nonexistentthing", "--json"})

	_ = cmd.Execute()

	output := strings.TrimSpace(stdout.String())
	if output != "[]" {
		t.Errorf("expected empty JSON array, got: %s", output)
	}
}

func TestSearchCommandValidation(t *testing.T) {
	t.Parallel()

	t.Run("query under 3 characters returns error", func(t *testing.T) {
		t.Parallel()
		cmd := NewRootCmd()
		cmd.SetArgs([]string{"search", "go"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for short query")
		}
		if !strings.Contains(err.Error(), "3 characters") {
			t.Errorf("error should mention 3 characters, got: %s", err.Error())
		}
	})

	// The 3-character floor is measured against the name, excluding the
	// "category:" prefix, so a short scoped name is rejected the same as a short
	// bare query — the prefix does not pad it over the threshold.
	t.Run("category prefix with short name returns error", func(t *testing.T) {
		t.Parallel()
		cmd := NewRootCmd()
		cmd.SetArgs([]string{"search", "roles:go"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for short scoped query")
		}
		if !strings.Contains(err.Error(), "3 characters") {
			t.Errorf("error should mention 3 characters, got: %s", err.Error())
		}
	})

	t.Run("unknown category prefix returns error", func(t *testing.T) {
		t.Parallel()
		cmd := NewRootCmd()
		cmd.SetArgs([]string{"search", "bogus:golang"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for unknown category prefix")
		}
		if !strings.Contains(err.Error(), "unknown category") {
			t.Errorf("error should mention unknown category, got: %s", err.Error())
		}
	})

	t.Run("find alias is registered", func(t *testing.T) {
		t.Parallel()
		cmd := NewRootCmd()
		for _, sub := range cmd.Commands() {
			if sub.Name() == "search" {
				found := false
				for _, alias := range sub.Aliases {
					if alias == "find" {
						found = true
					}
				}
				if !found {
					t.Error("search command should have 'find' alias")
				}
				return
			}
		}
		t.Error("search command not found")
	})
}

func TestShortenHome(t *testing.T) {
	t.Parallel()

	t.Run("shortens home directory", func(t *testing.T) {
		t.Parallel()
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot determine home directory")
		}
		result := shortenHome(home + "/.config/start")
		if result != "~/.config/start" {
			t.Errorf("shortenHome(%q) = %q, want %q", home+"/.config/start", result, "~/.config/start")
		}
	})

	t.Run("returns non-home path unchanged", func(t *testing.T) {
		t.Parallel()
		result := shortenHome("/tmp/some/path")
		if result != "/tmp/some/path" {
			t.Errorf("shortenHome(/tmp/some/path) = %q, want /tmp/some/path", result)
		}
	})
}
