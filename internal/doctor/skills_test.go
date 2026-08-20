package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/config"
	"github.com/p3bot/start/internal/skills"
)

func TestCheckSkillsNoneInstalled(t *testing.T) {
	t.Parallel()
	paths := config.Paths{Global: t.TempDir(), Local: t.TempDir()}
	section := CheckSkills(paths, SkillDestScan{})
	if section.Name != "Skills" {
		t.Errorf("Name = %q", section.Name)
	}
	if len(section.Results) != 1 || section.Results[0].Label != "None installed" {
		t.Errorf("results = %+v", section.Results)
	}
}

func TestCheckSkillsDriftFix(t *testing.T) {
	t.Parallel()
	global := t.TempDir()
	local := t.TempDir()
	if err := skills.Upsert(global, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := skills.Upsert(local, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Global: global, Local: local, GlobalExists: true, LocalExists: true}
	section := CheckSkills(paths, SkillDestScan{Scanned: true})

	var globalFix, localFix string
	for _, r := range section.Results {
		if r.Label != "workflows/one-by-one" {
			continue
		}
		if strings.Contains(r.Message, "global") {
			globalFix = r.Fix
		}
		if strings.Contains(r.Message, "local") {
			localFix = r.Fix
		}
		if r.Status != StatusNotFound {
			t.Errorf("drift status = %v, want notfound", r.Status)
		}
	}
	if globalFix != "start install skills:workflows/one-by-one" {
		t.Errorf("global Fix = %q", globalFix)
	}
	if localFix != "start install skills:workflows/one-by-one --local" {
		t.Errorf("local Fix = %q", localFix)
	}
}

func TestCheckSkillsCatalogDownSkipsDestHealth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := skills.Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	section := CheckSkills(config.Paths{Global: dir, GlobalExists: true}, SkillDestScan{Scanned: true, Err: errors.New("unavailable")})
	warned := false
	drift := false
	for _, r := range section.Results {
		if r.Status == StatusWarn && strings.Contains(r.Message, "skipped") {
			warned = true
		}
		if r.Fix != "" {
			drift = true
		}
	}
	if !warned {
		t.Errorf("want dest-health skip warning, got %+v", section.Results)
	}
	if drift {
		t.Errorf("catalog-down must not report drift: %+v", section.Results)
	}
}

func TestCheckSkillsUnscannedDoesNotReportDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := skills.Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	section := CheckSkills(config.Paths{Global: dir, GlobalExists: true}, SkillDestScan{})
	for _, r := range section.Results {
		if r.Fix != "" {
			t.Errorf("unscanned dests must not offer install Fix: %+v", r)
		}
		if r.Status == StatusNotFound {
			t.Errorf("unscanned dests must not report missing: %+v", r)
		}
	}
}

func TestCheckSkillsFrontmatterPerDest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := skills.Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	validRoot := filepath.Join(dir, "valid")
	invalidRoot := filepath.Join(dir, "invalid")
	foreignRoot := filepath.Join(dir, "foreign")
	writeTestSkill(t, filepath.Join(validRoot, "one-by-one"), "---\nname: one-by-one\ndescription: walk findings\n---\n")
	writeTestSkill(t, filepath.Join(invalidRoot, "one-by-one"), "---\nname: other\ndescription: walk findings\n---\n")
	writeTestSkill(t, filepath.Join(foreignRoot, "hand-copy"), "---\nname: hand-copy\ndescription: not ours\n---\n")

	section := CheckSkills(config.Paths{Global: dir, GlobalExists: true}, SkillDestScan{
		Scanned: true,
		Global:  []skills.Dest{{Root: validRoot}, {Root: invalidRoot}, {Root: foreignRoot}},
	})

	var pass, fail int
	for _, r := range section.Results {
		if strings.Contains(r.Label, "hand-copy") {
			t.Errorf("foreign dest should not be checked: %+v", r)
		}
		switch r.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
			if !strings.Contains(r.Label, "invalid") {
				t.Errorf("fail should be the invalid dest, got %q", r.Label)
			}
		}
	}
	if pass != 1 || fail != 1 {
		t.Errorf("pass=%d fail=%d results=%+v", pass, fail, section.Results)
	}
}

func TestCheckSkillsEmptyDirIsDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := skills.Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "skills")
	if err := os.MkdirAll(filepath.Join(root, "one-by-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	section := CheckSkills(config.Paths{Global: dir, GlobalExists: true}, SkillDestScan{
		Scanned: true,
		Global:  []skills.Dest{{Root: root}},
	})
	found := false
	for _, r := range section.Results {
		if r.Label == "workflows/one-by-one" && r.Status == StatusNotFound && r.Fix != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("empty leftover dir should be drift: %+v", section.Results)
	}
}

func TestCheckSkillsStatErrorIsNotDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := skills.Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "skills")
	dest := filepath.Join(root, "one-by-one")
	writeTestSkill(t, dest, "---\nname: one-by-one\ndescription: walk\n---\n")
	if err := os.Chmod(dest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err == nil {
		t.Skip("stat succeeds without directory traverse permission")
	}

	section := CheckSkills(config.Paths{Global: dir, GlobalExists: true}, SkillDestScan{
		Scanned: true,
		Global:  []skills.Dest{{Root: root}},
	})
	found := false
	for _, r := range section.Results {
		if r.Label != "workflows/one-by-one" {
			continue
		}
		found = true
		if r.Status != StatusFail {
			t.Errorf("status = %v, want fail", r.Status)
		}
		if r.Fix != "" {
			t.Errorf("stat error must not offer install Fix: %+v", r)
		}
	}
	if !found {
		t.Errorf("want fail for unreadable dest: %+v", section.Results)
	}
}

func writeTestSkill(t *testing.T, dest, body string) {
	t.Helper()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
