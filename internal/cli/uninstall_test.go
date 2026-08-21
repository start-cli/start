package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile seeds one CUE config file under dir, creating dir as needed.
func writeConfigFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

const seedAgentsCUE = `// start configuration
// Managed by 'start install'
agents: {
	claude: {
		origin:  "github.com/x/claude@v0.1.0"
		bin:     "claude"
		command: "{{.bin}}"
	}
	gpt: {
		bin:     "gpt"
		command: "{{.bin}}"
	}
}
`

const seedRolesCUE = `// start configuration
roles: {
	"golang/assistant": {
		prompt: "Go expert."
	}
	"python/assistant": {
		prompt: "Python expert."
	}
}
`

const seedTasksCUE = `// start configuration
tasks: {
	review: {
		prompt: "Review."
	}
}
`

const seedSettingsCUE = `settings: {
	default_agent: "claude"
}
`

// seedGlobalConfig writes the standard four-category fixture into the global
// config dir and returns that dir. isolateConfigEnv points XDG_CONFIG_HOME at a
// temp dir, so global config lives at <tmp>/start.
func seedGlobalConfig(t *testing.T) string {
	t.Helper()
	tmpDir := isolateConfigEnv(t)
	globalDir := filepath.Join(tmpDir, "start")
	writeConfigFile(t, globalDir, "agents.cue", seedAgentsCUE)
	writeConfigFile(t, globalDir, "roles.cue", seedRolesCUE)
	writeConfigFile(t, globalDir, "tasks.cue", seedTasksCUE)
	writeConfigFile(t, globalDir, "settings.cue", seedSettingsCUE)
	return globalDir
}

// runUninstallCmd executes a fresh root command with a non-TTY empty stdin.
func runUninstallCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCmd()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestUninstall_DryRunDoesNotRemove(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	stdout, _, err := runUninstallCmd(t, "uninstall", "gpt", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run uninstall without --force: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Dry run - no changes applied") {
		t.Errorf("want dry-run header, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, `Would remove agent "gpt"`) {
		t.Errorf("want would-remove line, got:\n%s", stdout)
	}
	if !strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "gpt") {
		t.Error("dry-run must leave gpt installed")
	}
}

func TestConfigRemoveInteractive_DryRunDoesNotRemove(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	// 1 = agents, 2 = gpt (sorted: claude, gpt). No confirm line: dry-run must
	// skip it the way runRemoval does.
	err := runConfigRemoveInteractive(slowStdin("1\n2\n"), stdout, stderr, false, false, false, true)
	if err != nil {
		t.Fatalf("interactive dry-run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout.String(), "Dry run - no changes applied") {
		t.Errorf("want dry-run header, got:\n%s", stdout)
	}
	if !strings.Contains(stdout.String(), `Would remove agent "gpt"`) {
		t.Errorf("want would-remove line, got:\n%s", stdout)
	}
	if strings.Contains(stdout.String(), "Remove agent") {
		t.Errorf("dry-run must not ask to confirm a delete:\n%s", stdout)
	}
	if !strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "gpt") {
		t.Error("dry-run must leave gpt installed")
	}
}

func TestUninstall_GlobalRemovalPreservesSiblingsAndComments(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	if _, _, err := runUninstallCmd(t, "uninstall", "gpt", "--force"); err != nil {
		t.Fatalf("uninstall gpt: %v", err)
	}

	agents := readFileString(t, filepath.Join(globalDir, "agents.cue"))
	if strings.Contains(agents, "gpt") {
		t.Errorf("gpt should be removed:\n%s", agents)
	}
	if !strings.Contains(agents, "claude") {
		t.Errorf("claude sibling should remain:\n%s", agents)
	}
	if !strings.Contains(agents, "Managed by 'start install'") {
		t.Errorf("managed comment header should be preserved:\n%s", agents)
	}
}

func TestUninstall_EmptiedCategoryStructIsRemoved(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	if _, _, err := runUninstallCmd(t, "uninstall", "review", "--force"); err != nil {
		t.Fatalf("uninstall review: %v", err)
	}

	tasks := readFileString(t, filepath.Join(globalDir, "tasks.cue"))
	if strings.Contains(tasks, "review") {
		t.Errorf("review should be removed:\n%s", tasks)
	}
	if strings.Contains(tasks, "tasks:") {
		t.Errorf("emptied tasks category should be dropped, not left as tasks: {}:\n%s", tasks)
	}
}

func TestUninstall_LocalScopeLeavesGlobalUntouched(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	chdir(t, tmpDir)
	globalDir := filepath.Join(tmpDir, "start")
	localDir := filepath.Join(tmpDir, ".start")
	writeConfigFile(t, globalDir, "roles.cue", seedRolesCUE)
	writeConfigFile(t, localDir, "roles.cue", seedRolesCUE)

	if _, _, err := runUninstallCmd(t, "uninstall", "golang/assistant", "--local", "--force"); err != nil {
		t.Fatalf("uninstall --local: %v", err)
	}

	local := readFileString(t, filepath.Join(localDir, "roles.cue"))
	if strings.Contains(local, "golang/assistant") {
		t.Errorf("golang/assistant should be removed from local:\n%s", local)
	}
	global := readFileString(t, filepath.Join(globalDir, "roles.cue"))
	if !strings.Contains(global, "golang/assistant") {
		t.Errorf("global config must be untouched:\n%s", global)
	}
}

func TestUninstall_AliasesBehaveIdentically(t *testing.T) {
	for _, alias := range []string{"uninstall", "remove", "rm"} {
		t.Run(alias, func(t *testing.T) {
			globalDir := seedGlobalConfig(t)
			if _, _, err := runUninstallCmd(t, alias, "gpt", "--force"); err != nil {
				t.Fatalf("%s gpt: %v", alias, err)
			}
			agents := readFileString(t, filepath.Join(globalDir, "agents.cue"))
			if strings.Contains(agents, "gpt") {
				t.Errorf("%s should remove gpt:\n%s", alias, agents)
			}
		})
	}
}

func TestUninstall_MultipleQueriesEachReported(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	stdout, _, err := runUninstallCmd(t, "uninstall", "gpt", "review", "--force")
	if err != nil {
		t.Fatalf("uninstall gpt review: %v", err)
	}
	if !strings.Contains(stdout, `Removed agent "gpt"`) {
		t.Errorf("expected agent removal report, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, `Removed task "review"`) {
		t.Errorf("expected task removal report, got:\n%s", stdout)
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "gpt") {
		t.Error("gpt should be removed")
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "tasks.cue")), "review") {
		t.Error("review should be removed")
	}
}

func TestUninstall_MultiQueryFailureDoesNotAbortRest(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	stdout, _, err := runUninstallCmd(t, "uninstall", "does-not-exist", "gpt", "--force")
	if err == nil {
		t.Fatal("expected error from the unresolved query")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
	if !strings.Contains(stdout, `Removed agent "gpt"`) {
		t.Errorf("gpt should still be removed despite sibling failure:\n%s", stdout)
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "gpt") {
		t.Error("gpt should be removed")
	}
}

func TestUninstall_NotFound(t *testing.T) {
	seedGlobalConfig(t)
	if _, _, err := runUninstallCmd(t, "uninstall", "missing", "--force"); err == nil {
		t.Fatal("expected not-found error")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found', got: %v", err)
	}
}

func TestUninstall_AmbiguousNonTTYErrors(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	globalDir := filepath.Join(tmpDir, "start")
	writeConfigFile(t, globalDir, "agents.cue", "agents: {\n\tshared: {bin: \"x\", command: \"x\"}\n}\n")
	writeConfigFile(t, globalDir, "roles.cue", "roles: {\n\tshared: {prompt: \"x\"}\n}\n")

	_, _, err := runUninstallCmd(t, "uninstall", "shared", "--force")
	if err == nil {
		t.Fatal("expected ambiguity error on non-TTY")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected 'ambiguous', got: %v", err)
	}
	if !strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "shared") {
		t.Error("nothing should be removed on ambiguity")
	}
}

func TestUninstall_NonInteractiveWithoutForceErrorsAndKeepsConfig(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	_, _, err := runUninstallCmd(t, "uninstall", "gpt")
	if err == nil {
		t.Fatal("expected error without --force on non-interactive stream")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected '--force' hint, got: %v", err)
	}
	if !strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "gpt") {
		t.Error("config must be unchanged when removal is declined")
	}
}

func TestUninstall_NoArgNonInteractiveErrors(t *testing.T) {
	seedGlobalConfig(t)
	_, _, err := runUninstallCmd(t, "uninstall")
	if err == nil {
		t.Fatal("expected error for no-arg uninstall on non-interactive stream")
	}
	if !strings.Contains(err.Error(), "query required in non-interactive mode") {
		t.Errorf("expected non-interactive query error, got: %v", err)
	}
}

func TestUninstall_DefaultAgentWarning(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	_, stderr, err := runUninstallCmd(t, "uninstall", "claude", "--force")
	if err != nil {
		t.Fatalf("uninstall claude: %v", err)
	}
	if !strings.Contains(stderr, "default_agent") {
		t.Errorf("expected default_agent warning on stderr, got: %q", stderr)
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "claude") {
		t.Error("claude should still be removed despite the warning")
	}
}

func TestUninstall_NonDefaultSameNameNoWarning(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	globalDir := filepath.Join(tmpDir, "start")
	// A role named "claude" shares the default_agent name but is not an agent.
	writeConfigFile(t, globalDir, "roles.cue", "roles: {\n\tclaude: {prompt: \"x\"}\n}\n")
	writeConfigFile(t, globalDir, "settings.cue", seedSettingsCUE)

	_, stderr, err := runUninstallCmd(t, "uninstall", "claude", "--force")
	if err != nil {
		t.Fatalf("uninstall role claude: %v", err)
	}
	if strings.Contains(stderr, "default_agent") {
		t.Errorf("removing a same-named role must not warn, got: %q", stderr)
	}
}

func TestUninstall_ShortNameRemovable(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	globalDir := filepath.Join(tmpDir, "start")
	writeConfigFile(t, globalDir, "agents.cue", "agents: {\n\ta: {bin: \"a\", command: \"a\"}\n}\n")

	if _, _, err := runUninstallCmd(t, "uninstall", "a", "--force"); err != nil {
		t.Fatalf("uninstall short name: %v", err)
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "\ta:") {
		t.Error("one-character agent should be removable by exact name")
	}
}

func TestUninstall_CategoryQualifierPrefixOnly(t *testing.T) {
	t.Run("prefix within category", func(t *testing.T) {
		globalDir := seedGlobalConfig(t)
		if _, _, err := runUninstallCmd(t, "uninstall", "roles:golang", "--force"); err != nil {
			t.Fatalf("uninstall roles:golang: %v", err)
		}
		roles := readFileString(t, filepath.Join(globalDir, "roles.cue"))
		if strings.Contains(roles, "golang/assistant") {
			t.Errorf("golang/assistant should be removed by prefix:\n%s", roles)
		}
		if !strings.Contains(roles, "python/assistant") {
			t.Errorf("python/assistant should remain:\n%s", roles)
		}
	})

	t.Run("qualifier does not substring-match", func(t *testing.T) {
		seedGlobalConfig(t)
		// "assistant" is a substring but not a prefix of the role names.
		if _, _, err := runUninstallCmd(t, "uninstall", "roles:assistant", "--force"); err == nil {
			t.Fatal("expected not-found: category qualifier is prefix-only")
		}
	})

	t.Run("exact whole-name resolves ahead of substring", func(t *testing.T) {
		globalDir := seedGlobalConfig(t)
		if _, _, err := runUninstallCmd(t, "uninstall", "golang/assistant", "--force"); err != nil {
			t.Fatalf("uninstall exact: %v", err)
		}
		roles := readFileString(t, filepath.Join(globalDir, "roles.cue"))
		if strings.Contains(roles, "golang/assistant") {
			t.Errorf("exact name should be removed:\n%s", roles)
		}
	})
}

// TestRemovalParity asserts start uninstall and start config remove produce
// byte-identical results for the same query and scope.
func TestRemovalParity(t *testing.T) {
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		globalDir := seedGlobalConfig(t)
		if _, _, err := runUninstallCmd(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return readFileString(t, filepath.Join(globalDir, "agents.cue"))
	}

	uninstalled := run(t, "uninstall", "gpt", "--force")
	configRemoved := run(t, "config", "remove", "gpt", "--force")

	if uninstalled != configRemoved {
		t.Errorf("uninstall and config remove diverged:\nuninstall:\n%s\nconfig remove:\n%s", uninstalled, configRemoved)
	}
	if strings.Contains(uninstalled, "gpt") || !strings.Contains(uninstalled, "claude") {
		t.Errorf("unexpected result content:\n%s", uninstalled)
	}
}

// TestRemoveResolvedItems_WarnsOnDefaultAgent covers the shared removal loop
// that both start uninstall / config remove (runRemoval) and the no-arg
// interactive config-remove path use, so removing the configured default_agent
// warns and reports identically regardless of entry point.
func TestRemoveResolvedItems_WarnsOnDefaultAgent(t *testing.T) {
	globalDir := seedGlobalConfig(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	items := []configMatch{{Name: "claude", Category: "agent"}}

	errs := removeResolvedItems(nil, stdout, stderr, items, false, false, "claude", false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !strings.Contains(stderr.String(), "default_agent") {
		t.Errorf("expected default_agent warning on stderr, got: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `Removed agent "claude"`) {
		t.Errorf("expected removal report on stdout, got: %q", stdout.String())
	}
	if strings.Contains(readFileString(t, filepath.Join(globalDir, "agents.cue")), "claude") {
		t.Error("claude should be removed")
	}
}

// TestRemoveResolvedItems_NonDefaultNoWarning confirms a non-default agent
// removal stays silent on stderr.
func TestRemoveResolvedItems_NonDefaultNoWarning(t *testing.T) {
	seedGlobalConfig(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	items := []configMatch{{Name: "gpt", Category: "agent"}}

	if errs := removeResolvedItems(nil, stdout, stderr, items, false, false, "claude", false); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if strings.Contains(stderr.String(), "default_agent") {
		t.Errorf("removing a non-default agent must not warn, got: %q", stderr.String())
	}
}

func TestRemoveResolvedItems_FailedItemIsSilent(t *testing.T) {
	seedGlobalConfig(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	items := []configMatch{{Name: "x", Category: "nope"}}

	errs := removeResolvedItems(nil, stdout, stderr, items, false, false, "", false)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	joined := errors.Join(errs...)
	if !IsSilentError(joined) {
		t.Fatal("removal already printed the fault; main.go must not reprint")
	}
	if !strings.Contains(stdout.String(), `Error removing nope "x"`) {
		t.Errorf("want Error removing line, got:\n%s", stdout)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr must stay empty, got: %q", stderr)
	}
}

func TestConfirmConfigRemoval(t *testing.T) {
	items := []configMatch{{Name: "gpt", Category: "agent"}}

	t.Run("declining makes no change", func(t *testing.T) {
		out := &bytes.Buffer{}
		confirmed, err := confirmConfigRemoval(out, strings.NewReader("n\n"), items, false)
		if err != nil {
			t.Fatalf("confirmConfigRemoval: %v", err)
		}
		if confirmed {
			t.Error("expected decline")
		}
		if !strings.Contains(out.String(), "global") {
			t.Errorf("prompt should name the scope: %q", out.String())
		}
		if !strings.Contains(out.String(), "gpt") {
			t.Errorf("prompt should name the module: %q", out.String())
		}
	})

	t.Run("accepting confirms and names local scope", func(t *testing.T) {
		out := &bytes.Buffer{}
		confirmed, err := confirmConfigRemoval(out, strings.NewReader("y\n"), items, true)
		if err != nil {
			t.Fatalf("confirmConfigRemoval: %v", err)
		}
		if !confirmed {
			t.Error("expected confirmation")
		}
		if !strings.Contains(out.String(), "local") {
			t.Errorf("prompt should name the local scope: %q", out.String())
		}
	})
}

func TestConfigRemove_DefaultsToGlobalScope(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	chdir(t, tmpDir)
	localDir := filepath.Join(tmpDir, ".start")
	writeConfigFile(t, localDir, "roles.cue", seedRolesCUE)

	// Without --local, a module present only in local config is not found.
	if _, _, err := runUninstallCmd(t, "config", "remove", "golang/assistant", "--force"); err == nil {
		t.Fatal("expected not-found without --local for a local-only module")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found', got: %v", err)
	}
	if !strings.Contains(readFileString(t, filepath.Join(localDir, "roles.cue")), "golang/assistant") {
		t.Error("local config must be untouched by a default-scope removal")
	}

	// With --local it resolves and removes.
	if _, _, err := runUninstallCmd(t, "config", "remove", "golang/assistant", "--local", "--force"); err != nil {
		t.Fatalf("config remove --local: %v", err)
	}
	if strings.Contains(readFileString(t, filepath.Join(localDir, "roles.cue")), "golang/assistant") {
		t.Error("golang/assistant should be removed from local config")
	}
}
