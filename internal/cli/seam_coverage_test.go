package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/registry"
)

// errFetchBoom is the canned fetch failure that drives update's per-module
// error path offline.
var errFetchBoom = errors.New("registry stub: boom")

// Exercises the command paths the registry-client provider seam makes reachable
// offline but that the --json drift guard (json_capture_test.go) does not cover:
// text-mode rendering, category filtering, --export, the update apply/upgrade
// write path, doctor's schema-validation success branch, and the install flow.

// captureText runs a text-mode command through the shared captureStreams
// scaffold, returning stdout plus the Execute error and discarding stderr.
func captureText(t *testing.T, stub *registryStub, args ...string) (string, error) {
	t.Helper()
	stdout, _, err := captureStreams(t, stub, args...)
	return stdout, err
}

// buildModuleFixture writes a self-contained CUE module to a temp dir and
// returns its path, suitable as a stub Fetch SourceDir. It has no external deps
// so it builds with a nil registry.
func buildModuleFixture(t *testing.T, pkg, body string) string {
	t.Helper()
	dir := t.TempDir()
	modDir := filepath.Join(dir, "cue.mod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatalf("creating cue.mod dir: %v", err)
	}
	moduleCue := "module: \"test.example/module@v0\"\nlanguage: version: \"v0.15.1\"\n"
	if err := os.WriteFile(filepath.Join(modDir, "module.cue"), []byte(moduleCue), 0o644); err != nil {
		t.Fatalf("writing module.cue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkg+".cue"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s.cue: %v", pkg, err)
	}
	return dir
}

// agentFixtureBody is the agent module the stub serves for the sentinel agent's
// Fetch, so update/install can extract real content offline.
const agentFixtureBody = `package agent

agent: {
	description: "Sentinel agent fetched from the stub"
	bin:     "sentinel"
	command: "{{.bin}} run"
}
`

// schemaFixtureBody is a permissive schemas module the stub serves so doctor's
// schema-validation success branch runs offline against the isolated config.
const schemaFixtureBody = `package schemas

#Agent: {...}
#Role: {...}
#Context: {...}
#Task: {...}
#Settings: {...}
#SkillInstall: {...}
`

// sentinelModuleBase is the sentinel agent's module path with major version.
// Lookups strip the version suffix, so a SetFetch keyed here also matches the
// resolved @v1.0.0 path.
const sentinelModuleBase = "github.com/p3bot/library/agents/" + sentinelAgentName + "@v1"

// writeInstalledAgentAt writes the sentinel agent into the named local config
// file at the given origin version, so collectInstalledModules picks it up and
// the rewrite target resolves to that file.
func writeInstalledAgentAt(t *testing.T, tmpDir, fileName, version string) string {
	t.Helper()
	content := `agents: {
	"` + sentinelAgentName + `": {
		bin:     "sentinel"
		command: "{{.bin}} run"
		origin:  "github.com/p3bot/library/agents/` + sentinelAgentName + `@` + version + `"
		version: "` + version + `"
	}
}
`
	path := filepath.Join(tmpDir, ".start", fileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing installed agent fixture: %v", err)
	}
	return path
}

// findUpdateResult returns the decoded update result object whose module name
// matches, failing the test if absent.
func findUpdateResult(t *testing.T, results []any, name string) map[string]any {
	t.Helper()
	for _, r := range results {
		obj, ok := r.(map[string]any)
		if !ok {
			continue
		}
		mod, ok := obj["module"].(map[string]any)
		if ok && mod["name"] == name {
			return obj
		}
	}
	t.Fatalf("no update result for module %q in %v", name, results)
	return nil
}

// TestLibraryTextOffline asserts library renders its grouped text output
// offline from the index fixture the stub serves.
func TestLibraryTextOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "library")
	if err != nil {
		t.Fatalf("library: %v\noutput: %s", err, out)
	}
	if stub.providerCalls != 1 {
		t.Errorf("library should consult the provider once, got %d", stub.providerCalls)
	}
	if !strings.Contains(out, sentinelAgentName) {
		t.Errorf("library text output missing sentinel %q\noutput: %s", sentinelAgentName, out)
	}
	for _, want := range []string{"go-expert", "environment", "review/pre-commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("library text output missing %q\noutput: %s", want, out)
		}
	}
}

// TestLibraryCategoryFilterOffline asserts library agents --json restricts the
// output to the agents category, dropping entries from other categories.
func TestLibraryCategoryFilterOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	decoded, raw := captureJSON(t, stub, "library", "agents", "--json")
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("library agents --json should decode to an object, got %T", decoded)
	}
	if _, ok := obj["agents"].(map[string]any); !ok {
		t.Errorf("library agents --json missing agents object; got keys %v", mapKeys(obj))
	}
	// Non-agent entries must not leak through the category filter.
	for _, gone := range []string{"go-expert", "environment", "review/pre-commit"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("library agents --json leaked non-agent entry %q\noutput: %s", gone, raw)
		}
	}
}

// TestLibraryExportOffline asserts library --export dumps the raw index CUE
// from the fixture source dir offline.
func TestLibraryExportOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "library", "--export")
	if err != nil {
		t.Fatalf("library --export: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "package index") {
		t.Errorf("library --export missing CUE package header\noutput: %s", out)
	}
	if !strings.Contains(out, sentinelAgentName) {
		t.Errorf("library --export missing sentinel %q\noutput: %s", sentinelAgentName, out)
	}
}

// TestLibraryExportRejectsCategory asserts the documented guard that --export
// cannot be combined with a category filter still holds offline.
func TestLibraryExportRejectsCategory(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "library", "agents", "--export")
	if err == nil {
		t.Fatalf("library agents --export should error; output: %s", out)
	}
	if !strings.Contains(err.Error(), "category") {
		t.Errorf("expected category-conflict error, got: %v", err)
	}
}

// TestLibraryVerboseOffline asserts the --verbose rendering path (which marks
// installed modules) runs offline against the stub.
func TestLibraryVerboseOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "library", "--verbose")
	if err != nil {
		t.Fatalf("library --verbose: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, sentinelAgentName) {
		t.Errorf("library --verbose missing sentinel %q\noutput: %s", sentinelAgentName, out)
	}
}

// TestLibraryUnknownCategory asserts the pre-network category validation
// rejects an unknown category argument.
func TestLibraryUnknownCategory(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "library", "bogus")
	if err == nil {
		t.Fatalf("library bogus should error; output: %s", out)
	}
	if !strings.Contains(err.Error(), "unknown category") {
		t.Errorf("expected unknown-category error, got: %v", err)
	}
}

// TestSearchTextOffline asserts search renders its text-mode results offline,
// including the registry-only sentinel served by the stub index.
func TestSearchTextOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	out, err := captureText(t, stub, "search", "sentinel")
	if err != nil {
		t.Fatalf("search: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, sentinelAgentName) {
		t.Errorf("search text output missing sentinel %q\noutput: %s", sentinelAgentName, out)
	}
}

// captureStreams mirrors captureText but keeps stdout and stderr separate, so
// tests can assert which stream carries the result line versus the outage warning.
func captureStreams(t *testing.T, stub *registryStub, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	cmd := NewRootCmd()
	cmd.SetContext(WithProvider(cmd.Context(), func() (registry.Client, error) {
		stub.providerCalls++
		return stub, nil
	}))

	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)

	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

// TestSearchTextResultsOffline covers local matches + registry down in text
// mode: the matches print and the command succeeds (exit 0), but the user is
// warned the registry was unavailable so the result is known to be incomplete.
func TestSearchTextResultsOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetchIndexError(transientFetchErr())

	stdout, stderr, err := captureStreams(t, stub, "search", "assistant")
	if err != nil {
		t.Fatalf("local results should keep the command successful: %v", err)
	}
	if !strings.Contains(stdout, "assistant") {
		t.Errorf("stdout should list the local match; got %q", stdout)
	}
	if !strings.Contains(stderr, "registry unavailable") {
		t.Errorf("stderr should warn the registry was down; got %q", stderr)
	}
}

// TestSearchTextEmptyOffline covers nothing-matched-anywhere + registry down in
// text mode: the human gets a friendly no-matches line on stdout and the outage
// warning on stderr, while the command still fails with the transient exit code
// — silenced so main.go adds no duplicate Error: line.
func TestSearchTextEmptyOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetchIndexError(transientFetchErr())

	stdout, stderr, err := captureStreams(t, stub, "search", "zzznomatchanywhere")
	if err == nil {
		t.Fatal("expected a non-nil error so an agent retries instead of trusting an empty result")
	}
	if !IsSilentError(err) {
		t.Errorf("error should be silenced to avoid a duplicate Error: line; got %v", err)
	}
	if got := ExitCodeFromError(err); got != ExitTransient {
		t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
	}
	if !strings.Contains(stdout, "No matches found") {
		t.Errorf("stdout should carry the friendly no-matches line; got %q", stdout)
	}
	if !strings.Contains(stderr, "registry unavailable") {
		t.Errorf("stderr should warn about the outage; got %q", stderr)
	}
}

// TestSearchJSONEmptyOffline covers nothing-matched-anywhere + registry down
// under --json: stdout must stay empty (the failure half of the JSON contract)
// and the command fails with the transient code.
func TestSearchJSONEmptyOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetchIndexError(transientFetchErr())

	stdout, _, err := captureStreams(t, stub, "search", "zzznomatchanywhere", "--json")
	if err == nil {
		t.Fatal("expected a non-nil error under --json")
	}
	if got := ExitCodeFromError(err); got != ExitTransient {
		t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("--json failure must leave stdout empty; got %q", stdout)
	}
}

// TestSearchJSONResultsOffline covers local matches + registry down under
// --json: the command succeeds (exit 0) and writes the results to stdout, but
// the outage is surfaced on stderr so the consumer can tell the set is
// incomplete. Without the stderr warning a partial result would be
// indistinguishable from a complete one.
func TestSearchJSONResultsOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetchIndexError(transientFetchErr())

	stdout, stderr, err := captureStreams(t, stub, "search", "assistant", "--json")
	if err != nil {
		t.Fatalf("local results should keep the command successful: %v", err)
	}
	if !strings.Contains(stdout, "assistant") {
		t.Errorf("stdout should carry the local match JSON; got %q", stdout)
	}
	if !strings.Contains(stderr, "registry unavailable") {
		t.Errorf("stderr should warn the registry was down so the set is known incomplete; got %q", stderr)
	}
}

// TestUpdateAppliesUpgradeOffline drives the full update apply path offline: an
// installed agent at v0.0.1 is upgraded to the stub index's v1.0.0 and written
// back to config — the core behaviour the drift guard's no-op fixture never hits.
func TestUpdateAppliesUpgradeOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	cfgPath := writeInstalledAgentAt(t, tmpDir, "agents.cue", "v0.0.1")
	stub.SetFetch(sentinelModuleBase, registry.FetchResult{
		SourceDir: buildModuleFixture(t, "agent", agentFixtureBody),
	}, nil)

	decoded, raw := captureJSON(t, stub, "update", "--json")
	results, ok := decoded.([]any)
	if !ok {
		t.Fatalf("update --json should decode to an array, got %T", decoded)
	}
	res := findUpdateResult(t, results, sentinelAgentName)
	if res["updated"] != true {
		t.Errorf("update should have applied the upgrade; result: %v", res)
	}
	if res["oldVersion"] != "v0.0.1" {
		t.Errorf("oldVersion = %v, want v0.0.1", res["oldVersion"])
	}
	if res["newVersion"] != stubVersion {
		t.Errorf("newVersion = %v, want %q", res["newVersion"], stubVersion)
	}
	if strings.Contains(string(raw), `"error"`) {
		t.Errorf("update reported an error\noutput: %s", raw)
	}

	rewritten, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading rewritten config: %v", err)
	}
	if !strings.Contains(string(rewritten), "@"+stubVersion) {
		t.Errorf("config not rewritten to %s:\n%s", stubVersion, rewritten)
	}
	if strings.Contains(string(rewritten), "@v0.0.1") {
		t.Errorf("config still pins old version:\n%s", rewritten)
	}
}

// TestUpdateDryRunOffline asserts --dry-run reports the upgrade as applicable
// but does not rewrite the config (and never reaches Fetch).
func TestUpdateDryRunOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	cfgPath := writeInstalledAgentAt(t, tmpDir, "agents.cue", "v0.0.1")

	decoded, _ := captureJSON(t, stub, "update", "--dry-run", "--json")
	results := decoded.([]any)
	res := findUpdateResult(t, results, sentinelAgentName)
	if res["updated"] != true {
		t.Errorf("dry-run should mark the module updatable; result: %v", res)
	}

	unchanged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(unchanged), "@v0.0.1") {
		t.Errorf("dry-run must not rewrite the config:\n%s", unchanged)
	}
}

// TestUpdateForceOffline asserts --force re-fetches and rewrites a module that
// is already at the latest version (the non-force path would skip it).
func TestUpdateForceOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	writeInstalledAgentAt(t, tmpDir, "agents.cue", stubVersion)
	stub.SetFetch(sentinelModuleBase, registry.FetchResult{
		SourceDir: buildModuleFixture(t, "agent", agentFixtureBody),
	}, nil)

	decoded, _ := captureJSON(t, stub, "update", "--force", "--json")
	results := decoded.([]any)
	res := findUpdateResult(t, results, sentinelAgentName)
	if res["updated"] != true {
		t.Errorf("--force should re-apply an up-to-date module; result: %v", res)
	}
}

// TestUpdateNoArgsEmptyOffline asserts update prints the documented empty JSON
// array when no registry-origin modules are installed.
func TestUpdateNoArgsEmptyOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())

	decoded, _ := captureJSON(t, stub, "update", "--json")
	arr, ok := decoded.([]any)
	if !ok {
		t.Fatalf("update --json should decode to an array, got %T", decoded)
	}
	if len(arr) != 0 {
		t.Errorf("update with no installed registry modules should be empty, got %v", arr)
	}
}

// TestUpdateFetchErrorOffline asserts a fetch failure during an applicable
// upgrade is captured per-module as a non-fatal error in the result, exercising
// the UpdateResult error JSON field that the happy-path tests never populate.
func TestUpdateFetchErrorOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	cfgPath := writeInstalledAgentAt(t, tmpDir, "agents.cue", "v0.0.1")
	stub.SetFetch(sentinelModuleBase, registry.FetchResult{}, errFetchBoom)

	decoded, _ := captureJSON(t, stub, "update", "--json")
	results := decoded.([]any)
	res := findUpdateResult(t, results, sentinelAgentName)
	if res["updated"] == true {
		t.Errorf("update should not report success when fetch fails; result: %v", res)
	}
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "boom") {
		t.Errorf("update result missing fetch error message; got %q", msg)
	}

	// The config must be left untouched when the fetch fails.
	unchanged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(unchanged), "@v0.0.1") {
		t.Errorf("config should be unchanged after a failed fetch:\n%s", unchanged)
	}
}

// TestUpdateQueryFilterOffline asserts a query argument restricts the update set
// to matching modules.
func TestUpdateQueryFilterOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	writeInstalledAgentAt(t, tmpDir, "agents.cue", stubVersion)

	// A query matching nothing yields the documented empty array.
	decoded, _ := captureJSON(t, stub, "update", "no-such-module", "--json")
	if arr, ok := decoded.([]any); !ok || len(arr) != 0 {
		t.Errorf("update with non-matching query should be empty, got %v", decoded)
	}

	// A query matching the installed agent's name returns exactly it.
	decoded2, _ := captureJSON(t, stub, "update", "sentinel", "--json")
	results := decoded2.([]any)
	if len(results) != 1 {
		t.Fatalf("update sentinel should match one module, got %d: %v", len(results), results)
	}
	findUpdateResult(t, results, sentinelAgentName)
}

// TestDoctorSchemaSuccessOffline drives doctor's schema-validation success
// branch offline, exercising the path the drift guard only covers in its
// degraded "Skipped" form.
func TestDoctorSchemaSuccessOffline(t *testing.T) {
	_, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetch(registry.SchemaModulePath, registry.FetchResult{
		SourceDir: buildModuleFixture(t, "schemas", schemaFixtureBody),
	}, nil)

	decoded, raw := captureJSON(t, stub, "doctor", "--json")
	if stub.providerCalls != 2 {
		t.Errorf("doctor should consult the provider twice, got %d", stub.providerCalls)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("doctor --json should decode to an object, got %T", decoded)
	}
	if _, ok := obj["sections"]; !ok {
		t.Fatalf("doctor --json missing 'sections' key; got %v", mapKeys(obj))
	}
	if !strings.Contains(string(raw), "Schema Validation") {
		t.Errorf("doctor --json missing Schema Validation section\noutput: %s", raw)
	}
	for _, degraded := range []string{"registry unavailable", "cannot fetch schemas", "cannot resolve schema version", "cannot load schemas"} {
		if strings.Contains(string(raw), degraded) {
			t.Errorf("schema section degraded (%q) despite a served schema module\noutput: %s", degraded, raw)
		}
	}
}

// TestInstallOffline drives the install flow offline: the stub serves the index
// and agent fixture, and the module is written into local config. install has
// no other offline coverage.
func TestInstallOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	stub.SetFetch(sentinelModuleBase, registry.FetchResult{
		SourceDir: buildModuleFixture(t, "agent", agentFixtureBody),
	}, nil)

	out, err := captureText(t, stub, "install", sentinelAgentName, "--local")
	if err != nil {
		t.Fatalf("install: %v\noutput: %s", err, out)
	}

	cfgPath := filepath.Join(tmpDir, ".start", "agents.cue")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("install did not write agents.cue: %v", err)
	}
	if !strings.Contains(string(data), sentinelAgentName) {
		t.Errorf("installed config missing sentinel %q:\n%s", sentinelAgentName, data)
	}
	if !strings.Contains(string(data), "@"+stubVersion) {
		t.Errorf("installed config missing resolved version %s:\n%s", stubVersion, data)
	}
}

// TestInstallAlreadyInstalledOffline asserts install is a no-op (no error, no
// rewrite to an older version) when the module is already present at latest.
func TestInstallAlreadyInstalledOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	writeInstalledAgentAt(t, tmpDir, "agents.cue", stubVersion)

	out, err := captureText(t, stub, "install", sentinelAgentName, "--local")
	if err != nil {
		t.Fatalf("install (already installed): %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Already installed") {
		t.Errorf("expected already-installed notice\noutput: %s", out)
	}
}

// TestListVerboseOffline asserts list --verbose consults the registry through
// the seam to annotate update availability, running offline against the stub.
func TestListVerboseOffline(t *testing.T) {
	tmpDir, stub := setupStartTestConfigWithRegistry(t, stubLibraryIndex())
	writeInstalledAgentAt(t, tmpDir, "agents.cue", "v0.0.1")

	out, err := captureText(t, stub, "list", "--verbose")
	if err != nil {
		t.Fatalf("list --verbose: %v\noutput: %s", err, out)
	}
	if stub.providerCalls != 1 {
		t.Errorf("list --verbose should consult the provider once, got %d", stub.providerCalls)
	}
	if !strings.Contains(out, sentinelAgentName) {
		t.Errorf("list --verbose missing installed sentinel %q\noutput: %s", sentinelAgentName, out)
	}
}
