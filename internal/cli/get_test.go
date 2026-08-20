package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func setupGetTestConfig(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	// Isolate from the user's real config and registry cache.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	// CUE module cache writes read-only files; chmod before TempDir cleanup can unlink.
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	roleFile := filepath.Join(dir, "role.md")
	if err := os.WriteFile(roleFile, []byte("Role file contents.\n"), 0o644); err != nil {
		t.Fatalf("writing role file: %v", err)
	}

	mixedFile := filepath.Join(dir, "mixed.md")
	if err := os.WriteFile(mixedFile, []byte("MIXED FILE CONTENT"), 0o644); err != nil {
		t.Fatalf("writing mixed file: %v", err)
	}

	txtFile := filepath.Join(dir, "role.txt")
	if err := os.WriteFile(txtFile, []byte("Plain text body.\n"), 0o644); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	// Referenced as "~/tilde-role.md" in CUE; lives at $HOME (== dir) so
	// ExpandFilePath's tilde branch resolves to it.
	tildeFile := filepath.Join(dir, "tilde-role.md")
	if err := os.WriteFile(tildeFile, []byte("Tilde file contents."), 0o644); err != nil {
		t.Fatalf("writing tilde file: %v", err)
	}

	// Origin-bearing role: a fake origin is fine — printGetVerbose only echoes
	// it, and @module/ resolution is not triggered for an absolute file path.
	tracedFile := filepath.Join(dir, "traced.md")
	if err := os.WriteFile(tracedFile, []byte("traced contents"), 0o644); err != nil {
		t.Fatalf("writing traced file: %v", err)
	}

	// Referenced as "./relative-role.md" in CUE (dir is the test's cwd after
	// chdir below); exercises ExpandFilePath's filepath.Abs branch.
	relativeFile := filepath.Join(dir, "relative-role.md")
	if err := os.WriteFile(relativeFile, []byte("relative contents"), 0o644); err != nil {
		t.Fatalf("writing relative file: %v", err)
	}

	// File content references {{.command_output}}, paired with a non-empty
	// command, to assert getUTD's trim block suppresses lazy command execution.
	fcCmdRefFile := filepath.Join(dir, "fc-cmd-ref.md")
	if err := os.WriteFile(fcCmdRefFile, []byte("before {{.command_output}} after"), 0o644); err != nil {
		t.Fatalf("writing fc-cmd-ref file: %v", err)
	}

	cueConfig := `
agents: {
	claude: {
		bin:           "claude"
		command:       "{{.bin}} --model {{.model}} '{{.prompt}}'"
		description:   "Claude"
		default_model: "sonnet"
		models: {
			sonnet: "claude-sonnet-4"
			haiku:  "claude-haiku-4"
		}
	}
	bare: {
		bin:         "bare"
		description: "Agent with no command"
	}
}

roles: {
	"role-file": {
		description: "File-source role"
		file:        "` + roleFile + `"
	}
	"role-prompt": {
		description: "Prompt-source role"
		prompt:      "Hello {{.user}}"
	}
	"role-plain": {
		description: "Non-markdown file source"
		file:        "` + txtFile + `"
	}
	"role-mixed": {
		description: "File and prompt; file should win"
		file:        "` + mixedFile + `"
		prompt:      "PROMPT WINS"
	}
	"role-empty": {
		description: "No source fields"
	}
	"role-tilde": {
		description: "Tilde-path file source"
		file:        "~/tilde-role.md"
	}
	"role-relative": {
		description: "Relative-path file source"
		file:        "./relative-role.md"
	}
	"pc-priority": {
		description: "Prompt and command; prompt should win"
		prompt:      "PROMPT VALUE"
		command:     "echo COMMAND VALUE"
	}
	"role-module-no-origin": {
		description: "@module/ file path without origin (error guard)"
		file:        "@module/anywhere.md"
	}
	"role-traced": {
		description: "File source with origin (verbose metadata)"
		file:        "` + tracedFile + `"
		origin:      "github.com/example/start-library/roles/traced@v1.2.3"
	}
	"fc-cmd-ref": {
		description: "File source whose content references {{.command_output}}; command must not run"
		file:        "` + fcCmdRefFile + `"
		command:     "echo SHOULD-NOT-APPEAR"
	}
	"pc-cmd-ref": {
		description: "Prompt referencing {{.command_output}} with command; command must not run"
		prompt:      "before {{.command_output}} after"
		command:     "echo SHOULD-NOT-APPEAR"
	}
}

contexts: {
	"ctx-cmd": {
		description: "Command-source context with custom shell and timeout"
		command:     "printf 'cmd-output'"
		shell:       "bash -c"
		timeout:     5
	}
}

tasks: {
	"task-prompt": {
		description: "Prompt-source task"
		prompt:      "Task body"
	}
	"task-cmd": {
		description: "Command-source task"
		command:     "printf 'task-cmd-output'"
	}
}
`

	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	chdir(t, dir)
	return dir
}

// restoreNoColor captures color.NoColor and restores it after the test. A
// --color=always command flips this process-global to decorate; without
// restoration the false value leaks into later tests that read color.NoColor
// directly (e.g. printVerboseDump snapshots that bypass PersistentPreRunE).
func restoreNoColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	// --color=always is still gated by NO_COLOR in resolveColorMode; clear it
	// so styling tests can run in environments that export NO_COLOR=1.
	t.Setenv("NO_COLOR", "")
	t.Cleanup(func() { color.NoColor = prev })
}

// hasANSI reports whether s contains an ANSI escape sequence.
func hasANSI(s string) bool { return strings.Contains(s, "\x1b[") }

// runGetCmd runs `start get` with the given args and a non-TTY stdin.
func runGetCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(append([]string{"get"}, args...))
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestGetUTDPromptSource(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if !strings.HasPrefix(stdout, "Hello ") {
		t.Errorf("expected stdout to start with rendered prompt, got: %q", stdout)
	}
	if strings.Contains(stdout, "{{.user}}") {
		t.Errorf("template placeholder should be substituted, got: %q", stdout)
	}
}

func TestGetUTDFileSource(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-file")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if stdout != "Role file contents.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Role file contents.\n")
	}
}

func TestGetUTDFileWinsOverPrompt(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-mixed")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "MIXED FILE CONTENT") {
		t.Errorf("expected file content in stdout, got: %q", stdout)
	}
	if strings.Contains(stdout, "PROMPT WINS") {
		t.Errorf("prompt should not appear when file is set, got: %q", stdout)
	}
}

// TestGetUTDCommandSourceWithShellTimeout pins that getUTD's trim block
// preserves Shell and Timeout — they are execution config, not source fields.
func TestGetUTDCommandSourceWithShellTimeout(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "ctx-cmd")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if stdout != "cmd-output\n" {
		t.Errorf("stdout = %q, want %q", stdout, "cmd-output\n")
	}
}

// The styling integration tests force decoration via --color=always so the
// styled branch runs through a non-TTY buffer; they assert the predicate is
// actually wired into getUTD, not merely correct in isolation.

func TestGetStylesPromptUnderForcedColor(t *testing.T) {
	setupGetTestConfig(t)
	restoreNoColor(t)

	stdout, stderr, err := runGetCmd(t, "role-prompt", "--color=always")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if !hasANSI(stdout) {
		t.Errorf("prompt source should be styled under --color=always, got raw: %q", stdout)
	}
	if !strings.Contains(stdout, "Hello") {
		t.Errorf("styled output should still contain the prompt text, got: %q", stdout)
	}
}

func TestGetStylesMarkdownFileUnderForcedColor(t *testing.T) {
	setupGetTestConfig(t)
	restoreNoColor(t)

	stdout, stderr, err := runGetCmd(t, "role-file", "--color=always")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if !hasANSI(stdout) {
		t.Errorf(".md file source should be styled under --color=always, got raw: %q", stdout)
	}
}

func TestGetDoesNotStyleNonMarkdownFile(t *testing.T) {
	setupGetTestConfig(t)
	restoreNoColor(t)

	stdout, stderr, err := runGetCmd(t, "role-plain", "--color=always")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if hasANSI(stdout) {
		t.Errorf(".txt file source must not be styled, got: %q", stdout)
	}
	if stdout != "Plain text body.\n" {
		t.Errorf("non-markdown body must be emitted raw, got: %q", stdout)
	}
}

func TestGetDoesNotStyleCommandSource(t *testing.T) {
	setupGetTestConfig(t)
	restoreNoColor(t)

	stdout, stderr, err := runGetCmd(t, "ctx-cmd", "--color=always")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if hasANSI(stdout) {
		t.Errorf("command source must not be styled, got: %q", stdout)
	}
	if stdout != "cmd-output\n" {
		t.Errorf("command output must be emitted raw, got: %q", stdout)
	}
}

func TestGetDoesNotStyleAgentTemplate(t *testing.T) {
	setupGetTestConfig(t)
	restoreNoColor(t)

	stdout, stderr, err := runGetCmd(t, "claude", "--color=always")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if hasANSI(stdout) {
		t.Errorf("agent command template must not be styled, got: %q", stdout)
	}
}

func TestGetAgent(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "claude --model claude-sonnet-4") {
		t.Errorf("expected resolved bin and model, got: %q", stdout)
	}
	if !strings.Contains(stdout, "{{.prompt}}") {
		t.Errorf("runtime {{.prompt}} placeholder should remain, got: %q", stdout)
	}
}

func TestGetAgentNoCommand(t *testing.T) {
	setupGetTestConfig(t)

	stdout, _, err := runGetCmd(t, "bare")
	if err == nil {
		t.Fatal("expected error for agent with no command field")
	}
	if !strings.Contains(err.Error(), "bare") {
		t.Errorf("error should name the agent, got: %v", err)
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention command field, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error, got: %q", stdout)
	}
}

func TestGetUTDEmptyFields(t *testing.T) {
	setupGetTestConfig(t)

	stdout, _, err := runGetCmd(t, "role-empty")
	if err == nil {
		t.Fatal("expected error for UTD module with no source fields")
	}
	if !strings.Contains(err.Error(), "role-empty") {
		t.Errorf("error should name the module, got: %v", err)
	}
	for _, want := range []string{"file", "prompt", "command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list %q as expected field, got: %v", want, err)
		}
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error, got: %q", stdout)
	}
}

// TestGetNoArgNonTTY verifies no-arg get in a non-interactive environment
// errors rather than blocking on a prompt.
func TestGetNoArgNonTTY(t *testing.T) {
	setupGetTestConfig(t)

	_, _, err := runGetCmd(t)
	if err == nil {
		t.Fatal("expected error for no argument in non-TTY mode")
	}
	if !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("error should mention non-interactive mode, got: %v", err)
	}
}

// TestGetAmbiguousNonTTY verifies an ambiguous name in non-TTY mode errors
// with the candidate matches listed.
func TestGetAmbiguousNonTTY(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	cueConfig := `
roles: {
	helper: {
		prompt: "role helper"
	}
}
tasks: {
	helper: {
		prompt: "task helper"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	_, _, err := runGetCmd(t, "helper")
	if err == nil {
		t.Fatal("expected ambiguity error in non-TTY mode")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity, got: %v", err)
	}
	for _, want := range []string{"roles:helper", "tasks:helper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list candidate %q, got: %v", want, err)
		}
	}
}

// TestGetVerboseCommandSource verifies --verbose on a command-source module
// emits a "Command:" line on stderr so callers can see the shell-out behind stdout.
func TestGetVerboseCommandSource(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--verbose", "get", "ctx-cmd"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stderrStr := stderr.String()
	for _, want := range []string{"Type: Context", "Name: ctx-cmd", "Command: printf 'cmd-output'"} {
		if !strings.Contains(stderrStr, want) {
			t.Errorf("stderr missing %q\ngot: %s", want, stderrStr)
		}
	}
	if stdout.String() != "cmd-output\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "cmd-output\n")
	}
}

func TestGetVerboseToStderr(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--verbose", "get", "role-prompt"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stderrStr := stderr.String()
	for _, want := range []string{"Type: Role", "Name: role-prompt"} {
		if !strings.Contains(stderrStr, want) {
			t.Errorf("stderr missing %q\ngot: %s", want, stderrStr)
		}
	}

	stdoutStr := stdout.String()
	for _, banned := range []string{"Type:", "Name:"} {
		if strings.Contains(stdoutStr, banned) {
			t.Errorf("stdout should not contain %q metadata, got: %q", banned, stdoutStr)
		}
	}
	if !strings.HasPrefix(stdoutStr, "Hello ") {
		t.Errorf("stdout should still contain rendered content, got: %q", stdoutStr)
	}
}

// TestGetQuietSuppressesStderr covers the verbose-path Quiet gate and the
// integration shape: --quiet leaves stdout with only module content and
// stderr empty on the happy path. The autoInstall and widen-notice arms of
// the same contract are covered elsewhere.
func TestGetQuietSuppressesStderr(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--quiet", "get", "role-prompt"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Errorf("--quiet must leave stderr empty on happy path, got: %q", stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "Hello ") {
		t.Errorf("stdout should carry rendered content, got: %q", stdout.String())
	}
}

// TestGetResolveQueryRoutesToStderr asserts get prompts and emits the
// short-query fallback on stderr (not stdout), keeping the pipe clean when
// stdin is a TTY but stdout is piped.
func TestGetResolveQueryRoutesToStderr(t *testing.T) {
	setupGetTestConfig(t)

	t.Run("no-arg non-TTY surfaces error without writing stderr", func(t *testing.T) {
		stderr := new(bytes.Buffer)
		_, err := getResolveQuery(nil, stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("expected error for no-arg non-TTY")
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be untouched on non-TTY error path, got: %q", stderr.String())
		}
	})

	t.Run("short query passes through getResolveQuery to the engine floor", func(t *testing.T) {
		// The floor moved into the resolver (exempting the exact tier), so
		// getResolveQuery no longer rejects a short query itself.
		stderr := new(bytes.Buffer)
		q, err := getResolveQuery([]string{"ab"}, stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("getResolveQuery should pass a short query through, got: %v", err)
		}
		if q != "ab" {
			t.Errorf("getResolveQuery = %q, want %q", q, "ab")
		}
	})

	t.Run("short non-exact query errors at the engine floor", func(t *testing.T) {
		stdout, _, err := runGetCmd(t, "ab")
		if err == nil {
			t.Fatal("expected error for short non-exact query")
		}
		if !strings.Contains(err.Error(), "3 characters") {
			t.Errorf("error should mention minimum length, got: %v", err)
		}
		if stdout != "" {
			t.Errorf("stdout should be empty on error, got: %q", stdout)
		}
	})
}

// TestGetCommandHelp verifies `start get help` prints the command help and that
// get registers --global plus inherits --local. Flag presence is asserted via
// direct lookup so cosmetic help-formatter changes cannot false-fail it.
func TestGetCommandHelp(t *testing.T) {
	stdout, _, err := runGetCmd(t, "help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"get", "stdout", "start describe", "Auto-installed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output missing %q\ngot: %s", want, stdout)
		}
	}

	root := NewRootCmd()
	var getCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "get" {
			getCmd = c
			break
		}
	}
	if getCmd == nil {
		t.Fatal("get command not registered on root")
	}
	if getCmd.Flag("global") == nil {
		t.Error("get command missing --global flag")
	}
	if getCmd.Flag("local") == nil {
		t.Error("get command missing inherited --local flag (expected via root persistent flags)")
	}
}

// TestGetAppearsInRootHelp asserts get registers with GroupID "workflow"
// (structural check avoids fragility against Cobra help-formatter changes).
func TestGetAppearsInRootHelp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	cmd := NewRootCmd()
	for _, c := range cmd.Commands() {
		if c.Name() == "get" {
			if c.GroupID != "workflow" {
				t.Errorf("get.GroupID = %q, want %q", c.GroupID, "workflow")
			}
			return
		}
	}
	t.Fatal("get command not registered on root")
}

// TestGetFilePathReadsDirectly verifies a filesystem path bypasses resolution
// and outputs the file's content directly.
func TestGetFilePathReadsDirectly(t *testing.T) {
	dir := setupGetTestConfig(t)

	docPath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(docPath, []byte("Direct file body.\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	stdout, stderr, err := runGetCmd(t, docPath)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Direct file body.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Direct file body.\n")
	}
}

// TestGetRemoteLocatorReadsDirectly verifies get fetches an http(s) locator and
// writes its body to stdout pipe-clean, the remote analogue of a direct file
// read.
func TestGetRemoteLocatorReadsDirectly(t *testing.T) {
	setupGetTestConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "Remote get body.\n")
	}))
	defer srv.Close()

	stdout, stderr, err := runGetCmd(t, srv.URL+"/note.md")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Remote get body.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Remote get body.\n")
	}
}

// TestGetUnknownName verifies a name with no installed or registry match
// surfaces a clear error and leaves stdout empty.
func TestGetUnknownName(t *testing.T) {
	setupGetTestConfig(t)

	stdout, _, err := runGetCmd(t, "definitely-not-a-real-module-zzz")
	if err == nil {
		t.Fatal("expected error for unknown module name")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-module-zzz") {
		t.Errorf("error should name the missing module, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error, got: %q", stdout)
	}
}

// TestGetUTDTildePath verifies a ~/-prefixed file field resolves through tilde
// expansion and outputs the file's contents.
func TestGetUTDTildePath(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-tilde")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Tilde file contents.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Tilde file contents.\n")
	}
}

func TestGetVerboseFileAndOrigin(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--verbose", "get", "role-traced"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stderrStr := stderr.String()
	wants := []string{
		"Type: Role",
		"Name: role-traced",
		"Origin: github.com/example/start-library/roles/traced@v1.2.3",
		"Path: ", // Path: <absolute> — only assert the prefix; absolute path varies by tempdir
	}
	for _, want := range wants {
		if !strings.Contains(stderrStr, want) {
			t.Errorf("stderr missing %q\ngot: %s", want, stderrStr)
		}
	}
	// Path line should reference the actual file basename so we know it's the
	// resolved path, not the literal config string.
	if !strings.Contains(stderrStr, "traced.md") {
		t.Errorf("stderr Path line missing resolved file name 'traced.md'\ngot: %s", stderrStr)
	}

	if stdout.String() != "traced contents\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "traced contents\n")
	}
}

// TestGetShortQueryNonTTYEndToEnd is the cobra-level counterpart to the
// getResolveQuery unit test: a short query in a non-TTY environment must error
// and never write to stdout. The TTY-mode re-prompt is not covered (no
// pseudo-TTY helpers).
func TestGetShortQueryNonTTYEndToEnd(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "ab")
	if err == nil {
		t.Fatal("expected error for short query in non-TTY")
	}
	if !strings.Contains(err.Error(), "3 characters") {
		t.Errorf("error should mention minimum length, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout must be empty on short-query failure, got: %q", stdout)
	}
	// The TTY-mode re-prompt notice is only meaningful when re-prompting, so it
	// must not clutter scripted (non-TTY) callers' stderr.
	if strings.Contains(stderr, "Query must be at least 3 characters") {
		t.Errorf("non-TTY path should not emit TTY re-prompt notice, stderr: %q", stderr)
	}
}

// TestGetUTDFileSourceSuppressesCommand pins the no-shell-out invariant of
// getUTD's trim block: with file and command both set and the file
// referencing {{.command_output}}, the command must not execute. The
// companion TestGetUTDFileWinsOverPrompt covers file-wins but not this.
func TestGetUTDFileSourceSuppressesCommand(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "fc-cmd-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if strings.Contains(stdout, "SHOULD-NOT-APPEAR") {
		t.Errorf("command output leaked into file-source render — getUTD trim block did not suppress command execution\nstdout: %q\nstderr: %s", stdout, stderr)
	}
	for _, marker := range []string{"before", "after"} {
		if !strings.Contains(stdout, marker) {
			t.Errorf("file content marker %q missing from stdout: %q", marker, stdout)
		}
	}
}

// TestGetUTDPromptSourceSuppressesCommand is the prompt-branch counterpart to
// TestGetUTDFileSourceSuppressesCommand: with prompt set and referencing
// {{.command_output}} alongside a command, the command must not execute.
func TestGetUTDPromptSourceSuppressesCommand(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "pc-cmd-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	if strings.Contains(stdout, "SHOULD-NOT-APPEAR") {
		t.Errorf("command output leaked into prompt-source render — getUTD trim block did not suppress command execution\nstdout: %q\nstderr: %s", stdout, stderr)
	}
	for _, marker := range []string{"before", "after"} {
		if !strings.Contains(stdout, marker) {
			t.Errorf("prompt content marker %q missing from stdout: %q", marker, stdout)
		}
	}
}

// TestGetUTDPromptWinsOverCommand covers getUTD's else-if branch: with file
// empty and both prompt and command set, the prompt must win.
func TestGetUTDPromptWinsOverCommand(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "pc-priority")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "PROMPT VALUE") {
		t.Errorf("stdout should contain prompt content, got: %q", stdout)
	}
	if strings.Contains(stdout, "COMMAND VALUE") {
		t.Errorf("command output must not appear when prompt is set, got: %q", stdout)
	}
}

// TestGetUTDRelativePath verifies a relative file field resolves through
// ExpandFilePath's filepath.Abs branch and outputs the file's contents.
func TestGetUTDRelativePath(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-relative")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "relative contents\n" {
		t.Errorf("stdout = %q, want %q", stdout, "relative contents\n")
	}
}

// TestGetUTDModuleNoOrigin verifies the error guard for an @module/ file path
// with no origin field: descriptive error, empty stdout.
func TestGetUTDModuleNoOrigin(t *testing.T) {
	setupGetTestConfig(t)

	stdout, _, err := runGetCmd(t, "role-module-no-origin")
	if err == nil {
		t.Fatal("expected error for @module/ path without origin")
	}
	if !strings.Contains(err.Error(), "role-module-no-origin") {
		t.Errorf("error should name the module, got: %v", err)
	}
	if !strings.Contains(err.Error(), "@module/") {
		t.Errorf("error should mention @module/, got: %v", err)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error should mention origin field, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error, got: %q", stdout)
	}
}

// TestGetTooManyArgs verifies more than one positional arg is rejected at the
// cobra Args layer (before RunE), so stdout is never touched.
func TestGetTooManyArgs(t *testing.T) {
	setupGetTestConfig(t)

	stdout, _, err := runGetCmd(t, "role-prompt", "extra-arg")
	if err == nil {
		t.Fatal("expected error for two positional arguments")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when args validation fails, got: %q", stdout)
	}
}

// TestGetUTDModulePath verifies @module/ resolution end-to-end: the file is
// looked up in $CUE_CACHE_DIR/mod/extract/<dir(modulePath)>/<base(modulePath)+version>/
// (see ResolveModulePath). The test fabricates that layout and reads through
// getUTD's @module/ branch.
func TestGetUTDModulePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	cacheDir := filepath.Join(dir, "cue-cache")
	t.Setenv("CUE_CACHE_DIR", cacheDir)

	// CUE module cache writes read-only files; chmod before TempDir cleanup.
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})

	// Origin layout: <dir(modulePath)>/<base(modulePath)+version>
	// → github.com/example/test-mod / sub@v1.0.0
	origin := "github.com/example/test-mod/sub@v1.0.0"
	versionedDir := filepath.Join(cacheDir, "mod", "extract",
		"github.com", "example", "test-mod", "sub@v1.0.0")
	if err := os.MkdirAll(versionedDir, 0o755); err != nil {
		t.Fatalf("creating versioned cache dir: %v", err)
	}
	moduleFile := filepath.Join(versionedDir, "module-content.md")
	if err := os.WriteFile(moduleFile, []byte("module file content"), 0o644); err != nil {
		t.Fatalf("writing module file: %v", err)
	}

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	cueConfig := `
roles: {
	"role-module": {
		description: "Module-prefixed file"
		file:        "@module/module-content.md"
		origin:      "` + origin + `"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, stderr, err := runGetCmd(t, "role-module")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "module file content\n" {
		t.Errorf("stdout = %q, want %q", stdout, "module file content\n")
	}
}

// TestGetMergedScopeFindsGlobalAndLocal pins the scope constant in runGet's
// loadConfig from both directions: a global-only and a local-only role must
// each be reachable, so flipping runGet to ScopeLocal or ScopeGlobal breaks
// one sub-test.
func TestGetMergedScopeFindsGlobalAndLocal(t *testing.T) {
	dir := t.TempDir()

	globalStartDir := filepath.Join(dir, ".config", "start")
	if err := os.MkdirAll(globalStartDir, 0o755); err != nil {
		t.Fatalf("creating global config dir: %v", err)
	}
	globalCueConfig := `
roles: {
	"global-only-role": {
		description: "Lives only in global config"
		prompt:      "from global"
	}
}
`
	if err := os.WriteFile(filepath.Join(globalStartDir, "settings.cue"), []byte(globalCueConfig), 0o644); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	localStartDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(localStartDir, 0o755); err != nil {
		t.Fatalf("creating local config dir: %v", err)
	}
	localCueConfig := `
roles: {
	"local-only-role": {
		description: "Lives only in local config"
		prompt:      "from local"
	}
}
`
	if err := os.WriteFile(filepath.Join(localStartDir, "settings.cue"), []byte(localCueConfig), 0o644); err != nil {
		t.Fatalf("writing local config: %v", err)
	}

	// globalConfigDir prefers XDG_CONFIG_HOME over $HOME/.config. Force the
	// $HOME/.config branch so this test is deterministic regardless of the
	// caller's environment.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	chdir(t, dir)

	t.Run("global-only role visible via merged scope", func(t *testing.T) {
		stdout, stderr, err := runGetCmd(t, "global-only-role")
		if err != nil {
			t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
		}
		if stdout != "from global\n" {
			t.Errorf("stdout = %q, want %q", stdout, "from global\n")
		}
	})

	t.Run("local-only role visible via merged scope", func(t *testing.T) {
		stdout, stderr, err := runGetCmd(t, "local-only-role")
		if err != nil {
			t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
		}
		if stdout != "from local\n" {
			t.Errorf("stdout = %q, want %q", stdout, "from local\n")
		}
	})
}

// TestGetAgentModelOverrideExact verifies --model with a key in the agent's
// models map produces the resolved id, not default_model — keeping `get` and
// `start` agreed on the substituted model.
func TestGetAgentModelOverrideExact(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--model", "haiku", "get", "claude"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stdoutStr := stdout.String()
	if !strings.Contains(stdoutStr, "claude-haiku-4") {
		t.Errorf("expected --model haiku to resolve to %q, got: %q", "claude-haiku-4", stdoutStr)
	}
	if strings.Contains(stdoutStr, "claude-sonnet-4") {
		t.Errorf("default_model should not appear when --model is set, got: %q", stdoutStr)
	}
}

// TestGetAgentModelOverrideSubstring verifies the substring path of
// resolveModelName: --model "hai" matches the only key containing it, "haiku".
func TestGetAgentModelOverrideSubstring(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--model", "hai", "get", "claude"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stdoutStr := stdout.String()
	if !strings.Contains(stdoutStr, "claude-haiku-4") {
		t.Errorf("expected --model 'hai' to resolve to haiku id %q, got: %q", "claude-haiku-4", stdoutStr)
	}
}

// TestGetAgentModelOverridePassthrough verifies a --model value absent from
// the models map is substituted verbatim, so users can pass unregistered ids.
func TestGetAgentModelOverridePassthrough(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--model", "claude-opus-4-7", "get", "claude"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stdoutStr := stdout.String()
	if !strings.Contains(stdoutStr, "claude-opus-4-7") {
		t.Errorf("expected literal --model value to pass through, got: %q", stdoutStr)
	}
}

// TestGetVerboseTildePathExpanded verifies --verbose reports the resolved
// absolute path for a tilde-prefixed file source, not the literal "~/..." string.
func TestGetVerboseTildePathExpanded(t *testing.T) {
	dir := setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--verbose", "get", "role-tilde"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	expectedPath := filepath.Join(dir, "tilde-role.md")
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Path: "+expectedPath) {
		t.Errorf("stderr should report expanded path %q, got: %s", expectedPath, stderrStr)
	}
	if strings.Contains(stderrStr, "Path: ~/tilde-role.md") {
		t.Errorf("stderr should not contain the unexpanded literal path, got: %s", stderrStr)
	}
}

// setupGetDualScopeConfig writes global and local configs that both define a
// "shared-role" with distinct content (so --local vs --global can be told
// apart) plus a scope-only role each (to exercise the other scope's not-found path).
func setupGetDualScopeConfig(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	// Not-found lookups fall through to the registry, which writes read-only
	// files into HOME/.cache/cue. Re-chmod before TempDir cleanup so the
	// teardown unlink calls can succeed.
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})

	globalStartDir := filepath.Join(dir, ".config", "start")
	if err := os.MkdirAll(globalStartDir, 0o755); err != nil {
		t.Fatalf("creating global config dir: %v", err)
	}
	globalCueConfig := `
roles: {
	"global-only-role": {
		description: "Lives only in global config"
		prompt:      "from global"
	}
	"shared-role": {
		description: "Defined in both scopes"
		prompt:      "shared from global"
	}
}
`
	if err := os.WriteFile(filepath.Join(globalStartDir, "settings.cue"), []byte(globalCueConfig), 0o644); err != nil {
		t.Fatalf("writing global config: %v", err)
	}

	localStartDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(localStartDir, 0o755); err != nil {
		t.Fatalf("creating local config dir: %v", err)
	}
	localCueConfig := `
roles: {
	"local-only-role": {
		description: "Lives only in local config"
		prompt:      "from local"
	}
	"shared-role": {
		description: "Defined in both scopes"
		prompt:      "shared from local"
	}
}
`
	if err := os.WriteFile(filepath.Join(localStartDir, "settings.cue"), []byte(localCueConfig), 0o644); err != nil {
		t.Fatalf("writing local config: %v", err)
	}

	// globalConfigDir prefers XDG_CONFIG_HOME over $HOME/.config. Force the
	// $HOME/.config branch so this test is deterministic regardless of the
	// caller's environment.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	chdir(t, dir)
	return dir
}

// TestGetLocalScope verifies --local restricts resolution to local config.
// The only discriminating assertion is the not-found path on a global-only
// module: a shared-role check would pass even if --local were ignored, since
// merged scope makes local override global on conflict.
func TestGetLocalScope(t *testing.T) {
	setupGetDualScopeConfig(t)

	stdout, _, err := runGetCmd(t, "--local", "global-only-role")
	if err == nil {
		t.Fatal("expected not-found error for global-only role under --local")
	}
	if !strings.Contains(err.Error(), "global-only-role") {
		t.Errorf("error should name the missing module, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on not-found error, got: %q", stdout)
	}
}

func TestGetGlobalScope(t *testing.T) {
	setupGetDualScopeConfig(t)

	t.Run("shared role resolves to global definition", func(t *testing.T) {
		stdout, stderr, err := runGetCmd(t, "--global", "shared-role")
		if err != nil {
			t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
		}
		if stdout != "shared from global\n" {
			t.Errorf("stdout = %q, want %q", stdout, "shared from global\n")
		}
	})

	t.Run("local-only role not found under --global", func(t *testing.T) {
		stdout, _, err := runGetCmd(t, "--global", "local-only-role")
		if err == nil {
			t.Fatal("expected not-found error for local-only role under --global")
		}
		if !strings.Contains(err.Error(), "local-only-role") {
			t.Errorf("error should name the missing module, got: %v", err)
		}
		if stdout != "" {
			t.Errorf("stdout should be empty on not-found error, got: %q", stdout)
		}
	})
}

// TestGetLocalAndGlobalMutuallyExclusive verifies both --local and --global is
// a usage error (exit 2). The check is explicit (validateScopeFlags) rather
// than Cobra's MarkFlagsMutuallyExclusive, whose untyped error maps to exit 1.
func TestGetLocalAndGlobalMutuallyExclusive(t *testing.T) {
	stdout, _, err := runGetCmd(t, "--local", "--global", "any-name")
	if err == nil {
		t.Fatal("expected mutual-exclusion error when both --local and --global are set")
	}
	if ExitCodeFromError(err) != ExitUsage {
		t.Errorf("mutual-exclusion should be a usage error (exit 2), got %d: %v", ExitCodeFromError(err), err)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion, got: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on mutual-exclusion error, got: %q", stdout)
	}
}

func TestEnsureTrailingNewline(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"already newline-terminated", "abc\n", "abc\n"},
		{"missing newline gets one", "abc", "abc\n"},
		{"multi-line with newline preserved", "a\nb\nc\n", "a\nb\nc\n"},
		{"multi-line without newline gets one", "a\nb\nc", "a\nb\nc\n"},
		{"trailing whitespace untouched", "abc  ", "abc  \n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureTrailingNewline(tc.in)
			if got != tc.want {
				t.Errorf("ensureTrailingNewline(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGetAgentVerboseMetadata covers getAgent's --verbose branch: an agent has
// no file or command field, so stderr carries Type and Name only.
func TestGetAgentVerboseMetadata(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--verbose", "get", "claude"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}

	stderrStr := stderr.String()
	for _, want := range []string{"Type: Agent", "Name: claude"} {
		if !strings.Contains(stderrStr, want) {
			t.Errorf("stderr missing %q\ngot: %s", want, stderrStr)
		}
	}
	for _, banned := range []string{"Path:", "Command:"} {
		if strings.Contains(stderrStr, banned) {
			t.Errorf("stderr should not contain %q for an agent (no UTD source fields), got: %s", banned, stderrStr)
		}
	}

	stdoutStr := stdout.String()
	for _, banned := range []string{"Type:", "Name:"} {
		if strings.Contains(stdoutStr, banned) {
			t.Errorf("stdout should not contain %q metadata, got: %q", banned, stdoutStr)
		}
	}
	if !strings.Contains(stdoutStr, "claude --model claude-sonnet-4") {
		t.Errorf("stdout should still contain the rendered agent command, got: %q", stdoutStr)
	}
}

// TestGetAgentRuntimePlaceholdersIntact pins that only {{.bin}} and {{.model}}
// are resolved at get time; {{.prompt}}, {{.role}}, {{.role_file}}, and
// {{.datetime}} stay intact for the execution path to fill.
func TestGetAgentRuntimePlaceholdersIntact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	cueConfig := `
agents: {
	"runtime-rich": {
		bin:           "rt"
		command:       "{{.bin}} -m {{.model}} -r {{.role}} -rf {{.role_file}} -d {{.datetime}} -- {{.prompt}}"
		description:   "Agent exercising every runtime placeholder"
		default_model: "fast"
		models: {
			fast: "rt-fast-1"
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, stderr, err := runGetCmd(t, "runtime-rich")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}

	for _, want := range []string{"rt -m rt-fast-1", "{{.role}}", "{{.role_file}}", "{{.datetime}}", "{{.prompt}}"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\ngot: %q", want, stdout)
		}
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("stdout should end with a single newline, got: %q", stdout)
	}
}

// TestGetStdoutContentOnly verifies default mode emits only rendered content
// to stdout with stderr empty. Asserted independently of --quiet so a dropped
// verbose gate cannot pass merely because --quiet still suppresses it.
func TestGetStdoutContentOnly(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "role-file")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Role file contents.\n" {
		t.Errorf("stdout = %q, want exactly %q (no metadata, no extra newlines)", stdout, "Role file contents.\n")
	}
	if stderr != "" {
		t.Errorf("stderr should be empty on default-mode happy path, got: %q", stderr)
	}
}

// TestGetAllSourceFieldsFileWins covers the three-field case: file, prompt, and
// command all set must emit only the file content (trim clears both others).
func TestGetAllSourceFieldsFileWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	roleFile := filepath.Join(dir, "all-three.md")
	if err := os.WriteFile(roleFile, []byte("FILE-WINS"), 0o644); err != nil {
		t.Fatalf("writing role file: %v", err)
	}

	cueConfig := `
roles: {
	"all-three": {
		description: "File, prompt, and command — file must win"
		file:        "` + roleFile + `"
		prompt:      "PROMPT-LOSES"
		command:     "echo COMMAND-LOSES"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, stderr, err := runGetCmd(t, "all-three")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "FILE-WINS") {
		t.Errorf("stdout should contain file content, got: %q", stdout)
	}
	for _, banned := range []string{"PROMPT-LOSES", "COMMAND-LOSES"} {
		if strings.Contains(stdout, banned) {
			t.Errorf("stdout must not contain %q when file is set, got: %q", banned, stdout)
		}
	}
}

// TestGetUTDFileMissingOnDisk covers getUTD's process error path: a file
// source pointing at a missing file errors and leaves stdout empty.
func TestGetUTDFileMissingOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	missing := filepath.Join(dir, "does-not-exist.md")
	cueConfig := `
roles: {
	"role-missing-file": {
		description: "File path that does not exist on disk"
		file:        "` + missing + `"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, _, err := runGetCmd(t, "role-missing-file")
	if err == nil {
		t.Fatal("expected error when file source path does not exist")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on file-read error, got: %q", stdout)
	}
}

// TestGetUTDTaskPromptSource verifies a prompt-source task resolves through
// get, guarding against get silently dropping the task category.
func TestGetUTDTaskPromptSource(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "task-prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Task body\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Task body\n")
	}
}

// TestGetUTDTaskCommandSource verifies a command-source task executes and its
// output flows through ensureTrailingNewline.
func TestGetUTDTaskCommandSource(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "task-cmd")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "task-cmd-output\n" {
		t.Errorf("stdout = %q, want %q", stdout, "task-cmd-output\n")
	}
}

// TestGetUTDFileMultilineNoExtraNewline verifies a file already ending in a
// newline is emitted verbatim — no double-newline that would shift `wc -l`.
func TestGetUTDFileMultilineNoExtraNewline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	multi := filepath.Join(dir, "multi.md")
	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(multi, []byte(content), 0o644); err != nil {
		t.Fatalf("writing multi file: %v", err)
	}

	cueConfig := `
roles: {
	"role-multi": {
		description: "Multi-line file with trailing newline"
		file:        "` + multi + `"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, stderr, err := runGetCmd(t, "role-multi")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != content {
		t.Errorf("stdout = %q, want %q (no extra newline)", stdout, content)
	}
}

// TestGetUTDPromptAddsTrailingNewline verifies a prompt with no trailing
// newline gets exactly one appended through the full get pipeline.
func TestGetUTDPromptAddsTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	startDir := filepath.Join(dir, ".start")
	if err := os.MkdirAll(startDir, 0o755); err != nil {
		t.Fatalf("creating .start dir: %v", err)
	}

	cueConfig := `
roles: {
	"no-newline": {
		description: "Prompt without trailing newline"
		prompt:      "no-newline-here"
	}
}
`
	if err := os.WriteFile(filepath.Join(startDir, "settings.cue"), []byte(cueConfig), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, dir)

	stdout, stderr, err := runGetCmd(t, "no-newline")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "no-newline-here\n" {
		t.Errorf("stdout = %q, want %q (exactly one trailing newline)", stdout, "no-newline-here\n")
	}
}

// TestGetCrossCategoryFindsContext verifies a context module is reachable via
// the cross-category resolver, guarding against contexts being dropped from
// describeCategories.
func TestGetCrossCategoryFindsContext(t *testing.T) {
	setupGetTestConfig(t)

	stdout, stderr, err := runGetCmd(t, "ctx-cmd")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "cmd-output\n" {
		t.Errorf("stdout = %q, want %q", stdout, "cmd-output\n")
	}
}

// TestGetAgentExplicitlyEmptyModelFlag verifies an empty --model does not
// trigger resolveModelName (getAgent gates on flags.Model != ""), so
// default_model still wins.
func TestGetAgentExplicitlyEmptyModelFlag(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--model", "", "get", "claude"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude-sonnet-4") {
		t.Errorf("empty --model should fall back to default_model, got: %q", stdout.String())
	}
}

// TestGetDebugFlagDoesNotPolluteStdout verifies --debug diagnostics never
// write to stdout, keeping it pipe-clean regardless of verbosity.
func TestGetDebugFlagDoesNotPolluteStdout(t *testing.T) {
	setupGetTestConfig(t)

	cmd := NewRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--debug", "get", "role-file"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr.String())
	}
	if stdout.String() != "Role file contents.\n" {
		t.Errorf("stdout should contain only the file content under --debug, got: %q", stdout.String())
	}
}
