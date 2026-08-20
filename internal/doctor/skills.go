package doctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/skills"
)

// SkillDestScan is the uninstall-candidate dest set for each scope.
// Scanned is true when dests were looked up (including a failed lookup).
// A zero value is "not looked up": dest health is skipped and missing-dest
// Fixes are not emitted. Err is set when the agent catalog could not be
// consulted; dest slices are then ignored and dest health is skipped.
type SkillDestScan struct {
	Global  []skills.Dest
	Local   []skills.Dest
	Err     error
	Scanned bool
}

// CheckSkills reconciles each skills inventory against uninstall-candidate
// dests and validates SKILL.md frontmatter on every present dest.
func CheckSkills(paths config.Paths, scan SkillDestScan) SectionResult {
	section := SectionResult{Name: "Skills"}

	global, globalErr := loadSkillInventory(paths.GlobalExists, paths.Global)
	local, localErr := loadSkillInventory(paths.LocalExists, paths.Local)
	if globalErr != nil {
		section.Results = append(section.Results, CheckResult{
			Status:  StatusFail,
			Label:   "Global inventory",
			Message: globalErr.Error(),
			Fix:     "Fix CUE syntax in skills.cue",
		})
	}
	if localErr != nil {
		section.Results = append(section.Results, CheckResult{
			Status:  StatusFail,
			Label:   "Local inventory",
			Message: localErr.Error(),
			Fix:     "Fix CUE syntax in skills.cue",
		})
	}

	total := len(global) + len(local)
	if total == 0 && globalErr == nil && localErr == nil {
		section.Results = append(section.Results, CheckResult{
			Status: StatusInfo,
			Label:  "None installed",
		})
		return section
	}

	if total > 0 && (!scan.Scanned || scan.Err != nil) {
		if scan.Err != nil {
			section.Results = append(section.Results, CheckResult{
				Status:  StatusWarn,
				Label:   "Dest health",
				Message: "skipped (agent catalog unavailable)",
			})
		}
		appendSkillIdentity(&section, "global", global)
		appendSkillIdentity(&section, "local", local)
		section.Summary = fmt.Sprintf("%d installed", total)
		return section
	}

	checkSkillScope(&section, "global", global, scan.Global, false)
	checkSkillScope(&section, "local", local, scan.Local, true)

	if total > 0 {
		section.Summary = fmt.Sprintf("%d installed", total)
	}
	return section
}

func loadSkillInventory(exists bool, dir string) (map[string]skills.Entry, error) {
	if !exists {
		return map[string]skills.Entry{}, nil
	}
	return skills.Load(dir)
}

func appendSkillIdentity(section *SectionResult, scope string, entries map[string]skills.Entry) {
	for _, key := range sortedSkillKeys(entries) {
		e := entries[key]
		msg := scope
		if e.Version != "" {
			msg = e.Version + " (" + scope + ")"
		}
		section.Results = append(section.Results, CheckResult{
			Status:  StatusInfo,
			Label:   key,
			Message: msg,
			Indent:  1,
		})
	}
}

func checkSkillScope(section *SectionResult, scope string, entries map[string]skills.Entry, roots []skills.Dest, local bool) {
	for _, key := range sortedSkillKeys(entries) {
		present, err := skills.PresentDests(roots, key)
		if err != nil {
			section.Results = append(section.Results, CheckResult{
				Status:  StatusFail,
				Label:   key,
				Message: err.Error(),
			})
			continue
		}
		if len(present) == 0 {
			section.Results = append(section.Results, CheckResult{
				Status:  StatusNotFound,
				Label:   key,
				Message: "missing SKILL.md (" + scope + ")",
				Fix:     skills.InstallCommand(key, local),
			})
			continue
		}
		for _, dest := range present {
			skillFile := skills.SkillFile(dest)
			issues := skills.ValidateFrontmatter(skillFile)
			label := shortenPath(skillFile)
			if len(issues) == 0 {
				section.Results = append(section.Results, CheckResult{
					Status:  StatusPass,
					Label:   label,
					Message: "valid frontmatter",
				})
				continue
			}
			section.Results = append(section.Results, CheckResult{
				Status:  StatusFail,
				Label:   label,
				Message: strings.Join(issues, "; "),
			})
		}
	}
}

func sortedSkillKeys(entries map[string]skills.Entry) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
