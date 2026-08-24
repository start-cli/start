package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/registry"
)

func TestLaunchAgentName(t *testing.T) {
	t.Parallel()
	withDefault := buildTestCfg(t, `{ settings: { default_agent: "grok/auto" } }`)
	withoutDefault := buildTestCfg(t, `{ settings: {} }`)

	t.Run("flag wins over default", func(t *testing.T) {
		got, err := launchAgentName(&Flags{Agent: []string{"echo"}}, withDefault.Value)
		if err != nil {
			t.Fatalf("launchAgentName() error = %v", err)
		}
		if got != "echo" {
			t.Errorf("launchAgentName() = %q, want %q", got, "echo")
		}
	})

	t.Run("omitted flag uses default_agent", func(t *testing.T) {
		got, err := launchAgentName(&Flags{}, withDefault.Value)
		if err != nil {
			t.Fatalf("launchAgentName() error = %v", err)
		}
		if got != "grok/auto" {
			t.Errorf("launchAgentName() = %q, want %q", got, "grok/auto")
		}
	})

	t.Run("omitted flag and unset default is empty", func(t *testing.T) {
		got, err := launchAgentName(&Flags{}, withoutDefault.Value)
		if err != nil {
			t.Fatalf("launchAgentName() error = %v", err)
		}
		if got != "" {
			t.Errorf("launchAgentName() = %q, want empty", got)
		}
	})

	t.Run("multiple --agent is usage", func(t *testing.T) {
		_, err := launchAgentName(&Flags{Agent: []string{"echo", "other"}}, withDefault.Value)
		if err == nil {
			t.Fatal("expected usage error for multiple --agent")
		}
		if got := ExitCodeFromError(err); got != ExitUsage {
			t.Errorf("exit code = %d, want %d (usage)", got, ExitUsage)
		}
	})
}

func TestComputeWantLive_DefaultAgent(t *testing.T) {
	t.Parallel()
	installed := buildTestCfg(t, `{
		settings: { default_agent: "claude" }
		agents: { claude: { bin: "claude", command: "{{.bin}}" } }
	}`)
	missingDefault := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: { claude: { bin: "claude", command: "{{.bin}}" } }
	}`)

	t.Run("installed default stays cache-gated", func(t *testing.T) {
		r := newTestResolver(installed)
		agent, err := launchAgentName(&Flags{}, installed.Value)
		if err != nil {
			t.Fatal(err)
		}
		if r.computeWantLive(baseSurfaces(&Flags{}, agent)) {
			t.Error("installed default_agent must not force live")
		}
	})

	t.Run("uninstalled default forces live", func(t *testing.T) {
		r := newTestResolver(missingDefault)
		agent, err := launchAgentName(&Flags{}, missingDefault.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !r.computeWantLive(baseSurfaces(&Flags{}, agent)) {
			t.Error("uninstalled default_agent must force live")
		}
	})

	t.Run("--agent installed ignores uninstalled default", func(t *testing.T) {
		r := newTestResolver(missingDefault)
		flags := &Flags{Agent: []string{"claude"}}
		agent, err := launchAgentName(flags, missingDefault.Value)
		if err != nil {
			t.Fatal(err)
		}
		if agent != "claude" {
			t.Fatalf("identifier = %q, want claude", agent)
		}
		if r.computeWantLive(baseSurfaces(flags, agent)) {
			t.Error("unused default_agent must not force live when --agent is installed")
		}
	})

	t.Run("--agent uninstalled forces live despite installed default", func(t *testing.T) {
		r := newTestResolver(installed)
		flags := &Flags{Agent: []string{"missing-agent"}}
		agent, err := launchAgentName(flags, installed.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !r.computeWantLive(baseSurfaces(flags, agent)) {
			t.Error("uninstalled --agent must force live")
		}
	})
}

func TestResolveAgent_SlashNameExact(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: {
			"grok/auto": { bin: "grok", command: "{{.bin}}" }
		}
	}`)
	r := newTestResolver(cfg)
	name, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.resolveAgent(name)
	if err != nil {
		t.Fatalf("resolveAgent(%q) error = %v", name, err)
	}
	if got != "grok/auto" {
		t.Errorf("resolveAgent(%q) = %q, want %q", name, got, "grok/auto")
	}
}

// TestDefaultAgent_CfgStaleReloadSurfacesAgent covers the post-install visibility
// a registry-only default needs before ExtractAgent: the module lands on disk,
// cfgStale triggers reload, then the identified default resolves and extracts.
// A successful auto-install needs the live registry, which the offline test
// binary lacks, so the write is simulated the same way as the role reload test.
func TestDefaultAgent_CfgStaleReloadSurfacesAgent(t *testing.T) {
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "settings.cue")

	discard := io.Discard
	stdin := strings.NewReader("")
	flags := &Flags{}

	before := `
agents: {}
settings: { default_agent: "grok/auto" }
`
	if err := os.WriteFile(configFile, []byte(before), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := loadMergedConfigFromDirWithDebug(discard, discard, stdin, tmpDir, flags)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	agent, err := launchAgentName(flags, cfg.Value)
	if err != nil {
		t.Fatalf("launchAgentName: %v", err)
	}
	if agent != "grok/auto" {
		t.Fatalf("identifier = %q, want grok/auto", agent)
	}

	r := newResolver(cfg, flags, discard, discard, stdin)
	if _, err := r.resolveAgent(agent); err == nil {
		t.Fatal("default agent should be unresolvable before it is installed")
	}

	after := `
agents: {
	"grok/auto": { bin: "echo", command: "{{.bin}} grok" }
}
settings: { default_agent: "grok/auto" }
`
	r.cfgStale = true
	if err := os.WriteFile(configFile, []byte(after), 0o644); err != nil {
		t.Fatalf("rewriting config: %v", err)
	}
	if err := r.reloadConfig(tmpDir); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if r.cfgStale {
		t.Error("reloadConfig should clear cfgStale")
	}

	got, err := r.resolveAgent(agent)
	if err != nil {
		t.Fatalf("default agent should resolve after reload: %v", err)
	}
	if got != "grok/auto" {
		t.Errorf("resolveAgent = %q, want grok/auto", got)
	}

	env, err := buildExecutionEnv(r.cfg, tmpDir, got, flags, discard, discard, stdin)
	if err != nil {
		t.Fatalf("buildExecutionEnv after reload: %v", err)
	}
	if env.Agent.Name != "grok/auto" {
		t.Errorf("extracted agent = %q, want grok/auto", env.Agent.Name)
	}
}

func TestResolveAgent_DefaultAgentRegistryOnlyInstalls(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: {}
	}`)
	r := newResolverWithIndex(cfg, &registry.Index{
		Agents: map[string]registry.IndexEntry{
			"grok/auto": {Module: "github.com/test/agents/grok/auto@v0"},
		},
	})
	name, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.resolveAgent(name)
	if err == nil {
		t.Fatal("expected install attempt for registry-only exact")
	}
	if !strings.Contains(err.Error(), "registry client unavailable") {
		t.Errorf("error = %q, want install path reached", err.Error())
	}
}

func TestResolveAgent_DefaultAgentNotFound(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: {}
	}`)
	r := newResolverWithIndex(cfg, &registry.Index{})
	name, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.resolveAgent(name)
	if err == nil {
		t.Fatal("expected not-found")
	}
	if strings.Contains(err.Error(), "loading agent") {
		t.Errorf("error = %q, should fail in resolution not ExtractAgent", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

func TestResolveAgent_DefaultAgentIndexUnreachable(t *testing.T) {
	t.Parallel()
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: {}
	}`)
	r := newResolver(cfg, &Flags{}, io.Discard, io.Discard, strings.NewReader(""))
	r.didFetch = true
	r.index = nil
	r.indexErr = &registry.FetchError{Kind: registry.FetchTransient, Op: "fetch", Path: "x", Err: io.EOF}
	name, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.resolveAgent(name)
	if err == nil {
		t.Fatal("expected transient error")
	}
	if got := ExitCodeFromError(err); got != ExitTransient {
		t.Errorf("exit code = %d, want %d (transient)", got, ExitTransient)
	}
}

func TestDefaultAgent_UninstalledForcesLiveAcquisition(t *testing.T) {
	isolateConfigEnv(t)
	seedFreshIndexCache(t)
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: {}
	}`)
	r, src := newRecordingResolver(cfg, &registry.Index{})
	agent, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	r.wantLive = r.computeWantLive(baseSurfaces(&Flags{}, agent))
	_, _ = r.resolveAgent(agent)
	if len(src.live) == 0 || !src.live[0] {
		t.Errorf("uninstalled default_agent should resolve live, got %v", src.live)
	}
}

func TestDefaultAgent_InstalledStaysCacheGated(t *testing.T) {
	isolateConfigEnv(t)
	seedFreshIndexCache(t)
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "claude" }
		agents: { claude: { bin: "claude", command: "{{.bin}}" } }
	}`)
	r, src := newRecordingResolver(cfg, &registry.Index{})
	agent, err := launchAgentName(&Flags{}, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	r.wantLive = r.computeWantLive(baseSurfaces(&Flags{}, agent))
	if _, err := r.resolveAgent(agent); err != nil {
		t.Fatalf("resolveAgent(%q) error = %v", agent, err)
	}
	if len(src.live) != 0 {
		t.Errorf("installed default_agent should make no registry call, got %v", src.live)
	}
}

func TestDefaultAgent_AgentFlagIgnoresUninstalledDefault(t *testing.T) {
	isolateConfigEnv(t)
	seedFreshIndexCache(t)
	cfg := buildTestCfg(t, `{
		settings: { default_agent: "grok/auto" }
		agents: { claude: { bin: "claude", command: "{{.bin}}" } }
	}`)
	r, src := newRecordingResolver(cfg, &registry.Index{})
	flags := &Flags{Agent: []string{"claude"}}
	agent, err := launchAgentName(flags, cfg.Value)
	if err != nil {
		t.Fatal(err)
	}
	r.wantLive = r.computeWantLive(baseSurfaces(flags, agent))
	if _, err := r.resolveAgent(agent); err != nil {
		t.Fatalf("resolveAgent(%q) error = %v", agent, err)
	}
	if len(src.live) != 0 {
		t.Errorf("--agent installed should ignore uninstalled default, got %v", src.live)
	}
}

const defaultAgentLaunchCfg = `
agents: {
	echo: {
		bin: "echo"
		command: "{{.bin}} 'Agent executed'"
	}
}
roles: {
	assistant: { prompt: "You are a helpful assistant." }
}
contexts: {
	env: { required: true, prompt: "Environment context" }
}
tasks: {
	"test-task": { prompt: "Test task prompt." }
}
settings: {
	default_agent: "grok/auto"
}
`

const unsetDefaultTwoAgentsCfg = `
agents: {
	alpha: {
		bin: "echo"
		command: "{{.bin}} alpha"
	}
	echo: {
		bin: "echo"
		command: "{{.bin}} echo"
	}
}
roles: {
	assistant: { prompt: "You are a helpful assistant." }
}
contexts: {
	env: { required: true, prompt: "Environment context" }
}
`

func writeLaunchConfig(t *testing.T, body string) {
	t.Helper()
	tmpDir := isolateConfigEnv(t)
	configDir := filepath.Join(tmpDir, ".start")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.cue"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	chdir(t, tmpDir)
}

func TestExecuteStart_UninstalledDefaultAgentNotFoundFromResolver(t *testing.T) {
	writeLaunchConfig(t, defaultAgentLaunchCfg)

	err := executeStart(io.Discard, io.Discard, strings.NewReader(""), &Flags{DryRun: true}, orchestration.ContextSelection{
		IncludeRequired: true,
	}, "")
	if err == nil {
		t.Fatal("expected not-found for uninstalled default_agent")
	}
	if strings.Contains(err.Error(), "loading agent") {
		t.Errorf("error = %q, should fail in resolution not ExtractAgent", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

func TestExecuteStart_AgentFlagOverridesUninstalledDefault(t *testing.T) {
	writeLaunchConfig(t, defaultAgentLaunchCfg)

	stdout := new(bytes.Buffer)
	err := executeStart(stdout, io.Discard, strings.NewReader(""), &Flags{
		DryRun: true,
		Agent:  []string{"echo"},
	}, orchestration.ContextSelection{
		IncludeRequired: true,
	}, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "echo") {
		t.Errorf("expected --agent echo to launch, got:\n%s", stdout.String())
	}
}

func TestExecuteStart_UnsetDefaultSelectsInstalledOnly(t *testing.T) {
	writeLaunchConfig(t, unsetDefaultTwoAgentsCfg)

	stdout := new(bytes.Buffer)
	err := executeStart(stdout, io.Discard, strings.NewReader(""), &Flags{DryRun: true}, orchestration.ContextSelection{
		IncludeRequired: true,
	}, "")
	if err != nil {
		t.Fatalf("executeStart() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "alpha") {
		t.Errorf("expected first installed agent, got:\n%s", stdout.String())
	}
}

func TestExecuteTask_UninstalledDefaultAgentNotFoundFromResolver(t *testing.T) {
	writeLaunchConfig(t, defaultAgentLaunchCfg)

	err := executeTask(io.Discard, io.Discard, strings.NewReader(""), &Flags{DryRun: true}, "test-task", "")
	if err == nil {
		t.Fatal("expected not-found for uninstalled default_agent")
	}
	if strings.Contains(err.Error(), "loading agent") {
		t.Errorf("error = %q, should fail in resolution not ExtractAgent", err.Error())
	}
	if got := ExitCodeFromError(err); got != ExitNotFound {
		t.Errorf("exit code = %d, want %d (not-found)", got, ExitNotFound)
	}
}

func TestExecuteTask_AgentFlagOverridesUninstalledDefault(t *testing.T) {
	writeLaunchConfig(t, defaultAgentLaunchCfg)

	stdout := new(bytes.Buffer)
	err := executeTask(stdout, io.Discard, strings.NewReader(""), &Flags{
		DryRun: true,
		Agent:  []string{"echo"},
	}, "test-task", "")
	if err != nil {
		t.Fatalf("executeTask() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "echo") {
		t.Errorf("expected --agent echo to launch, got:\n%s", stdout.String())
	}
}
