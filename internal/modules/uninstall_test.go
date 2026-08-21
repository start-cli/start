package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveModuleFromConfig_PreservesSiblingsAndComments(t *testing.T) {
	t.Parallel()
	configPath := filepath.Join(t.TempDir(), "agents.cue")
	content := `// start configuration
// Managed by 'start install'
agents: {
	claude: {
		origin: "github.com/x/claude@v0.1.0"
		bin:    "claude"
	}
	gpt: {
		bin: "gpt"
	}
}
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveModuleFromConfig(configPath, "agents", "gpt"); err != nil {
		t.Fatalf("RemoveModuleFromConfig() error: %v", err)
	}

	result, _ := os.ReadFile(configPath)
	s := string(result)
	if strings.Contains(s, "gpt") {
		t.Errorf("gpt should be removed:\n%s", s)
	}
	if !strings.Contains(s, "claude") {
		t.Errorf("claude should remain:\n%s", s)
	}
	if !strings.Contains(s, "Managed by 'start install'") {
		t.Errorf("comment header should be preserved:\n%s", s)
	}
}

func TestRemoveModuleFromConfig_DropsEmptiedCategory(t *testing.T) {
	t.Parallel()
	configPath := filepath.Join(t.TempDir(), "tasks.cue")
	content := `tasks: {
	review: {
		prompt: "Review."
	}
}
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveModuleFromConfig(configPath, "tasks", "review"); err != nil {
		t.Fatalf("RemoveModuleFromConfig() error: %v", err)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("emptied config file should be removed, stat: %v", err)
	}
}

func TestRemoveModuleFromConfig_DeletesHeaderOnlyFile(t *testing.T) {
	t.Parallel()
	configPath := filepath.Join(t.TempDir(), "skills.cue")
	content := `// start configuration
// Managed by 'start install'
skills: {
	"finding/one-by-one": {
		origin:  "github.com/p3bot/library/skills/finding/one-by-one@v1.2.0"
		version: "v1.2.0"
	}
}
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveModuleFromConfig(configPath, "skills", "finding/one-by-one"); err != nil {
		t.Fatalf("RemoveModuleFromConfig() error: %v", err)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		data, _ := os.ReadFile(configPath)
		t.Fatalf("header-only skills.cue should be removed, left %q", data)
	}
}

func TestRemoveModuleFromConfig_NotFound(t *testing.T) {
	t.Parallel()
	configPath := filepath.Join(t.TempDir(), "agents.cue")

	t.Run("missing file", func(t *testing.T) {
		if err := RemoveModuleFromConfig(configPath, "agents", "claude"); err == nil {
			t.Fatal("expected error for missing file")
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found', got: %v", err)
		}
	})

	content := "agents: {\n\tclaude: {bin: \"claude\"}\n}\n"
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("missing module", func(t *testing.T) {
		if err := RemoveModuleFromConfig(configPath, "agents", "gpt"); err == nil {
			t.Fatal("expected error for absent module")
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found', got: %v", err)
		}
	})

	t.Run("missing category", func(t *testing.T) {
		if err := RemoveModuleFromConfig(configPath, "roles", "claude"); err == nil {
			t.Fatal("expected error for absent category")
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found', got: %v", err)
		}
	})
}
