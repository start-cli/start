package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	maxSkillNameLen        = 64
	maxSkillDescriptionLen = 1024
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ValidateFrontmatter checks SKILL.md against the Agent Skills rules: name
// charset and length, no leading/trailing/consecutive hyphens, name equal to
// the parent directory, and a present description within length. Returns nil
// when the file is valid; otherwise one diagnostic per broken rule.
func ValidateFrontmatter(skillFile string) []string {
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return []string{fmt.Sprintf("cannot read: %v", err)}
	}
	block, err := extractFrontmatter(string(data))
	if err != nil {
		return []string{err.Error()}
	}

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return []string{fmt.Sprintf("invalid YAML frontmatter: %v", err)}
	}

	var issues []string
	issues = append(issues, validateSkillName(fm.Name, filepath.Base(filepath.Dir(skillFile)))...)
	issues = append(issues, validateSkillDescription(fm.Description)...)
	return issues
}

func validateSkillName(name, parentDir string) []string {
	var issues []string
	if name == "" {
		issues = append(issues, "name is required")
		return issues
	}
	if utf8.RuneCountInString(name) > maxSkillNameLen {
		issues = append(issues, fmt.Sprintf("name exceeds %d characters", maxSkillNameLen))
	}
	if !skillNamePattern.MatchString(name) {
		issues = append(issues, "name must be lowercase a-z, 0-9, and hyphens, with no leading, trailing, or consecutive hyphens")
	}
	if name != parentDir {
		issues = append(issues, fmt.Sprintf("name %q must equal parent directory %q", name, parentDir))
	}
	return issues
}

func validateSkillDescription(description string) []string {
	if strings.TrimSpace(description) == "" {
		return []string{"description is required"}
	}
	if utf8.RuneCountInString(description) > maxSkillDescriptionLen {
		return []string{fmt.Sprintf("description exceeds %d characters", maxSkillDescriptionLen)}
	}
	return nil
}

func extractFrontmatter(content string) (string, error) {
	s := strings.TrimPrefix(content, "\uFEFF")
	s = strings.TrimLeft(s, "\r\n")
	if !strings.HasPrefix(s, "---") {
		return "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := s[3:]
	if rest == "" {
		return "", fmt.Errorf("missing YAML frontmatter closing delimiter")
	}
	if rest[0] != '\n' && !strings.HasPrefix(rest, "\r\n") {
		return "", fmt.Errorf("missing YAML frontmatter")
	}
	if rest[0] == '\n' {
		rest = rest[1:]
	} else {
		rest = rest[2:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		if strings.HasPrefix(rest, "---") {
			return "", nil
		}
		return "", fmt.Errorf("missing YAML frontmatter closing delimiter")
	}
	return rest[:idx], nil
}
