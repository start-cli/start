package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestListCategoryValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "invalid category",
			args:    []string{"list", "invalid"},
			wantErr: `unknown category "invalid"`,
		},
		{
			name:    "valid category agents - no error from validation",
			args:    []string{"list", "agents"},
			wantErr: "", // fails later on config, not on category validation
		},
		{
			name:    "valid category plural",
			args:    []string{"list", "tasks"},
			wantErr: "",
		},
		{
			name:    "valid category singular",
			args:    []string{"list", "task"},
			wantErr: "",
		},
		{
			name:    "valid category skills",
			args:    []string{"list", "skills"},
			wantErr: "",
		},
		{
			name:    "valid category skill singular",
			args:    []string{"list", "skill"},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tt.args)
			err := cmd.Execute()

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
			} else {
				// Only category validation is under test; downstream failures
				// (e.g. missing config) are acceptable.
				if err != nil && strings.Contains(err.Error(), "unknown category") {
					t.Errorf("valid category should not fail validation, got: %v", err)
				}
			}
		})
	}
}

func TestPrintInstalledModulesJSON(t *testing.T) {
	t.Parallel()
	installed := []InstalledModule{
		{
			Category:     "agents",
			Name:         "ai/claude",
			Description:  "Claude by Anthropic",
			Tags:         []string{"anthropic", "ai"},
			Models:       []string{"claude-sonnet-4-20250514"},
			InstalledVer: "v0.2.0",
			Scope:        "global",
			Origin:       "github.com/test/agents/ai/claude@v0.2.0",
			ConfigFile:   "/home/user/.start/agents.cue",
		},
		{
			Category:     "roles",
			Name:         "golang/assistant",
			Description:  "Go programming expert",
			Tags:         []string{"golang"},
			InstalledVer: "v0.1.0",
			Scope:        "local",
			Origin:       "github.com/test/roles/golang/assistant@v0.1.0",
			ConfigFile:   "/project/.start/roles.cue",
		},
	}

	data, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	output := string(data)

	if !strings.Contains(output, `"category": "agents"`) {
		t.Errorf("output missing category field, got: %s", output)
	}
	if !strings.Contains(output, `"name": "ai/claude"`) {
		t.Errorf("output missing name field, got: %s", output)
	}
	if !strings.Contains(output, `"version": "v0.2.0"`) {
		t.Errorf("output missing version field, got: %s", output)
	}
	if !strings.Contains(output, `"scope": "global"`) {
		t.Errorf("output missing scope field, got: %s", output)
	}
	if !strings.Contains(output, `"description": "Claude by Anthropic"`) {
		t.Errorf("output missing description field, got: %s", output)
	}
	if !strings.Contains(output, `"anthropic"`) {
		t.Errorf("output missing tags, got: %s", output)
	}
	if !strings.Contains(output, `"claude-sonnet-4-20250514"`) {
		t.Errorf("output missing models, got: %s", output)
	}
	if strings.Contains(output, `"updateAvailable"`) {
		t.Errorf("omitempty should suppress false updateAvailable, got: %s", output)
	}
}

// TestInstalledModuleJSONOmitsEmptyOptionalFields verifies omitempty suppresses nil slices.
func TestInstalledModuleJSONOmitsEmptyOptionalFields(t *testing.T) {
	t.Parallel()
	module := InstalledModule{
		Category: "roles",
		Name:     "test",
		Scope:    "global",
		Origin:   "github.com/test/roles/test@v0.1.0",
	}

	data, err := json.MarshalIndent(module, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	output := string(data)

	if strings.Contains(output, `"tags"`) {
		t.Errorf("omitempty should suppress nil tags, got: %s", output)
	}
	if strings.Contains(output, `"models"`) {
		t.Errorf("omitempty should suppress nil models, got: %s", output)
	}
	if strings.Contains(output, `"description"`) {
		t.Errorf("omitempty should suppress empty description, got: %s", output)
	}
	if strings.Contains(output, `"status"`) {
		t.Errorf("omitempty should suppress empty status, got: %s", output)
	}
}

func TestInstalledModuleJSONStatusMissing(t *testing.T) {
	t.Parallel()
	module := InstalledModule{
		Category: "skills",
		Name:     "workflows/one-by-one",
		Scope:    "global",
		Origin:   "github.com/p3bot/library/skills/workflows/one-by-one@v1.0.0",
		Status:   skillStatusMissing,
	}
	data, err := json.Marshal(module)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status":"missing"`) {
		t.Errorf("missing status not emitted: %s", data)
	}
}
