package cli

import (
	"fmt"
	"io"

	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/temp"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

// addTaskCommand adds the task command to the parent command.
func addTaskCommand(parent *cobra.Command) {
	taskCmd := &cobra.Command{
		Use:     "task [name] [instructions ...]",
		Aliases: []string{"tasks"},
		GroupID: "workflow",
		Short:   "List or run a predefined task",
		Long: `List configured tasks or run one with optional instructions.

Without arguments, lists all tasks from global and local configuration.
With a name, searches for and runs the matching task.

The name can be a config task name, a "tasks:name" fully-qualified address
(the prefix must be "tasks"), a file path (starting with ./, /, ~, or ~/), or an
http(s) URL. A path or URL is read directly. Tasks are reusable workflows defined
in configuration.

The first argument is the task name; every argument after it is an instruction
segment. Each segment is independently treated as inline text, a file path
(starting with ./, /, ~, or ~/), or an http(s) URL; paths and URLs are read and
inline text is used verbatim. Resolved segments are joined with exactly one blank
line between them.

Instructions fill the task template's {{.instructions}} placeholder. When the
task has no {{.instructions}} placeholder, the instructions are appended after
the rendered body with one blank line between them.

If only a name is given and stdin is piped, the piped content is used as the
instructions.`,
		Args: cobra.ArbitraryArgs,
		RunE: runTask,
	}
	parent.AddCommand(taskCmd)
}

// runTask executes the task command.
func runTask(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}
	if len(args) == 0 {
		if err := runConfigTaskList(cmd, args); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nRun %s to search and run a task.\n", tui.Annotate("start task <name>"))
		fmt.Fprintf(cmd.OutOrStdout(), "Run %s to search all configuration and modules.\n", tui.Annotate("start search <name>"))
		return nil
	}

	taskName := args[0]
	instructions := ""
	if len(args) > 1 {
		composed, err := orchestration.ComposeSegments(args[1:], "instructions")
		if err != nil {
			return err
		}
		instructions = composed
	} else {
		// Accept piped stdin as instructions. An instruction positional wins;
		// empty pipes are accepted (templates may not require {{.instructions}}).
		pipedText, piped, err := readPipedStdin(cmd.InOrStdin())
		if err != nil {
			return err
		}
		if piped {
			instructions = pipedText
		}
	}

	flags := getFlags(cmd)
	return executeTask(cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin(), flags, taskName, instructions)
}

// executeTask handles task execution.
func executeTask(stdout, stderr io.Writer, stdin io.Reader, flags *Flags, taskName, instructions string) error {
	cfg, workingDir, err := loadExecutionConfig(stdout, stderr, stdin, flags)
	if err != nil {
		return err
	}

	agentName, err := launchAgentName(flags, cfg.Value)
	if err != nil {
		return err
	}

	r := newResolver(cfg, flags, stdout, stderr, stdin)

	// Decide index liveness once, up front, over every known surface. The
	// task-declared role is late-bound — its name lives in the task content —
	// so it is excluded here and carries its own targeted liveness check
	// (ensureTaskRoleLive) after the task resolves.
	surfaces := append(baseSurfaces(flags, agentName), pendingSurface{taskName, singleCategoryScope("tasks", "task", false)})
	r.wantLive = r.computeWantLive(surfaces)

	if agentName != "" {
		agentName, err = r.resolveAgent(agentName)
		if err != nil {
			return err
		}
	}

	var roleName string
	if !flags.NoRole {
		roleName = flags.Role
		if roleName != "" {
			roleName, err = r.resolveRole(roleName)
			if err != nil {
				return err
			}
		}
	}

	contextTags := flags.Context
	if len(contextTags) > 0 {
		contextTags, err = r.resolveContexts(contextTags)
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

	debugf(stderr, flags, dbgTask, "Searching for task %q", taskName)

	var taskResult orchestration.ProcessResult
	var resolvedName string
	if orchestration.IsLocator(taskName) {
		debugf(stderr, flags, dbgTask, "Detected locator, reading content")
		content, err := orchestration.ReadLocator(taskName)
		if err != nil {
			return fmt.Errorf("reading task %q: %w", taskName, err)
		}
		// Process through the template processor for {{.instructions}} support.
		taskResult, err = env.Composer.ProcessContent(content, instructions)
		if err != nil {
			return fmt.Errorf("processing task file: %w", err)
		}
		taskResult.FileRead = true
		resolvedName = taskName // Display the locator as the task name.
	} else {
		// Resolve the task name through the unified engine: file paths are
		// handled above, so a path never reaches here. A registry-only match is
		// installed by the resolver, which sets r.didInstall.
		outcome, err := r.resolve(taskName, singleCategoryScope("tasks", "task", false))
		if err != nil {
			return err
		}
		resolvedName = outcome.match.Name
		debugf(stderr, flags, dbgTask, "Resolved to %q", resolvedName)

		if r.cfgStale {
			debugf(stderr, flags, dbgTask, "Reloading config after registry install")
			env, err = r.reloadEnvFromResolver(workingDir, agentName, flags, stdout, stderr, stdin)
			if err != nil {
				return err
			}
		}

		taskResult, err = env.Composer.ResolveTask(env.Cfg.Value, resolvedName, instructions)
		if err != nil {
			return fmt.Errorf("resolving task: %w", err)
		}

		// A task may declare its own role; resolve it through the same match
		// rule as --role, auto-installing a registry-only role and reloading.
		if !flags.NoRole && roleName == "" {
			if declared := orchestration.GetTaskRole(env.Cfg.Value, resolvedName); declared != "" {
				// Late liveness check for the one surface the up-front union could
				// not see; the declared role's name only became known here.
				r.ensureTaskRoleLive(declared)
				roleName, err = r.resolveRole(declared)
				if err != nil {
					return err
				}
				if r.cfgStale {
					env, err = r.reloadEnvFromResolver(workingDir, agentName, flags, stdout, stderr, stdin)
					if err != nil {
						return err
					}
				}
				debugf(stderr, flags, dbgRole, "Selected %q (from task)", roleName)
			}
		}
	}

	if taskResult.CommandExecuted {
		debugf(stderr, flags, dbgTask, "UTD source: command (executed)")
	} else if taskResult.FileRead {
		debugf(stderr, flags, dbgTask, "UTD source: file")
	} else {
		debugf(stderr, flags, dbgTask, "UTD source: prompt")
	}

	if instructions != "" {
		debugf(stderr, flags, dbgTask, "Instructions: %s", instructions)
	}

	if flags.Role != "" {
		debugf(stderr, flags, dbgRole, "Selected %q (--role flag)", flags.Role)
	}

	// Tasks load required contexts only, unless --context none suppresses them.
	selection := orchestration.ContextSelection{
		IncludeRequired: true,
		IncludeDefaults: false,
		Tags:            contextTags,
	}
	if flags.NoImplicitContexts {
		// SuppressImplicit records the opt-out so Compose stamps a skip rather
		// than none when the resulting context list is empty.
		selection.IncludeRequired = false
		selection.SuppressImplicit = true
	}

	debugf(stderr, flags, dbgContext, "Selection: required=%t, defaults=%t, tags=%v",
		selection.IncludeRequired, selection.IncludeDefaults, selection.Tags)

	var composeResult orchestration.ComposeResult
	var composeErr error
	if flags.NoRole {
		debugf(stderr, flags, dbgRole, "Skipping role (--role none)")
		composeResult, composeErr = env.Composer.Compose(env.Cfg.Value, selection, taskResult.Content)
		composeResult.RoleOutcome = roleSkipOutcome()
	} else {
		composeResult, composeErr = env.Composer.ComposeWithRole(env.Cfg.Value, selection, roleName, taskResult.Content)
	}
	if composeErr != nil {
		// Show role resolutions before returning the error.
		if !flags.Quiet && len(composeResult.RoleResolutions) > 0 {
			printComposeError(stdout, env.Agent, composeResult)
		}
		return fmt.Errorf("composing prompt: %w", composeErr)
	}

	for _, ctx := range composeResult.Contexts {
		debugf(stderr, flags, dbgContext, "Including %q", ctx.Name)
	}
	debugf(stderr, flags, dbgCompose, "Role: %d bytes", len(composeResult.Role))
	debugf(stderr, flags, dbgCompose, "Prompt: %d bytes (%d contexts)", len(composeResult.Prompt), len(composeResult.Contexts))

	printWarnings(flags, stderr, taskResult.Warnings)
	printWarnings(flags, stderr, composeResult.Warnings)

	model, modelSource := resolveModel(resolvedModel, env.Agent.DefaultModel)
	if model != "" {
		debugf(stderr, flags, dbgTask, "Model: %s (%s)", model, modelSource)
	} else {
		debugf(stderr, flags, dbgTask, "Model: agent default (none specified)")
	}

	execConfig := orchestration.ExecuteConfig{
		Agent:      env.Agent,
		Model:      resolvedModel,
		Role:       composeResult.Role,
		RoleFile:   composeResult.RoleFile,
		Prompt:     composeResult.Prompt,
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
		return executeTaskDryRun(stdout, cmdStr, execConfig, composeResult, env.Agent, model, modelSource, resolvedName, instructions)
	}

	if !flags.Quiet {
		printTaskExecutionInfo(stdout, env.Agent, model, modelSource, composeResult, resolvedName, instructions, taskResult)
	}

	debugf(stderr, flags, dbgExec, "Executing agent (process replacement)")
	// Replaces the current process; command already validated.
	return env.Executor.ExecuteCommand(cmdStr, execConfig)
}

// executeTaskDryRun handles --dry-run mode for tasks. cmdStr is the pre-built,
// pre-validated command string from the caller.
func executeTaskDryRun(w io.Writer, cmdStr string, cfg orchestration.ExecuteConfig, result orchestration.ComposeResult, agent orchestration.Agent, model, modelSource, taskName, instructions string) error {
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

	printTaskDryRunSummary(w, agent, model, modelSource, result, dir, taskName, instructions)

	return nil
}

// printTaskExecutionInfo prints the task execution summary.
func printTaskExecutionInfo(w io.Writer, agent orchestration.Agent, model, modelSource string, result orchestration.ComposeResult, taskName, instructions string, taskResult orchestration.ProcessResult) {
	printHeader(w, fmt.Sprintf("Starting Task: %s", taskName))
	printSeparator(w)

	printAgentModel(w, agent, model, modelSource)
	printContextTable(w, result.Contexts, result.ContextOutcome, result.Selection)
	printRoleTable(w, result.RoleOutcome, result.RoleResolutions)

	if taskResult.CommandExecuted {
		fmt.Fprintln(w, "Command: executed")
	}

	if instructions != "" {
		fmt.Fprintf(w, "Instructions:\n%s\n", instructions)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Starting %s - awaiting response...\n", agent.Name)
}

// printTaskDryRunSummary prints the task dry-run summary.
func printTaskDryRunSummary(w io.Writer, agent orchestration.Agent, model, modelSource string, result orchestration.ComposeResult, dir, taskName, instructions string) {
	printHeader(w, fmt.Sprintf("Dry Run - Task: %s", taskName))
	printSeparator(w)

	printAgentModel(w, agent, model, modelSource)
	printContextTable(w, result.Contexts, result.ContextOutcome, result.Selection)
	printRoleTable(w, result.RoleOutcome, result.RoleResolutions)

	if instructions != "" {
		fmt.Fprintf(w, "Instructions:\n%s\n", instructions)
		fmt.Fprintln(w)
	}

	if result.Role != "" {
		printContentPreview(w, "Role", tui.ColorRoles, result.Role, 5)
		fmt.Fprintln(w)
	}

	if result.Prompt != "" {
		printContentPreview(w, "Prompt", tui.ColorPrompts, result.Prompt, 5)
		fmt.Fprintln(w)
	}

	tui.ColorDim.Fprint(w, "Files:")
	fmt.Fprintf(w, " %s/\n", dir)
	fmt.Fprintln(w, "  role.md")
	fmt.Fprintln(w, "  prompt.md")
	fmt.Fprintln(w, "  command.txt")
}

// reloadEnvFromResolver refreshes the resolver's config from disk after a
// registry install and rebuilds the execution environment from it, so both the
// resolver and the composer see the newly installed module.
func (r *resolver) reloadEnvFromResolver(workingDir, agentName string, flags *Flags, stdout, stderr io.Writer, stdin io.Reader) (*ExecutionEnv, error) {
	if err := r.reloadConfig(workingDir); err != nil {
		return nil, err
	}
	return buildExecutionEnv(r.cfg, workingDir, agentName, flags, stdout, stderr, stdin)
}
