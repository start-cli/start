package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/p3bot/agentdex"
	"github.com/p3bot/start/internal/registry"
	"github.com/p3bot/start/internal/skills"
)

const skillModulePath = "github.com/p3bot/library/skills/workflows/one-by-one@v1"

func skillTestIndex() *registry.Index {
	return &registry.Index{
		Agents:   map[string]registry.IndexEntry{},
		Roles:    map[string]registry.IndexEntry{},
		Contexts: map[string]registry.IndexEntry{},
		Tasks:    map[string]registry.IndexEntry{},
		Skills: map[string]registry.IndexEntry{
			"workflows/one-by-one": {
				Module:      skillModulePath,
				Description: "Walk a list of findings one at a time",
				Version:     stubVersion,
				Tags:        []string{"workflow", "remediation"},
			},
		},
	}
}

func writeSkillFixture(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "skill-module")
	files := map[string]string{
		"SKILL.md":            "---\nname: one-by-one\ndescription: walk findings\n---\nWalk findings one by one.\n",
		"scripts/run.sh":      "#!/bin/sh\necho hi\n",
		"references/notes.md": "# notes\n",
		"skill.cue":           "package onebyone\n",
		"cue.mod/module.cue":  "module: \"github.com/p3bot/library/skills/workflows/one-by-one@v1\"\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func skillCatalogDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "skills", "testdata", "catalog")
}

func setupSkillCmd(t *testing.T, found []string) (home string, stub *registryStub, run func(args ...string) (stdout, stderr string, err error)) {
	t.Helper()
	home, stub = setupSkillEnv(t)
	return home, stub, skillRunner(t, stub, fixtureCatalogOpts(t, home, found))
}

func setupSkillEnv(t *testing.T) (home string, stub *registryStub) {
	t.Helper()
	idx := skillTestIndex()
	home, stub = setupStartTestConfigWithRegistry(t, idx)
	skillDir := writeSkillFixture(t, home)
	stub.SetFetch(skillModulePath, registry.FetchResult{SourceDir: skillDir}, nil)
	return home, stub
}

func fixtureCatalogOpts(t *testing.T, home string, found []string) []agentdex.Option {
	t.Helper()
	bins := map[string]string{}
	for _, id := range found {
		p := filepath.Join(home, "bin-"+id)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		bins[id] = p
	}
	return []agentdex.Option{
		agentdex.WithCatalogDir(skillCatalogDir(t)),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithBinPaths(bins),
		agentdex.WithEnvLookup(func(k string) (string, bool) {
			if k == "HOME" {
				return home, true
			}
			return "", false
		}),
		agentdex.WithWorkingDir(home),
	}
}

func skillRunner(t *testing.T, stub *registryStub, catalogOpts []agentdex.Option) func(args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return func(args ...string) (string, string, error) {
		cmd := NewRootCmd()
		ctx := WithProvider(cmd.Context(), func() (registry.Client, error) {
			return stub, nil
		})
		if catalogOpts != nil {
			ctx = WithSkillCatalogOpts(ctx, catalogOpts...)
		}
		ctx = withResolverSource(ctx, injectedIndexSource{index: stub.idx, client: stub})
		cmd.SetContext(ctx)
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetIn(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), errBuf.String(), err
	}
}

func TestInstallSkillPrefixedAndBare(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"claude-code", "agy"})

	stdout, _, err := run("install", "skills:workflows/one-by-one")
	if err != nil {
		t.Fatalf("prefixed install: %v\n%s", err, stdout)
	}
	primary := filepath.Join(home, ".agents", "skills", "one-by-one")
	assertSkillDest(t, primary)
	assertInventory(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start"), "workflows/one-by-one")

	// Bare name takes the same materialise path.
	home2, _, run2 := setupSkillCmd(t, []string{"claude-code", "agy"})
	stdout, _, err = run2("install", "workflows/one-by-one")
	if err != nil {
		t.Fatalf("bare install: %v\n%s", err, stdout)
	}
	assertSkillDest(t, filepath.Join(home2, ".agents", "skills", "one-by-one"))
}

func TestInstallSkillNamedAgentsAndLocal(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"claude-code"})

	stdout, _, err := run("install", "skills:workflows/one-by-one", "--agent", "claude-code,agy")
	if err != nil {
		t.Fatalf("named install: %v\n%s", err, stdout)
	}
	assertSkillDest(t, filepath.Join(home, ".claude", "skills", "one-by-one"))
	assertSkillDest(t, filepath.Join(home, ".agents", "skills", "one-by-one"))

	home2, _, run2 := setupSkillCmd(t, nil)
	stdout, _, err = run2("install", "--local", "skills:workflows/one-by-one", "--agent", "claude-code")
	if err != nil {
		t.Fatalf("local install: %v\n%s", err, stdout)
	}
	assertSkillDest(t, filepath.Join(home2, ".claude", "skills", "one-by-one"))
	assertInventory(t, filepath.Join(home2, ".start"), "workflows/one-by-one")
}

func TestLaunchRejectsMultipleAgents(t *testing.T) {
	chdir(t, setupStartTestConfig(t))
	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dry-run", "--agent", "echo", "--agent", "other"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at most one --agent") {
		t.Fatalf("launch should reject multiple --agent, got %v", err)
	}
}

func TestInstallSkillRejectsLibraryAgentName(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"claude-code"})
	_, _, err := run("install", "skills:workflows/one-by-one", "--agent", "claude/interactive")
	if err == nil || !strings.Contains(err.Error(), "unknown agentdex id") {
		t.Fatalf("want unknown agentdex id, got %v", err)
	}
}

func TestInstallSkillSkipsOtherScopeAgent(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy", "local-only"})

	stdout, _, err := run("install", "skills:workflows/one-by-one")
	if err != nil {
		t.Fatalf("install with local-only sibling: %v\n%s", err, stdout)
	}
	assertSkillDest(t, filepath.Join(home, ".agents", "skills", "one-by-one"))
}

func TestInstallSkillHardFails(t *testing.T) {
	t.Run("empty detected", func(t *testing.T) {
		_, _, run := setupSkillCmd(t, nil)
		_, _, err := run("install", "skills:workflows/one-by-one")
		if err == nil || !strings.Contains(err.Error(), "no skill-capable agent") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("not skill-capable", func(t *testing.T) {
		_, _, run := setupSkillCmd(t, nil)
		_, _, err := run("install", "skills:workflows/one-by-one", "--agent", "noskills")
		if err == nil || !strings.Contains(err.Error(), "not skill-capable") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no writable named path", func(t *testing.T) {
		_, _, run := setupSkillCmd(t, nil)
		_, _, err := run("install", "skills:workflows/one-by-one", "--agent", "alt-only")
		if err == nil || !strings.Contains(err.Error(), "no writable skills path") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestInstallSkillOverwritesLeftovers(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	dest := filepath.Join(home, ".agents", "skills", "one-by-one")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dest, "old.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("leftover should be gone")
	}
	assertSkillDest(t, dest)

	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	assertInventory(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start"), "workflows/one-by-one")
}

func TestInstallSkillRejectsSameLeaf(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatalf("reinstall same key: %v", err)
	}

	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(configDir, "review/one-by-one", "github.com/example/review@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	_, _, err := run("install", "skills:workflows/one-by-one")
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("want leaf conflict, got %v", err)
	}
	_ = home
}

func TestInstallSkillDoesNotWriteAgentsCue(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "agents.cue")
	data, err := os.ReadFile(agentsPath)
	if err == nil && strings.Contains(string(data), "skills") {
		t.Fatalf("agents.cue must not gain skills paths:\n%s", data)
	}
}

func TestListAndConfigRejectSkills(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, skillTestIndex())

	_, _, err := captureStreams(t, stub, "list", "skills")
	if err == nil || !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("list skills: %v", err)
	}

	_, _, err = captureStreams(t, stub, "config", "add", "skill")
	if err == nil || !strings.Contains(err.Error(), "not a config-merge module") {
		t.Fatalf("config add skill: %v", err)
	}

	_, _, err = captureStreams(t, stub, "update", "skills")
	if err == nil || !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("update skills: %v", err)
	}
}

func TestConfigGetDoesNotDumpSkillInventory(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	_ = home

	_, _, err := run("config", "get", "workflows/one-by-one")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("config get full name: %v", err)
	}
	_, _, err = run("config", "get", "one-by-one")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("config get leaf: %v", err)
	}
}

func TestUninstallSkillByAddressAndLeaf(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, ".agents", "skills", "one-by-one")
	if _, _, err := run("uninstall", "--force", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("dest should be removed")
	}
	entries, err := skills.Load(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["workflows/one-by-one"]; ok {
		t.Fatal("inventory entry should be gone")
	}

	home2, _, run2 := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run2("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run2("uninstall", "--force", "skills:one-by-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home2, ".agents", "skills", "one-by-one")); !os.IsNotExist(err) {
		t.Fatal("leaf uninstall should remove dest")
	}
}

func TestUninstallSkillAmbiguousLeaf(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(configDir, "other/one-by-one", "github.com/example/other@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	entries, err := skills.Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("pre-uninstall inventory = %v", entries)
	}
	_, _, err = run("uninstall", "--force", "skills:one-by-one")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous leaf, got %v", err)
	}
	_ = home
}

func TestUninstallSkillPrefixAmbiguityNotSwallowed(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start")
	if err := skills.Upsert(configDir, "workflows/triage", "github.com/example/triage@v1", "v1.0.0"); err != nil {
		t.Fatal(err)
	}

	_, _, err := run("uninstall", "--force", "skills:workflows")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous prefix, got %v", err)
	}
	entries, err := skills.Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["workflows/one-by-one"]; !ok {
		t.Fatal("prefix miss must not remove the installed skill")
	}
	_ = home
}

func TestGetSkillWithoutStartConfig(t *testing.T) {
	home := isolateConfigEnv(t)
	chdir(t, home)
	idx := skillTestIndex()
	if idx.Skills == nil {
		idx.Skills = map[string]registry.IndexEntry{}
	}
	fixtureDir := filepath.Join(home, "index-fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "index.cue"), []byte(renderIndexCUE(idx)), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := newRegistryStub(idx, fixtureDir)
	skillDir := writeSkillFixture(t, home)
	stub.SetFetch(skillModulePath, registry.FetchResult{SourceDir: skillDir}, nil)

	cmd := NewRootCmd()
	cmd.SetContext(withResolverSource(cmd.Context(), injectedIndexSource{index: stub.idx, client: stub}))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(&bytes.Buffer{})
	cmd.SetArgs([]string{"get", "skills:workflows/one-by-one"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("get without start config: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Walk findings one by one.") {
		t.Errorf("missing SKILL.md body:\n%s", out.String())
	}
}

func TestGetSkillPrintsBodyWithoutInstall(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})

	for _, query := range []string{"skills:workflows/one-by-one", "workflows/one-by-one"} {
		stdout, _, err := run("get", query)
		if err != nil {
			t.Fatalf("get %s: %v\n%s", query, err, stdout)
		}
		if !strings.Contains(stdout, "Walk findings one by one.") {
			t.Errorf("get %s missing body:\n%s", query, stdout)
		}
		if strings.Contains(stdout, "skill.cue") || strings.Contains(stdout, "Install targets") {
			t.Errorf("get %s should be SKILL.md only:\n%s", query, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("get must not write skills.cue")
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "one-by-one")); !os.IsNotExist(err) {
		t.Fatal("get must not create dest dirs")
	}
}

func TestDescribeSkillWithoutDetectedAgents(t *testing.T) {
	_, _, run := setupSkillCmd(t, nil)

	stdout, _, err := run("describe", "skills:workflows/one-by-one")
	if err != nil {
		t.Fatalf("describe without detected agents: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Skill:", "workflows/one-by-one", "Walk a list of findings", "SKILL.md", "Install targets:", "(none)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("describe missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("describe must not write skills.cue")
	}
}

func TestDescribeSkillMetadataFilesAndTargets(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})

	for _, query := range []string{"skills:workflows/one-by-one", "workflows/one-by-one"} {
		stdout, _, err := run("describe", query)
		if err != nil {
			t.Fatalf("describe %s: %v\n%s", query, err, stdout)
		}
		for _, want := range []string{"Skill:", "workflows/one-by-one", "Walk a list of findings", "SKILL.md", "scripts/run.sh", "Install targets:"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("describe %s missing %q:\n%s", query, want, stdout)
			}
		}
		if strings.Contains(stdout, "cue.mod") || strings.Contains(stdout, "skill.cue") {
			t.Errorf("describe %s should omit cue.mod/skill.cue:\n%s", query, stdout)
		}
		if !strings.Contains(stdout, filepath.Join(home, ".agents", "skills", "one-by-one")) {
			t.Errorf("describe %s missing install target:\n%s", query, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("describe must not write skills.cue")
	}
}

func TestInstallSkillMaterialiseFailureLeavesNoInventory(t *testing.T) {
	home, _, run := setupSkillCmd(t, []string{"agy"})
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "skills"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run("install", "skills:workflows/one-by-one"); err == nil {
		t.Fatal("install should fail when dest cannot be written")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("inventory must not be written when materialise fails")
	}
}

func TestInstallSkillCatalogFailClosed(t *testing.T) {
	home, stub := setupSkillEnv(t)
	run := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	_, _, err := run("install", "skills:workflows/one-by-one")
	if err == nil || !strings.Contains(err.Error(), "agent catalog unavailable") {
		t.Fatalf("want catalog fail-closed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("install must not write inventory when catalog fails")
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "one-by-one")); !os.IsNotExist(err) {
		t.Fatal("install must not write dests when catalog fails")
	}
}

func TestDescribeSkillCatalogFailClosed(t *testing.T) {
	home, stub := setupSkillEnv(t)
	run := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	_, _, err := run("describe", "skills:workflows/one-by-one")
	if err == nil || !strings.Contains(err.Error(), "agent catalog unavailable") {
		t.Fatalf("want catalog fail-closed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start", "skills.cue")); !os.IsNotExist(err) {
		t.Fatal("describe must not write inventory")
	}
}

func TestUninstallSkillCatalogFailClosedKeepsInventory(t *testing.T) {
	home, stub, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	fail := skillRunner(t, stub, []agentdex.Option{
		agentdex.WithCatalogDir(filepath.Join(home, "missing-catalog")),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithWorkingDir(home),
	})
	_, _, err := fail("uninstall", "--force", "skills:workflows/one-by-one")
	if err == nil || !strings.Contains(err.Error(), "agent catalog unavailable") {
		t.Fatalf("want catalog fail-closed, got %v", err)
	}
	entries, err := skills.Load(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "start"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["workflows/one-by-one"]; !ok {
		t.Fatal("inventory entry must stay when catalog fails")
	}
}

func TestGetSkillWithoutAgentdex(t *testing.T) {
	_, stub := setupSkillEnv(t)
	run := skillRunner(t, stub, nil)
	stdout, _, err := run("get", "skills:workflows/one-by-one")
	if err != nil {
		t.Fatalf("get without agentdex: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Walk findings one by one.") {
		t.Errorf("missing SKILL.md body:\n%s", stdout)
	}
}

func TestDescribeSkillAfterInstallKeepsLibraryMetadata(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := run("describe", "workflows/one-by-one")
	if err != nil {
		t.Fatalf("describe after install: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Walk a list of findings") {
		t.Errorf("description missing after install:\n%s", stdout)
	}
	if !strings.Contains(stdout, "workflow") {
		t.Errorf("tags missing after install:\n%s", stdout)
	}
}

func TestLibraryListsSkills(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, skillTestIndex())
	stdout, _, err := captureStreams(t, stub, "library", "skills")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "workflows/one-by-one") {
		t.Errorf("library skills missing entry:\n%s", stdout)
	}
}

func TestSearchListsSkills(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, skillTestIndex())
	stdout, _, err := captureStreams(t, stub, "search", "one-by-one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "skills:") {
		t.Errorf("search text missing skills category:\n%s", stdout)
	}
	if !strings.Contains(stdout, "workflows/one-by-one") {
		t.Errorf("search text missing skill hit:\n%s", stdout)
	}
}

func TestInstalledSkillIsStarred(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	if _, _, err := run("install", "skills:workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"library", "skills"},
		{"search", "one-by-one"},
	} {
		stdout, _, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stdout)
		}
		if !strings.Contains(stdout, "★") || !strings.Contains(stdout, "workflows/one-by-one") {
			t.Errorf("%v: want starred skill, got:\n%s", args, stdout)
		}
	}
}

func TestInstallSkillsPrefixNotUnknownCategory(t *testing.T) {
	_, _, run := setupSkillCmd(t, []string{"agy"})
	_, _, err := run("install", "skills:workflows/one-by-one")
	if err != nil && strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("skills: prefix must not be unknown category: %v", err)
	}
}

func assertSkillDest(t *testing.T, dest string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing under %s: %v", dest, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "run.sh")); err != nil {
		t.Fatalf("scripts missing under %s: %v", dest, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill.cue")); !os.IsNotExist(err) {
		t.Fatalf("skill.cue should not be at %s", dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "cue.mod")); !os.IsNotExist(err) {
		t.Fatalf("cue.mod should not be at %s", dest)
	}
}

func assertInventory(t *testing.T, configDir, key string) {
	t.Helper()
	entries, err := skills.Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := entries[key]
	if !ok {
		t.Fatalf("inventory missing %s in %s: %v", key, configDir, entries)
	}
	if e.Origin == "" || e.Version == "" {
		t.Errorf("inventory entry incomplete: %+v", e)
	}
	data, err := os.ReadFile(skills.InventoryPath(configDir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "targets") {
		t.Errorf("inventory must not record dests:\n%s", data)
	}
}
