package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeSkillMD(t *testing.T, dir, name, body string) string {
	t.Helper()
	dest := filepath.Join(dir, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, skillFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateFrontmatterValid(t *testing.T) {
	t.Parallel()
	path := writeSkillMD(t, t.TempDir(), "one-by-one", "---\nname: one-by-one\ndescription: walk findings one at a time\n---\nBody.\n")
	if issues := ValidateFrontmatter(path); len(issues) != 0 {
		t.Fatalf("valid SKILL.md: %v", issues)
	}
}

func TestValidateFrontmatterIssues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		leaf    string
		body    string
		wantSub string
	}{
		{
			name:    "missing frontmatter",
			leaf:    "one-by-one",
			body:    "# just markdown\n",
			wantSub: "missing YAML frontmatter",
		},
		{
			name:    "missing name",
			leaf:    "one-by-one",
			body:    "---\ndescription: walk findings\n---\n",
			wantSub: "name is required",
		},
		{
			name:    "missing description",
			leaf:    "one-by-one",
			body:    "---\nname: one-by-one\n---\n",
			wantSub: "description is required",
		},
		{
			name:    "leading hyphen",
			leaf:    "-one",
			body:    "---\nname: -one\ndescription: walk\n---\n",
			wantSub: "hyphens",
		},
		{
			name:    "name not parent dir",
			leaf:    "one-by-one",
			body:    "---\nname: other-name\ndescription: walk\n---\n",
			wantSub: "parent directory",
		},
		{
			name:    "consecutive hyphens",
			leaf:    "one-by-one",
			body:    "---\nname: one--by-one\ndescription: walk\n---\n",
			wantSub: "hyphens",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSkillMD(t, t.TempDir(), tt.leaf, tt.body)
			issues := ValidateFrontmatter(path)
			if len(issues) == 0 {
				t.Fatal("expected issues")
			}
			found := false
			for _, issue := range issues {
				if strings.Contains(issue, tt.wantSub) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("issues = %v, want substring %q", issues, tt.wantSub)
			}
		})
	}
}

func TestValidateFrontmatterLengthLimits(t *testing.T) {
	t.Parallel()
	longName := strings.Repeat("a", maxSkillNameLen+1)
	path := writeSkillMD(t, t.TempDir(), longName, "---\nname: "+longName+"\ndescription: walk\n---\n")
	issues := ValidateFrontmatter(path)
	found := false
	for _, issue := range issues {
		if strings.Contains(issue, "name exceeds") {
			found = true
		}
	}
	if !found {
		t.Errorf("long name issues = %v", issues)
	}

	longDesc := strings.Repeat("x", maxSkillDescriptionLen+1)
	if utf8.RuneCountInString(longDesc) <= maxSkillDescriptionLen {
		t.Fatal("test setup")
	}
	path = writeSkillMD(t, t.TempDir(), "one-by-one", "---\nname: one-by-one\ndescription: "+longDesc+"\n---\n")
	issues = ValidateFrontmatter(path)
	found = false
	for _, issue := range issues {
		if strings.Contains(issue, "description exceeds") {
			found = true
		}
	}
	if !found {
		t.Errorf("long description issues = %v", issues)
	}
}
