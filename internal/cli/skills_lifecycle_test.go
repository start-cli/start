package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/agentdex"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/skills"
)

func TestListSkillsPresentAndMissing(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run("list", "skills")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "workflows/one-by-one") {
		t.Errorf("list missing skill:\n%s", stdout)
	}
	if strings.Contains(stdout, "missing") {
		t.Errorf("present dest should not be missing:\n%s", stdout)
	}

	decoded := listSkillsJSON(t, run)
	if got := skillRowStatus(decoded, "workflows/one-by-one", "global"); got != "" {
		t.Errorf("present json status = %q, want omitted", got)
	}

	dest := filepath.Join(home, ".agents", "skills", "one-by-one")
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = run("list", "skills")
	if err != nil {
		t.Fatalf("list after delete: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "missing") {
		t.Errorf("want missing annotation:\n%s", stdout)
	}
	decoded = listSkillsJSON(t, run)
	if got := skillRowStatus(decoded, "workflows/one-by-one", "global"); got != skillStatusMissing {
		t.Errorf("json status = %q, want missing", got)
	}
}

func TestListSkillsNativeOnlyPresent(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"claude-code"})
	if _, _, err := run("install", "skills:workflows/one-by-one", "--agent", "claude-code"); err != nil {
		t.Fatal(err)
	}
	assertSkillDest(t, filepath.Join(home, ".claude", "skills", "one-by-one"))
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "one-by-one", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("named native install should not write primary")
	}
	decoded := listSkillsJSON(t, run)
	if got := skillRowStatus(decoded, "workflows/one-by-one", "global"); got != "" {
		t.Errorf("native-only present should omit status, got %q", got)
	}
	stdout, _, _ := run("doctor", "--json")
	if strings.Contains(stdout, "missing SKILL.md") {
		t.Errorf("native-only named install must not be drift:\n%s", stdout)
	}
}

func TestUpdateSkillNativeOnlyDoesNotSpread(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"claude-code"})
	if _, _, err := run("install", "skills:workflows/one-by-one", "--agent", "claude-code"); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(home, ".claude", "skills", "one-by-one")
	primary := filepath.Join(home, ".agents", "skills", "one-by-one")
	assertSkillDest(t, native)
	if _, err := os.Stat(filepath.Join(primary, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("named native install should not write primary")
	}

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(native, "old.txt")
	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row := findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] != true {
		t.Fatalf("native dest should rematerialise: %v", row)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("native dest should be rewritten")
	}
	assertSkillDest(t, native)
	if _, err := os.Stat(filepath.Join(primary, "SKILL.md")); !os.IsNotExist(err) {
		t.Error("update must not spread a native-only install onto the primary dest")
	}
	assertInventoryVersion(t, cfg, "workflows/one-by-one", stubVersion)
}

func TestListSkillsIgnoresForeignAndEmptyDir(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, ".agents", "skills", "hand-copy")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("---\nname: hand-copy\ndescription: other\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	decoded := listSkillsJSON(t, run)
	for _, row := range decoded {
		if row["name"] == "hand-copy" {
			t.Fatalf("foreign dest listed: %v", row)
		}
	}

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "review/pre-commit", "github.com/p3bot/library/skills/review/pre-commit@v1.0.0", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	decoded = listSkillsJSON(t, run)
	if got := skillRowStatus(decoded, "review/pre-commit", "global"); got != skillStatusMissing {
		t.Errorf("empty leftover dir status = %q, want missing", got)
	}
}

func TestListSkillsDualScope(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("install", "--local", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	decoded := listSkillsJSON(t, run)
	var global, local bool
	for _, row := range decoded {
		if row["name"] != "workflows/one-by-one" {
			continue
		}
		switch row["scope"] {
		case "global":
			global = true
		case "local":
			local = true
		}
	}
	if !global || !local {
		t.Fatalf("want global and local rows, got %v", decoded)
	}
}

func TestListNonSkillCategorySkipsDestScan(t *testing.T) {
	home, stub, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	fail := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	stdout, stderr, err := fail("list", "agents")
	if err != nil {
		t.Fatalf("list agents: %v\n%s", err, stdout)
	}
	if strings.Contains(stderr, "dest scan skipped") {
		t.Errorf("list agents must not dest-scan skills, stderr=%s", stderr)
	}
	if strings.Contains(stdout, "workflows/one-by-one") {
		t.Errorf("list agents must not emit skills:\n%s", stdout)
	}
}

func TestListSkillsCatalogDown(t *testing.T) {
	home, stub, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	fail := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	stdout, stderr, err := fail("list", "skills")
	if err != nil {
		t.Fatalf("list catalog-down: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "workflows/one-by-one") {
		t.Errorf("want inventory identity:\n%s", stdout)
	}
	if strings.Contains(stdout, "missing") {
		t.Errorf("catalog-down must not annotate missing:\n%s", stdout)
	}
	if !strings.Contains(stderr, "dest scan skipped") {
		t.Errorf("want dest-scan warning, stderr=%s", stderr)
	}
	stdout, _, err = fail("list", "skills", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatal(err)
	}
	if got := skillRowStatus(rows, "workflows/one-by-one", "global"); got != "" {
		t.Errorf("catalog-down json status = %q, want omitted", got)
	}
}

func TestListUpdateSkillDestStatError(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, ".agents", "skills", "one-by-one")
	lockSkillDest(t, dest)

	stdout, stderr, err := run("list", "skills", "--json")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, stdout)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatal(err)
	}
	if got := skillRowStatus(rows, "workflows/one-by-one", "global"); got != "" {
		t.Errorf("unreadable dest must not be status missing, got %q", got)
	}
	if !strings.Contains(stderr, "could not check some skill dests") {
		t.Errorf("want dest-check warning, stderr=%s", stderr)
	}

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row := findSkillUpdate(t, res, "workflows/one-by-one")
	msg, _ := row["error"].(string)
	if msg == "" {
		t.Fatalf("want per-skill error, got %v", row)
	}
	if strings.Contains(msg, "start install") {
		t.Errorf("stat error must not use the no-dest install hint: %q", msg)
	}
}

func TestUpdateSkillNewerAndForce(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, ".agents", "skills", "one-by-one")
	marker := filepath.Join(dest, "old.txt")
	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row := findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] != true {
		t.Errorf("newer should update: %v", row)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("rematerialise should replace dest contents")
	}
	assertInventoryVersion(t, cfg, "workflows/one-by-one", stubVersion)

	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row = findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] == true {
		t.Errorf("current should not update: %v", row)
	}

	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--force", "--json")
	row = findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] != true {
		t.Errorf("--force should rematerialise: %v", row)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("--force should replace dest contents")
	}
}

func TestUpdateSkillDoesNotCreateDests(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}

	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--dry-run", "--json")
	row := findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] == true || row["error"] == nil {
		t.Errorf("dry-run of no dest should preview the error: %v", row)
	}

	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row = findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] == true {
		t.Errorf("newer with no dest must not report updated: %v", row)
	}
	msg, _ := row["error"].(string)
	if !strings.Contains(msg, skills.InstallCommand("workflows/one-by-one", false)) {
		t.Errorf("global no-dest error missing install command: %q", msg)
	}
	if strings.Contains(msg, "--local") {
		t.Errorf("global no-dest error must not include --local: %q", msg)
	}
	assertInventoryVersion(t, cfg, "workflows/one-by-one", "v0.0.1")
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "one-by-one")); !os.IsNotExist(err) {
		t.Error("update must not create dests")
	}

	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--force", "--json")
	row = findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] == true || row["error"] == nil {
		t.Errorf("--force with no dest should error: %v", row)
	}
	assertInventoryVersion(t, cfg, "workflows/one-by-one", "v0.0.1")

	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@"+stubVersion, stubVersion); err != nil {
		t.Fatal(err)
	}
	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row = findSkillUpdate(t, res, "workflows/one-by-one")
	if row["updated"] == true {
		t.Errorf("current with dests missing is not an update: %v", row)
	}
	if _, ok := row["error"]; ok {
		t.Errorf("current missing dests must not error: %v", row)
	}
}

func TestUpdateSkillLocalNoDestIncludesLocalFlag(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if err := skills.Upsert(filepath.Join(home, ".start"), "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	row := findSkillUpdate(t, res, "workflows/one-by-one")
	msg, _ := row["error"].(string)
	want := skills.InstallCommand("workflows/one-by-one", true)
	if !strings.Contains(msg, want) {
		t.Errorf("local no-dest error = %q, want %q", msg, want)
	}
}

func TestUpdateSkillQuerySelection(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "review/pre-commit", "github.com/p3bot/library/skills/review/pre-commit@v1.0.0", "v1.0.0"); err != nil {
		t.Fatal(err)
	}

	res := updateJSON(t, run, "update", "skills", "--json")
	if !hasSkillUpdate(res, "workflows/one-by-one") || !hasSkillUpdate(res, "review/pre-commit") {
		t.Errorf("update skills should select every skill: %v", res)
	}

	res = updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	if !hasSkillUpdate(res, "workflows/one-by-one") {
		t.Errorf("prefixed address missing: %v", res)
	}
	if hasSkillUpdate(res, "review/pre-commit") {
		t.Errorf("skills:name must not select every skill: %v", res)
	}

	res = updateJSON(t, run, "update", "skills:workflows", "--json")
	if !hasSkillUpdate(res, "workflows/one-by-one") {
		t.Errorf("unique prefix missing: %v", res)
	}
	if hasSkillUpdate(res, "review/pre-commit") {
		t.Errorf("unique prefix must not select the other skill: %v", res)
	}

	res = updateJSON(t, run, "update", "skills:one-by-one", "--json")
	if !hasSkillUpdate(res, "workflows/one-by-one") {
		t.Errorf("unique dest leaf missing: %v", res)
	}
	if hasSkillUpdate(res, "review/pre-commit") {
		t.Errorf("unique dest leaf must not select the other skill: %v", res)
	}

	res = updateJSON(t, run, "update", "one-by-one", "--json")
	if !hasSkillUpdate(res, "workflows/one-by-one") {
		t.Errorf("bare substring should match: %v", res)
	}
}

func TestUpdateSkillAddressNotFoundAndAmbiguous(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	_, _, err := run("update", "skills:missing-skill")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not found, got %v", err)
	}

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "other/one-by-one", "github.com/p3bot/library/skills/other/one-by-one@v1.0.0", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	_, _, err = run("update", "skills:one-by-one")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous leaf, got %v", err)
	}

	if err := skills.Upsert(cfg, "workflows/other", "github.com/p3bot/library/skills/workflows/other@v1.0.0", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	_, _, err = run("update", "skills:workflows")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous prefix, got %v", err)
	}
}

func TestUpdateSkillCatalogDownStillUpdatesOthers(t *testing.T) {
	home, stub := setupSkillEnv(t)
	idx := stub.idx
	idx.Agents = stubLibraryIndex().Agents
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(cfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	writeInstalledAgentAt(t, home, "agents.cue", "v0.0.1")
	stub.SetFetch(sentinelModuleBase, registry.FetchResult{
		SourceDir: buildModuleFixture(t, "agent", agentFixtureBody),
	}, nil)

	fail := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	res := updateJSON(t, fail, "update", "--json")
	skillRow := findSkillUpdate(t, res, "workflows/one-by-one")
	if skillRow["updated"] == true || skillRow["error"] == nil {
		t.Errorf("catalog-down skill should error: %v", skillRow)
	}
	assertInventoryVersion(t, cfg, "workflows/one-by-one", "v0.0.1")

	agentRow := findUpdateByName(t, res, sentinelAgentName)
	if agentRow["updated"] != true {
		t.Errorf("other categories should still update: %v", agentRow)
	}
}

func TestUpdateSkillDualScope(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("install", "--local", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	globalCfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	localCfg := filepath.Join(home, ".start")
	if err := skills.Upsert(globalCfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := skills.Upsert(localCfg, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v0.0.1", "v0.0.1"); err != nil {
		t.Fatal(err)
	}

	res := updateJSON(t, run, "update", "skills:workflows/one-by-one", "--json")
	var global, local bool
	for _, row := range res {
		mod, _ := row["module"].(map[string]any)
		if mod["name"] != "workflows/one-by-one" {
			continue
		}
		if row["updated"] != true {
			t.Errorf("dual-scope row not updated: %v", row)
		}
		switch mod["scope"] {
		case "global":
			global = true
		case "local":
			local = true
		}
	}
	if !global || !local {
		t.Fatalf("want both scopes updated: %v", res)
	}
	assertInventoryVersion(t, globalCfg, "workflows/one-by-one", stubVersion)
	assertInventoryVersion(t, localCfg, "workflows/one-by-one", stubVersion)
}

func TestDoctorSkillsDriftAndFrontmatter(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"claude-code", "agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one", "--agent", "claude-code,agy"); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(home, ".claude", "skills", "one-by-one", "SKILL.md")
	if err := os.WriteFile(invalid, []byte("---\nname: other\ndescription: walk\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, ".agents", "skills", "hand-copy")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("---\nname: hand-copy\ndescription: other\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run("doctor", "--json")
	if err == nil {
		t.Fatal("doctor should report issues")
	}
	if strings.Contains(stdout, "hand-copy") {
		t.Errorf("foreign dest should not appear:\n%s", stdout)
	}
	if !strings.Contains(stdout, "parent directory") {
		t.Errorf("want frontmatter fail on invalid dest:\n%s", stdout)
	}
	if !strings.Contains(stdout, filepath.ToSlash(filepath.Join(".agents", "skills", "one-by-one", "SKILL.md"))) &&
		!strings.Contains(stdout, "valid frontmatter") {
		t.Errorf("want pass on valid dest:\n%s", stdout)
	}

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	before, err := os.ReadFile(skills.InventoryPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = run("doctor", "--json")
	after, err := os.ReadFile(skills.InventoryPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("doctor must not rewrite inventory")
	}

	if err := os.RemoveAll(filepath.Join(home, ".claude", "skills", "one-by-one")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".agents", "skills", "one-by-one")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = run("doctor", "--json")
	if err == nil {
		t.Fatal("doctor should report drift")
	}
	if !strings.Contains(stdout, `"fix": "start install skills:workflows/one-by-one"`) {
		t.Errorf("want global Fix, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "--local") {
		t.Errorf("global drift must not include --local:\n%s", stdout)
	}
}

func TestDoctorSkillsLocalFixAndCatalogDown(t *testing.T) {
	home, stub, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "--local", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".agents", "skills", "one-by-one")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := run("doctor", "--json")
	if err == nil {
		t.Fatal("doctor should report local drift")
	}
	if !strings.Contains(stdout, `"fix": "start install skills:workflows/one-by-one --local"`) {
		t.Errorf("want --local Fix:\n%s", stdout)
	}

	fail := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	stdout, _, _ = fail("doctor", "--json")
	if !strings.Contains(stdout, "skipped (agent catalog unavailable)") {
		t.Errorf("want catalog-down skip:\n%s", stdout)
	}
	if strings.Contains(stdout, `"fix"`) && strings.Contains(stdout, "start install skills:") {
		t.Errorf("catalog-down must not report dest drift:\n%s", stdout)
	}
}

func TestDoctorSkillsBrokenLocalInventoryDoesNotFakeGlobalDrift(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	localInv := skills.InventoryPath(filepath.Join(home, ".start"))
	if err := os.WriteFile(localInv, []byte("skills: {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run("doctor", "--json")
	if err == nil {
		t.Fatal("doctor should report local inventory fail")
	}
	if !strings.Contains(stdout, "Local inventory") {
		t.Errorf("want local inventory fail:\n%s", stdout)
	}
	if strings.Contains(stdout, `"fix": "start install skills:workflows/one-by-one"`) {
		t.Errorf("present global dest must not be drift:\n%s", stdout)
	}
	if !strings.Contains(stdout, "valid frontmatter") {
		t.Errorf("want dest health on the global skill:\n%s", stdout)
	}
}

func listSkillsJSON(t *testing.T, run func(args ...string) (string, string, error)) []map[string]any {
	t.Helper()
	stdout, _, err := run("list", "skills", "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, stdout)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode list json: %v\n%s", err, stdout)
	}
	return rows
}

func skillRowStatus(rows []map[string]any, name, scope string) string {
	for _, row := range rows {
		if row["name"] == name && row["scope"] == scope {
			s, _ := row["status"].(string)
			return s
		}
	}
	return ""
}

func updateJSON(t *testing.T, run func(args ...string) (string, string, error), args ...string) []map[string]any {
	t.Helper()
	stdout, _, err := run(args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, stdout)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode update json: %v\n%s", err, stdout)
	}
	return rows
}

func findSkillUpdate(t *testing.T, rows []map[string]any, name string) map[string]any {
	t.Helper()
	for _, row := range rows {
		mod, _ := row["module"].(map[string]any)
		if mod["category"] == "skills" && mod["name"] == name {
			return row
		}
	}
	t.Fatalf("no skill update row for %s: %v", name, rows)
	return nil
}

func hasSkillUpdate(rows []map[string]any, name string) bool {
	for _, row := range rows {
		mod, _ := row["module"].(map[string]any)
		if mod["category"] == "skills" && mod["name"] == name {
			return true
		}
	}
	return false
}

func findUpdateByName(t *testing.T, rows []map[string]any, name string) map[string]any {
	t.Helper()
	for _, row := range rows {
		mod, _ := row["module"].(map[string]any)
		if mod["name"] == name {
			return row
		}
	}
	t.Fatalf("no update row for %s: %v", name, rows)
	return nil
}

func assertInventoryVersion(t *testing.T, configDir, key, version string) {
	t.Helper()
	entries, err := skills.Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := entries[key]
	if !ok {
		t.Fatalf("inventory missing %s", key)
	}
	if e.Version != version {
		t.Errorf("version = %q, want %q", e.Version, version)
	}
}

func lockSkillDest(t *testing.T, dest string) {
	t.Helper()
	if err := os.Chmod(dest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err == nil {
		t.Skip("stat succeeds without directory traverse permission")
	}
}
