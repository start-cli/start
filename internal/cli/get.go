package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"cuelang.org/go/cue"
	"github.com/p3bot/start/internal/orchestration"
	"github.com/p3bot/start/internal/shell"
	"github.com/p3bot/start/internal/tui"
	"github.com/spf13/cobra"
)

func addGetCommand(parent *cobra.Command, flags *Flags) {
	getCmd := &cobra.Command{
		Use:     "get [name]",
		GroupID: "workflow",
		Short:   "Output module content to stdout",
		Long: `Output the resolved content of a module to stdout for piping or preview.

Searches across all categories (agents, roles, contexts, tasks, skills) and writes the
module's content to stdout. Names may be bare (e.g. "agents-md") or fully
qualified as "category:name" (e.g. "contexts:cwd/agents-md"); the category
prefix scopes the search to a single category. A file path (starting with ./, /,
~, or ~/) or an http(s) URL bypasses the search and its content is read directly.
UTD modules (roles, contexts,
tasks) are template-resolved: file contents are read, prompts are rendered,
and commands are executed. Agent modules emit the command template with static
placeholders ({{.bin}}, {{.model}}) substituted while runtime placeholders
({{.prompt}}, {{.role}}, {{.role_file}}, {{.datetime}}) are left intact. The
--model flag, when set, overrides the agent's default_model in the
{{.model}} substitution.

Source priority for UTD modules is file > prompt > command. When a UTD module
defines both file and prompt, get outputs the file. During role/task/context
rendering by 'start' or 'start task', behaviour differs: the prompt is rendered
and file contents are injected via {{.file_contents}}, command output via
{{.command_output}}. So for mixed-field modules, get's output will not match
what 'start' renders into the agent prompt — use 'start describe' to inspect the
prompt.

Stdout receives only the module content. Selection menus, registry progress,
auto-install notices, and --verbose metadata are written to stderr so the
output remains pipe-clean. On an interactive terminal, Markdown content
(rendered prompts and .md/.markdown file bodies) is styled; piped, redirected,
or --color=never output stays raw and byte-identical.

Use --global to restrict resolution to the global config (~/.config/start/) or
--local to restrict to the local config (./.start/). These flags are mutually
exclusive; omitting both resolves against the merged configuration.

Auto-installed modules always land in global config; the post-install lookup
widens to merged scope so a --local invocation can still see the new module.
To inspect strictly within --local, ensure the module is already installed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runGet,
	}

	getCmd.Flags().BoolVar(&flags.Global, "global", false, "Restrict to global config only")
	parent.AddCommand(getCmd)
}

func runGet(cmd *cobra.Command, args []string) error {
	if shown, err := checkHelpArg(cmd, args); shown || err != nil {
		return err
	}

	if err := validateScopeFlags(getFlags(cmd)); err != nil {
		return err
	}

	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()
	stdin := cmd.InOrStdin()

	query, err := getResolveQuery(args, stderr, stdin)
	if err != nil {
		return err
	}
	if query == "" {
		return nil
	}

	flags := getFlags(cmd)
	scope := scopeFromFlags(flags)
	cfg, err := loadConfigOrEmpty(scope)
	if err != nil {
		return err
	}

	// stderr in the stdout slot so fetch progress, auto-install notices, and
	// selection menus do not corrupt the piped content on stdout.
	r := newResolver(cfg, flags, stderr, stderr, stdin)
	attachResolverSource(r, cmd)
	outcome, err := r.resolveCrossNoInstall(query)
	if err != nil {
		return err
	}
	if outcome.locator != "" {
		return outputFileBody(stdout, flags, outcome.locator)
	}
	match := outcome.match

	if match.Category == "skills" {
		return getSkill(stdout, stderr, flags, r.client, enrichSkillMatch(r, match))
	}

	if _, err := r.finalize(match); err != nil {
		return err
	}

	// Refresh config after an auto-install so the new module's CUE value is
	// visible. reloadConfig always uses merged scope: autoInstall writes to
	// global, so merged is the smallest scope guaranteed to see the module for
	// any original --local/--global; --local widening is signalled via
	// notifyScopeWidenedIfLocal.
	if r.cfgStale {
		workingDir, wdErr := os.Getwd()
		if wdErr != nil {
			return fmt.Errorf("getting working directory: %w", wdErr)
		}
		if err := r.reloadConfig(workingDir); err != nil {
			return err
		}
		cfg = r.cfg
		notifyScopeWidenedIfLocal(stderr, flags, r.didInstall)
	}

	cat := describeCategoryFor(match.Category)
	if cat == nil {
		return fmt.Errorf("unknown category %q", match.Category)
	}

	items := cfg.Value.LookupPath(cue.ParsePath(cat.key))
	item := items.LookupPath(cue.MakePath(cue.Str(match.Name)))
	if !item.Exists() {
		return notFoundError(fmt.Errorf("%s %q not found", strings.ToLower(cat.itemType), match.Name))
	}

	if cat.itemType == "Agent" {
		return getAgent(stdout, stderr, flags, r, match.Name, item)
	}
	return getUTD(stdout, stderr, flags, match.Name, cat.itemType, item)
}

// getResolveQuery returns the module query, prompting interactively when no
// argument was supplied. Prompts and warnings go to stderr to keep stdout
// reserved for module content.
func getResolveQuery(args []string, stderr io.Writer, stdin io.Reader) (string, error) {
	if len(args) == 0 {
		if !isTerminal(stdin) {
			return "", usageError(fmt.Errorf("name required in non-interactive mode"))
		}
		return promptSearchQuery(stderr, stdin, 3)
	}

	// The three-character floor lives in the resolver, exempting the exact tier;
	// the raw argument passes straight through.
	return args[0], nil
}

// outputFileBody reads a locator supplied to get/describe (a local path or an
// http(s) URL) and writes its content, styling Markdown on a terminal exactly as
// a UTD file body is styled. Piped, redirected, or --color=never output stays
// raw and byte-identical.
func outputFileBody(w io.Writer, flags *Flags, path string) error {
	content, err := orchestration.ReadLocator(path)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}
	body := ensureTrailingNewline(content)
	if shouldStyleMarkdown(sourceFile, path) {
		return tui.RenderMarkdown(w, body, flags.MarkdownStyle())
	}
	fmt.Fprint(w, body)
	return nil
}

// getAgent writes the agent's command template (with {{.bin}} and {{.model}}
// resolved) to stdout, leaving runtime placeholders intact. --model is
// resolved via resolveModelName to match `start`'s rendering of the flag.
func getAgent(stdout, stderr io.Writer, flags *Flags, r *resolver, name string, item cue.Value) error {
	cmdField := item.LookupPath(cue.ParsePath("command"))
	command := ""
	if cmdField.Exists() {
		command, _ = cmdField.String()
	}
	if command == "" {
		return fmt.Errorf("agent %q has no command (or empty command field)", name)
	}

	if flags.Verbose {
		printGetVerbose(stderr, "Agent", name, item, "", "", false)
	}

	modelOverride := ""
	if flags.Model != "" {
		agent, err := orchestration.ExtractAgent(r.cfg.Value, name)
		if err != nil {
			return fmt.Errorf("loading agent %q for --model resolution: %w", name, err)
		}
		modelOverride = r.resolveModelName(flags.Model, agent)
	}

	rendered := partialFillAgentCommand(command, item, modelOverride)
	fmt.Fprint(stdout, ensureTrailingNewline(rendered))
	return nil
}

// getUTD resolves a UTD module and writes its content to stdout. Source
// priority is file > prompt > command; the trim block below flips
// TemplateProcessor's inverse intrinsic priority by clearing higher-priority
// sources before Process runs. Shell and Timeout pass through untouched.
func getUTD(stdout, stderr io.Writer, flags *Flags, name, itemType string, item cue.Value) error {
	fields := orchestration.ExtractUTDFields(item)
	if !orchestration.IsUTDValid(fields) {
		return fmt.Errorf("module %q has no content fields (expected one of: file, prompt, command)", name)
	}

	resolvedFile := ""
	fromModuleCache := false
	if fields.File != "" {
		if strings.HasPrefix(fields.File, "@module/") {
			fromModuleCache = true
			origin := orchestration.ExtractOrigin(item)
			if origin == "" {
				return fmt.Errorf("module %q has @module/ file path but no origin field", name)
			}
			resolved, err := orchestration.ResolveModulePath(fields.File, origin)
			if err != nil {
				return fmt.Errorf("resolving module path %s: %w", fields.File, err)
			}
			fields.File = resolved
		}
		// Expand ~/ and relative paths so verbose Path:/Cache: reports the same
		// location DefaultFileReader will read from. On expansion failure, keep
		// the literal config string and log the cause under --debug.
		resolvedFile = fields.File
		if expanded, expandErr := orchestration.ExpandFilePath(fields.File); expandErr == nil {
			resolvedFile = expanded
		} else {
			debugf(stderr, flags, dbgResolve, "expanding %s: %v", fields.File, expandErr)
		}
	}

	// Process picks Prompt before File, so clearing Prompt when File is set is
	// what makes get's file > prompt priority hold. Clearing Command also
	// suppresses Process's lazy {{.command_output}} expansion so get never
	// shells out unless command is the primary source. Do not extend this trim
	// to Shell or Timeout — they apply regardless of which source wins.
	if fields.File != "" {
		fields.Prompt = ""
		fields.Command = ""
	} else if fields.Prompt != "" {
		fields.Command = ""
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Runs after the trim block so fields.Command reflects the chosen source
	// (non-empty only when command is active), before any read or shell-out.
	if flags.Verbose {
		printGetVerbose(stderr, itemType, name, item, resolvedFile, fields.Command, fromModuleCache)
	}

	fr := &orchestration.DefaultFileReader{}
	sr := shell.NewRunner()
	processor := orchestration.NewTemplateProcessor(fr, sr, workingDir)

	result, err := processor.Process(fields, "")
	if err != nil {
		return err
	}

	// Feed the trailing-newline-normalised content to both paths so the styled
	// and raw outputs share one source: when decoration is off RenderMarkdown
	// passes content through byte-for-byte, keeping get pipe-clean.
	content := ensureTrailingNewline(result.Content)
	if shouldStyleMarkdown(utdSource(fields), fields.File) {
		return tui.RenderMarkdown(stdout, content, flags.MarkdownStyle())
	}
	fmt.Fprint(stdout, content)
	return nil
}

// utdSource reports the winning UTD source from post-trim fields, mirroring the
// file > prompt > command priority the trim block in getUTD enforces.
func utdSource(fields orchestration.UTDFields) markdownSource {
	switch {
	case fields.File != "":
		return sourceFile
	case fields.Prompt != "":
		return sourcePrompt
	default:
		return sourceCommand
	}
}

// printGetVerbose writes module metadata to stderr ahead of the content.
// fromModuleCache labels the location Cache: (vs Path:) so users aren't misled
// into editing the read-only CUE module cache instead of an editable source.
func printGetVerbose(stderr io.Writer, itemType, name string, item cue.Value, resolvedFile, command string, fromModuleCache bool) {
	fmt.Fprintf(stderr, "Type: %s\n", itemType)
	fmt.Fprintf(stderr, "Name: %s\n", name)
	if origin := orchestration.ExtractOrigin(item); origin != "" {
		fmt.Fprintf(stderr, "Origin: %s\n", origin)
	}
	if resolvedFile != "" {
		label := "Path"
		if fromModuleCache {
			label = "Cache"
		}
		fmt.Fprintf(stderr, "%s: %s\n", label, resolvedFile)
	}
	if command != "" {
		fmt.Fprintf(stderr, "Command: %s\n", command)
	}
}

// ensureTrailingNewline returns s with exactly one trailing newline; empty
// strings pass through.
func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
