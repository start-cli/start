package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cuelang.org/go/cue"
	"github.com/fatih/color"
	"github.com/p3bot/start/internal/config"
	internalcue "github.com/p3bot/start/internal/cue"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/shell"
	"github.com/p3bot/start/internal/temp"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

// flagsKey is the context key for storing Flags.
type flagsKey struct{}

// Flags holds all CLI flag values. Each command instance gets its own Flags so
// tests can run in parallel without shared state.
type Flags struct {
	Agent   []string
	Role    string
	Model   string
	Context []string
	DryRun  bool
	Quiet   bool
	Verbose bool
	Debug   bool
	Color   string
	Local   bool
	Global  bool
	Refresh bool

	// NoRole and NoImplicitContexts are derived skip state, set by none-sentinel
	// normalisation in PersistentPreRunE rather than bound to a flag. NoRole
	// skips role assignment. NoImplicitContexts suppresses the contexts that
	// load automatically (required and default), leaving only the explicit
	// selectors in Context — so "--context none" yields zero contexts and
	// "--context none,foo" yields just foo.
	NoRole             bool
	NoImplicitContexts bool

	// markdownStyle holds the glamour style ("dark" or "light"), settled lazily
	// on first use via MarkdownStyle. markdownStyleOnce confines the terminal
	// background probe to a single fire per invocation. Flags is always handled
	// by pointer, so the sync.Once is never copied.
	markdownStyleOnce  sync.Once
	markdownStyleValue string
}

// MarkdownStyle returns the glamour style for this invocation, probing the
// terminal background at most once and only on first use. Commands that never
// render Markdown never call it, so they never probe; the probe fires only when
// content is actually about to be styled. Decoration is derived from the
// already-settled color.NoColor, so the result is "dark" whenever decoration is
// off, when stdout is not a TTY, or when detection fails.
func (f *Flags) MarkdownStyle() string {
	f.markdownStyleOnce.Do(func() {
		f.markdownStyleValue = settleMarkdownStyle(!color.NoColor)
	})
	return f.markdownStyleValue
}

// getFlags retrieves Flags from the command context.
func getFlags(cmd *cobra.Command) *Flags {
	if f, ok := cmd.Context().Value(flagsKey{}).(*Flags); ok {
		return f
	}
	// Fallback for commands without context (shouldn't happen in normal use).
	return &Flags{}
}

// roleSkipReason is the opt-out reason stamped on RoleOutcome when --role none
// routes execution through Compose, which performs no role logic.
const roleSkipReason = "--role none"

// roleSkipOutcome is the role section outcome for a deliberate --role none
// opt-out. Compose performs no role logic and cannot know why it was called, so
// the CLI branch that routes there owns this outcome.
func roleSkipOutcome() orchestration.SectionOutcome {
	return orchestration.SectionOutcome{State: orchestration.SectionSkipped, Reason: roleSkipReason}
}

// Debug log categories.
const (
	dbgConfig  = "config"
	dbgAgent   = "agent"
	dbgRole    = "role"
	dbgContext = "context"
	dbgTask    = "task"
	dbgCompose = "compose"
	dbgExec    = "exec"
	dbgResolve = "resolve"
	dbgCache   = "cache"
)

// debugf prints debug output if debug mode is enabled.
// Format: [DEBUG HH:MM:SS.mmm] <category>: <message>
func debugf(stderr io.Writer, flags *Flags, category, format string, args ...any) {
	if flags.Debug {
		ts := time.Now().Format("15:04:05.000")
		fmt.Fprintf(stderr, "[DEBUG %s] %s: "+format+"\n", append([]any{ts, category}, args...)...)
	}
}

// ExecutionEnv holds the common execution environment for start and task commands.
type ExecutionEnv struct {
	Cfg        internalcue.LoadResult
	WorkingDir string
	Agent      orchestration.Agent
	Composer   *orchestration.Composer
	Executor   *orchestration.Executor
}

// loadExecutionConfig loads configuration and resolves the working directory.
// Separated from environment setup so the resolver can search installed config
// before the full environment is built.
func loadExecutionConfig(stdout, stderr io.Writer, stdin io.Reader, flags *Flags) (internalcue.LoadResult, string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return internalcue.LoadResult{}, "", fmt.Errorf("getting working directory: %w", err)
	}
	debugf(stderr, flags, dbgConfig, "Working directory: %s", workingDir)

	cfg, err := loadMergedConfigFromDirWithDebug(stdout, stderr, stdin, workingDir, flags)
	if err != nil {
		return internalcue.LoadResult{}, "", fmt.Errorf("loading configuration: %w", err)
	}

	return cfg, workingDir, nil
}

// launchAgentName returns the agent identifier for this launch: --agent if
// supplied, otherwise settings.default_agent when set. An empty result means
// fall through to the installed-only picker in resolveAgentName. --agent wins
// over the default; multiple --agent values are a usage error on launch.
func launchAgentName(flags *Flags, cfg cue.Value) (string, error) {
	switch len(flags.Agent) {
	case 0:
		return getDefaultAgentFromConfig(cfg), nil
	case 1:
		return flags.Agent[0], nil
	default:
		return "", usageError(fmt.Errorf("launch accepts at most one --agent"))
	}
}

// resolveAgentName is the installed-only picker used when the agent surface had
// no identifier: the only configured agent, a TTY menu, or the first agent on a
// non-TTY. settings.default_agent is an identifier for launchAgentName, not a
// picker choice.
func resolveAgentName(cfg internalcue.LoadResult, flags *Flags, stdout, stderr io.Writer, stdin io.Reader) (string, error) {
	choices, err := getConfiguredAgents(cfg.Value)
	if err != nil {
		return "", err
	}
	switch len(choices) {
	case 0:
		return "", fmt.Errorf("no agent configured")
	case 1:
		debugf(stderr, flags, dbgAgent, "Selected %q (only agent)", choices[0].Name)
		return choices[0].Name, nil
	}

	isTTY := isTerminal(stdin)
	if !isTTY {
		name := choices[0].Name
		debugf(stderr, flags, dbgAgent, "Selected %q (first agent, non-TTY)", name)
		if !flags.Quiet {
			fmt.Fprintf(stdout, "Using agent %q %s\n", name, tui.Annotate("set default_agent or use --agent to specify"))
		}
		return name, nil
	}

	// bufio.NewReader may buffer ahead, but nothing reads stdin after agent
	// resolution (the process is replaced by syscall.Exec).
	reader := bufio.NewReader(stdin)
	selected, err := promptAgentSelection(stdout, reader, choices)
	if err != nil {
		return "", err
	}
	debugf(stderr, flags, dbgAgent, "Selected %q (interactive)", selected)
	if promptSetDefault(stdout, reader, selected) {
		if err := setSetting(stdout, flags, "default_agent", selected, false); err != nil {
			printWarning(stdout, "could not save default: %v", err)
		}
	}
	return selected, nil
}

// buildExecutionEnv builds the execution environment from a loaded config and
// agent name, after the resolver has resolved the launch agent identifier.
func buildExecutionEnv(cfg internalcue.LoadResult, workingDir string, agentName string, flags *Flags, stdout, stderr io.Writer, stdin io.Reader) (*ExecutionEnv, error) {
	if agentName == "" {
		resolved, err := resolveAgentName(cfg, flags, stdout, stderr, stdin)
		if err != nil {
			return nil, err
		}
		agentName = resolved
	} else {
		debugf(stderr, flags, dbgAgent, "Selected %q", agentName)
	}

	agent, err := orchestration.ExtractAgent(cfg.Value, agentName)
	if err != nil {
		return nil, fmt.Errorf("loading agent: %w", err)
	}
	debugf(stderr, flags, dbgAgent, "Binary: %s", agent.Bin)
	debugf(stderr, flags, dbgAgent, "Command template: %s", agent.Command)

	shellRunner := shell.NewRunner()
	processor := orchestration.NewTemplateProcessor(nil, shellRunner, workingDir)
	composer := orchestration.NewComposer(processor, workingDir)
	executor := orchestration.NewExecutor(workingDir)

	return &ExecutionEnv{
		Cfg:        cfg,
		WorkingDir: workingDir,
		Agent:      agent,
		Composer:   composer,
		Executor:   executor,
	}, nil
}

// agentChoice represents an agent available for interactive selection.
type agentChoice struct {
	Name        string
	Description string
}

// getConfiguredAgents returns the list of configured agents in definition order.
func getConfiguredAgents(cfg cue.Value) ([]agentChoice, error) {
	agents := cfg.LookupPath(cue.ParsePath(internalcue.KeyAgents))
	if !agents.Exists() {
		return nil, nil
	}
	iter, err := agents.Fields()
	if err != nil {
		return nil, fmt.Errorf("reading agents configuration: %w", err)
	}
	var choices []agentChoice
	for iter.Next() {
		name := iter.Selector().Unquoted()
		val := iter.Value()
		var desc string
		if v := val.LookupPath(cue.ParsePath("description")); v.Exists() {
			desc, _ = v.String()
		}
		choices = append(choices, agentChoice{Name: name, Description: desc})
	}
	return choices, nil
}

// promptAgentSelection prompts the user to select an agent from multiple
// choices. The caller is responsible for TTY detection.
func promptAgentSelection(w io.Writer, reader *bufio.Reader, choices []agentChoice) (string, error) {
	fmt.Fprintf(w, "Multiple agents configured. Select an agent:\n\n")

	maxNameLen := 0
	for _, c := range choices {
		if len(c.Name) > maxNameLen {
			maxNameLen = len(c.Name)
		}
	}

	for i, c := range choices {
		if c.Description != "" {
			padding := strings.Repeat(" ", maxNameLen-len(c.Name)+2)
			fmt.Fprintf(w, "  %2d. %s%s%s\n", i+1, c.Name, padding, c.Description)
		} else {
			fmt.Fprintf(w, "  %2d. %s\n", i+1, c.Name)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Select %s: ", tui.Annotate("1-%d", len(choices)))

	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("no selection provided")
	}

	if choice, err := strconv.Atoi(input); err == nil {
		if choice >= 1 && choice <= len(choices) {
			return choices[choice-1].Name, nil
		}
		return "", fmt.Errorf("invalid selection: %s (choose 1-%d)", input, len(choices))
	}

	inputLower := strings.ToLower(input)
	for _, c := range choices {
		if strings.ToLower(c.Name) == inputLower {
			return c.Name, nil
		}
	}

	var subMatches []agentChoice
	for _, c := range choices {
		if strings.Contains(strings.ToLower(c.Name), inputLower) {
			subMatches = append(subMatches, c)
		}
	}
	if len(subMatches) == 1 {
		return subMatches[0].Name, nil
	}

	return "", fmt.Errorf("invalid selection: %s", input)
}

// promptSetDefault asks whether to set the selected agent as default. The
// caller is responsible for TTY detection.
func promptSetDefault(w io.Writer, reader *bufio.Reader, agentName string) bool {
	fmt.Fprintf(w, "Set %q as default agent? %s: ", agentName, tui.Bracket("y/N"))

	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// runStart executes the start command (root command with no subcommand).
//
// When stdin is piped (not a TTY) and non-blank, the piped content becomes
// the prompt text and only required contexts are included — making
// `echo hi | start` behave like `start prompt "hi"`. A blank or empty pipe
// means no prompt was given, so it runs the normal start flow with default
// contexts — `start </dev/null` behaves like bare `start`.
func runStart(cmd *cobra.Command, args []string) error {
	flags := getFlags(cmd)
	stdin := cmd.InOrStdin()

	pipedText, piped, err := readPipedStdin(stdin)
	if err != nil {
		return err
	}
	if piped && strings.TrimSpace(pipedText) != "" {
		return executeStart(cmd.OutOrStdout(), cmd.ErrOrStderr(), stdin, flags, orchestration.ContextSelection{
			IncludeRequired: true,
			IncludeDefaults: false,
			Tags:            flags.Context,
		}, pipedText)
	}

	return executeStart(cmd.OutOrStdout(), cmd.ErrOrStderr(), stdin, flags, orchestration.ContextSelection{
		IncludeRequired: true,
		IncludeDefaults: true,
		Tags:            flags.Context,
	}, "")
}

// executeStart is the shared execution logic for start commands.
func executeStart(stdout, stderr io.Writer, stdin io.Reader, flags *Flags, selection orchestration.ContextSelection, customText string) error {
	if flags.NoImplicitContexts {
		// Drop the auto-loaded required and default contexts; any explicit tag
		// or path selectors in selection.Tags still apply. SuppressImplicit
		// records the opt-out so Compose can stamp a skip rather than none when
		// the resulting list is empty.
		selection.IncludeRequired = false
		selection.IncludeDefaults = false
		selection.SuppressImplicit = true
	}

	cfg, workingDir, err := loadExecutionConfig(stdout, stderr, stdin, flags)
	if err != nil {
		return err
	}

	agentName, err := launchAgentName(flags, cfg.Value)
	if err != nil {
		return err
	}

	r := newResolver(cfg, flags, stdout, stderr, stdin)

	// Decide index liveness once, up front: live iff --refresh is set or some
	// surface has no installed match. Computed before the first resolve so the
	// choice is position-independent and the held index serves the whole
	// invocation. selection.Tags equals flags.Context here, so baseSurfaces
	// covers the contexts this invocation will resolve.
	r.wantLive = r.computeWantLive(baseSurfaces(flags, agentName))

	if agentName != "" {
		agentName, err = r.resolveAgent(agentName)
		if err != nil {
			return err
		}
	}

	roleName := flags.Role
	if roleName != "" && !flags.NoRole {
		roleName, err = r.resolveRole(roleName)
		if err != nil {
			return err
		}
	}

	if len(selection.Tags) > 0 {
		selection.Tags, err = r.resolveContexts(selection.Tags)
		if err != nil {
			return err
		}
	}

	if r.cfgStale {
		debugf(stderr, flags, dbgConfig, "Reloading config after registry installs")
		if err := r.reloadConfig(workingDir); err != nil {
			return err
		}
		cfg = r.cfg
	}

	env, err := buildExecutionEnv(cfg, workingDir, agentName, flags, stdout, stderr, stdin)
	if err != nil {
		return err
	}

	resolvedModel := flags.Model
	if resolvedModel != "" {
		resolvedModel = r.resolveModelName(resolvedModel, env.Agent)
	}

	debugf(stderr, flags, dbgContext, "Selection: required=%t, defaults=%t, tags=%v",
		selection.IncludeRequired, selection.IncludeDefaults, selection.Tags)

	var result orchestration.ComposeResult
	var composeErr error
	if flags.NoRole {
		debugf(stderr, flags, dbgRole, "Skipping role (--role none)")
		result, composeErr = env.Composer.Compose(env.Cfg.Value, selection, customText)
		result.RoleOutcome = roleSkipOutcome()
	} else {
		result, composeErr = env.Composer.ComposeWithRole(env.Cfg.Value, selection, roleName, customText)
	}
	if composeErr != nil {
		// Show role resolutions before returning the error.
		if !flags.Quiet && len(result.RoleResolutions) > 0 {
			printComposeError(stdout, env.Agent, result)
		}
		return fmt.Errorf("composing prompt: %w", composeErr)
	}

	debugf(stderr, flags, dbgRole, "Selected %q", result.RoleName)
	for _, ctx := range result.Contexts {
		debugf(stderr, flags, dbgContext, "Including %q", ctx.Name)
	}
	debugf(stderr, flags, dbgCompose, "Role: %d bytes", len(result.Role))
	debugf(stderr, flags, dbgCompose, "Prompt: %d bytes (%d contexts)", len(result.Prompt), len(result.Contexts))

	printWarnings(flags, stderr, result.Warnings)

	model, modelSource := resolveModel(resolvedModel, env.Agent.DefaultModel)
	if model != "" {
		debugf(stderr, flags, dbgAgent, "Model: %s (%s)", model, modelSource)
	} else {
		debugf(stderr, flags, dbgAgent, "Model: agent default (none specified)")
	}

	execConfig := orchestration.ExecuteConfig{
		Agent:      env.Agent,
		Model:      resolvedModel,
		Role:       result.Role,
		RoleFile:   result.RoleFile,
		Prompt:     result.Prompt,
		WorkingDir: env.WorkingDir,
		DryRun:     flags.DryRun,
	}

	cmdStr, err := env.Executor.BuildCommand(execConfig)
	if err != nil {
		return err
	}
	debugf(stderr, flags, dbgExec, "Final command: %s", cmdStr)

	if flags.DryRun {
		debugf(stderr, flags, dbgExec, "Dry-run mode, skipping execution")
		return executeDryRun(stdout, cmdStr, execConfig, result, env.Agent, model, modelSource)
	}

	if !flags.Quiet {
		printExecutionInfo(stdout, env.Agent, model, modelSource, result)
	}

	debugf(stderr, flags, dbgExec, "Executing agent (process replacement)")
	// Replaces the current process; command already validated.
	return env.Executor.ExecuteCommand(cmdStr, execConfig)
}

// resolveModel determines the effective model and its source ("--model" or
// "config"), or empty strings when both are empty.
func resolveModel(flagModel, configModel string) (model, source string) {
	if flagModel != "" {
		return flagModel, "--model"
	}
	if configModel != "" {
		return configModel, "config"
	}
	return "", ""
}

// printWarnings prints warnings to stderr if not in quiet mode.
func printWarnings(flags *Flags, stderr io.Writer, warnings []string) {
	if flags.Quiet {
		return
	}
	for _, w := range warnings {
		printWarning(stderr, "%s", w)
	}
}

// executeDryRun handles --dry-run mode. cmdStr is the pre-built, pre-validated
// command string from the caller.
func executeDryRun(w io.Writer, cmdStr string, cfg orchestration.ExecuteConfig, result orchestration.ComposeResult, agent orchestration.Agent, model, modelSource string) error {
	tempMgr := temp.NewDryRunManager()
	dir, err := tempMgr.DryRunDir()
	if err != nil {
		return fmt.Errorf("creating dry-run directory: %w", err)
	}

	var contextNames []string
	for _, ctx := range result.Contexts {
		contextNames = append(contextNames, ctx.Name)
	}

	cmdContent := orchestration.GenerateDryRunCommand(agent, cfg.Model, result.RoleName, contextNames, cfg.WorkingDir, cmdStr)

	if err := tempMgr.WriteDryRunFiles(dir, result.Role, result.Prompt, cmdContent); err != nil {
		return fmt.Errorf("writing dry-run files: %w", err)
	}

	printDryRunSummary(w, agent, model, modelSource, result, dir)

	return nil
}

// printExecutionInfo prints the execution summary.
func printExecutionInfo(w io.Writer, agent orchestration.Agent, model, modelSource string, result orchestration.ComposeResult) {
	printHeader(w, "Starting AI Agent")
	printSeparator(w)

	printAgentModel(w, agent, model, modelSource)
	printContextTable(w, result.Contexts, result.ContextOutcome, result.Selection)
	printRoleTable(w, result.RoleOutcome, result.RoleResolutions)

	fmt.Fprintf(w, "Starting %s - awaiting response...\n", agent.Name)
}

// printDryRunSummary prints the dry-run summary.
func printDryRunSummary(w io.Writer, agent orchestration.Agent, model, modelSource string, result orchestration.ComposeResult, dir string) {
	printHeader(w, "Dry Run - Agent Not Executed")
	printSeparator(w)

	printAgentModel(w, agent, model, modelSource)
	printContextTable(w, result.Contexts, result.ContextOutcome, result.Selection)
	printRoleTable(w, result.RoleOutcome, result.RoleResolutions)

	if result.Role != "" {
		printContentPreview(w, "Role", tui.ColorRoles, result.Role, 5)
		fmt.Fprintln(w)
	}

	if result.Prompt != "" {
		printContentPreview(w, "Prompt", tui.ColorPrompts, result.Prompt, 5)
		fmt.Fprintln(w)
	}

	tui.ColorDim.Fprint(w, "Files:")
	fmt.Fprintf(w, " %s\n", dir)
	fmt.Fprintln(w, "  role.md")
	fmt.Fprintln(w, "  prompt.md")
	fmt.Fprintln(w, "  command.txt")
}

// printComposeError shows agent, contexts, and role resolutions before a
// composition error, so the user understands what failed.
func printComposeError(w io.Writer, agent orchestration.Agent, result orchestration.ComposeResult) {
	printHeader(w, "Starting AI Agent")
	printSeparator(w)

	tui.ColorAgents.Fprint(w, "Agent:")
	fmt.Fprintf(w, " %s\n", agent.Name)
	fmt.Fprintln(w)

	printContextTable(w, result.Contexts, result.ContextOutcome, result.Selection)

	printRoleTable(w, result.RoleOutcome, result.RoleResolutions)
}

// printContentPreview prints content, showing all lines when total <= 2*maxLines
// and otherwise truncating to maxLines with a line-count header.
func printContentPreview(w io.Writer, label string, labelColor *color.Color, text string, maxLines int) {
	lines := strings.Split(text, "\n")
	threshold := maxLines * 2
	truncated := len(lines) > threshold

	if truncated {
		labelColor.Fprint(w, label)
		fmt.Fprintf(w, " %s:\n", tui.Annotate("%d lines", maxLines))
	} else {
		labelColor.Fprint(w, label)
		fmt.Fprintln(w, ":")
	}

	if truncated {
		for i := range maxLines {
			fmt.Fprintf(w, "  %s\n", lines[i])
		}
		fmt.Fprintf(w, "  ... %s\n", tui.Annotate("%d more lines", len(lines)-maxLines))
	} else {
		for _, line := range lines {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// loadMergedConfigFromDir loads configuration using the specified working directory
// for local config resolution. If workingDir is empty, uses current directory.
func loadMergedConfigFromDir(workingDir string) (internalcue.LoadResult, error) {
	return loadMergedConfigWithIO(os.Stdout, os.Stderr, os.Stdin, workingDir)
}

// loadMergedConfigFromDirWithDebug loads configuration with debug logging.
func loadMergedConfigFromDirWithDebug(stdout, stderr io.Writer, stdin io.Reader, workingDir string, flags *Flags) (internalcue.LoadResult, error) {
	paths, err := config.ResolvePaths(workingDir)
	if err != nil {
		return internalcue.LoadResult{}, fmt.Errorf("resolving config paths: %w", err)
	}

	debugf(stderr, flags, dbgConfig, "Global: %s (exists: %t)", paths.Global, paths.GlobalExists)
	debugf(stderr, flags, dbgConfig, "Local: %s (exists: %t)", paths.Local, paths.LocalExists)

	result, err := loadMergedConfigWithIO(stdout, stderr, stdin, workingDir)
	if err != nil {
		return result, err
	}

	var loaded []string
	if result.GlobalLoaded {
		loaded = append(loaded, "global")
	}
	if result.LocalLoaded {
		loaded = append(loaded, "local")
	}
	if len(loaded) > 0 {
		debugf(stderr, flags, dbgConfig, "Loaded from: %s", strings.Join(loaded, ", "))
	}

	return result, nil
}

// loadMergedConfigWithIO loads configuration with custom I/O streams.
func loadMergedConfigWithIO(stdout, stderr io.Writer, stdin io.Reader, workingDir string) (internalcue.LoadResult, error) {
	paths, err := config.ResolvePaths(workingDir)
	if err != nil {
		return internalcue.LoadResult{}, fmt.Errorf("resolving config paths: %w", err)
	}

	validation := config.ValidateConfig(paths)

	if !validation.AnyValid() {
		// Config exists but is invalid: report the first error with full details.
		if validation.HasErrors() {
			if validation.GlobalError != nil {
				return internalcue.LoadResult{}, fmt.Errorf("%s", validation.GlobalError.DetailedError())
			}
			if validation.LocalError != nil {
				return internalcue.LoadResult{}, fmt.Errorf("%s", validation.LocalError.DetailedError())
			}
		}

		if err := runAutoSetup(stdout, stderr, stdin); err != nil {
			return internalcue.LoadResult{}, err
		}
		// Re-resolve and validate after auto-setup.
		paths, err = config.ResolvePaths(workingDir)
		if err != nil {
			return internalcue.LoadResult{}, fmt.Errorf("resolving config paths: %w", err)
		}
		validation = config.ValidateConfig(paths)
		if !validation.AnyValid() {
			return internalcue.LoadResult{}, fmt.Errorf("auto-setup did not create valid configuration")
		}
	}

	dirs := paths.ForScope(config.ScopeMerged)
	loader := internalcue.NewLoader()
	return loader.Load(dirs)
}

// runAutoSetup runs the auto-setup flow.
func runAutoSetup(stdout, stderr io.Writer, stdin io.Reader) error {
	isTTY := isTerminal(stdin)

	autoSetup := orchestration.NewAutoSetup(stdout, stderr, stdin, isTTY)
	ctx := context.Background()

	_, err := autoSetup.Run(ctx)
	if err != nil {
		return fmt.Errorf("auto-setup failed: %w", err)
	}

	fmt.Fprintln(stdout)
	return nil
}
