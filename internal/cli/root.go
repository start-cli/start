package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/fatih/color"
	"github.com/p3bot/start/internal/registry"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// IsSilentError returns true if the error should not be printed to stderr.
// Walks the chain with errors.As to stay consistent with ExitCodeFromError,
// which main.go pairs it with.
func IsSilentError(err error) bool {
	type silent interface {
		Silent() bool
	}
	var s silent
	if errors.As(err, &s) {
		return s.Silent()
	}
	return false
}

// silenced wraps err so main.go suppresses its own "Error:" stderr line — the
// command has already reported the condition — while the exit-code mapper still
// derives the process code from the wrapped chain.
func silenced(err error) error {
	if err == nil {
		return nil
	}
	return silentErr{err: err}
}

type silentErr struct{ err error }

func (e silentErr) Error() string { return e.err.Error() }
func (e silentErr) Unwrap() error { return e.err }
func (e silentErr) Silent() bool  { return true }

// Build-time variables set via ldflags.
var (
	cliVersion = "dev"
	commit     = "unknown"
	buildDate  = "unknown"
	repoURL    = "https://github.com/p3bot/start"
)

var versionTemplate = fmt.Sprintf(`start version %s
%s
%s/issues/new
`, cliVersion, repoURL, repoURL)

// NewRootCmd creates a new root command instance with all subcommands attached.
// Each instance owns its Flags so tests stay isolated and can run in parallel.
func NewRootCmd() *cobra.Command {
	flags := &Flags{}

	cmd := &cobra.Command{
		Use:   "start",
		Short: "AI agent CLI orchestrator",
		Long: `
start
https://github.com/p3bot/start

A command-line orchestrator for AI agents built on CUE.
Manages prompt composition, context injection, and workflow automation.

Examples:
  start                              Launch agent with default role and contexts
  start task review/pre-commit       Run a predefined task
  start task https://x.test/t.md     Run a predefined task from a URL
  start --role go-expert             Launch with a specific role
  start --role https://x.test/r.md   Launch with a role fetched from a URL
  echo "summarise this" | start      Send piped text as a one-shot prompt
  echo "..." | start task review     Pipe instructions to a task
  start doctor                       Check installation and configuration

  `,
		Version: cliVersion,
		// SilenceUsage suppresses usage on RunE errors; flag/arg parse errors still show it.
		SilenceUsage: true,
		// SilenceErrors: main.go prints errors itself with coloured output.
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.WithValue(cmd.Context(), flagsKey{}, flags)
			cmd.SetContext(ctx)

			// Resolve colour from --color plus env into one settled state; an
			// invalid value is a usage error (exit 2).
			decorated, err := resolveColorMode(flags.Color, isTerminalWriter(cmd.OutOrStdout()))
			if err != nil {
				return err
			}
			color.NoColor = !decorated

			// The Markdown style is settled lazily by Flags.MarkdownStyle on
			// first use, so only get/describe probe the terminal background, and
			// only when content is actually about to be styled. Settling here
			// would impose that raw-mode/stdin probe on every command.

			// Debug implies verbose
			if flags.Debug {
				flags.Verbose = true
			}

			// Normalise the none sentinel once per invocation so downstream
			// code drives off derived skip state, never per-call-site token
			// parsing. Matched before any path/name resolution so a value like
			// "none" is the sentinel, never a "./none.md"-style path.
			if isNoneToken(flags.Role) {
				flags.NoRole = true
				flags.Role = ""
			}
			if skipContext, rest := resolveContextSkip(flags.Context); skipContext {
				flags.NoImplicitContexts = true
				flags.Context = rest
			}
			return nil
		},
	}

	// Cobra copies the root context onto each subcommand before running it, so
	// every subcommand observes this provider; tests override it before Execute
	// to run offline.
	cmd.SetContext(WithProvider(context.Background(), registry.NewClient))

	cmd.SetVersionTemplate(versionTemplate)

	cmd.PersistentFlags().StringSliceVarP(&flags.Agent, "agent", "a", nil, "Override agent (launch: one library agent; skill install/describe: agentdex catalog ids)")
	cmd.PersistentFlags().StringVarP(&flags.Role, "role", "r", "", "Override role (config name, file path, or http(s) URL); 'none' skips role assignment (also: nil, off, 0)")
	cmd.PersistentFlags().StringVarP(&flags.Model, "model", "m", "", "Override model selection")
	cmd.PersistentFlags().StringSliceVarP(&flags.Context, "context", "c", nil, "Select contexts (tags, file paths, or http(s) URLs); 'none' drops auto-loaded contexts (also: nil, off, 0)")
	cmd.PersistentFlags().BoolVar(&flags.DryRun, "dry-run", false, "Preview execution without launching agent")
	cmd.PersistentFlags().BoolVarP(&flags.Quiet, "quiet", "q", false, "Suppress output")
	cmd.PersistentFlags().BoolVar(&flags.Verbose, "verbose", false, "Detailed output")
	cmd.PersistentFlags().BoolVar(&flags.Debug, "debug", false, "Debug output (implies --verbose)")
	cmd.PersistentFlags().StringVar(&flags.Color, "color", "auto", "Colour output: auto, always, never")
	cmd.PersistentFlags().BoolVarP(&flags.Local, "local", "l", false, "Target local config (./.start/) instead of global")
	cmd.PersistentFlags().BoolVar(&flags.Refresh, "refresh", false, "Bypass the 24h index cache and resolve the registry index live")

	cmd.RunE = runStart

	cmd.AddGroup(
		&cobra.Group{ID: "modules", Title: "Modules:"},
		&cobra.Group{ID: "workflow", Title: "Workflow:"},
		&cobra.Group{ID: "utilities", Title: "Utilities:"},
	)

	addDescribeCommand(cmd, flags)
	addGetCommand(cmd, flags)
	addPromptCommand(cmd)
	addTaskCommand(cmd)
	addInstallCommand(cmd)
	addUninstallCommand(cmd)
	addListCommand(cmd)
	addUpdateCommand(cmd)
	addLibraryCommand(cmd)
	addConfigCommand(cmd, flags)
	addAliasCommand(cmd)
	addSearchCommand(cmd)
	addDoctorCommand(cmd)
	addCompletionCommand(cmd)

	addHelpCommand(cmd)

	// Classify Cobra flag-parse and arg-count failures as usage errors (exit 2).
	// Unknown-command errors are produced earlier during Find and stay exit 1 to
	// match git/gh.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})
	wrapUsageArgs(cmd)

	return cmd
}

// wrapUsageArgs wraps every command's positional-arg validator so an arg-count
// failure carries the usage fault domain (exit 2). A nil Args validator never
// errors, so it is left untouched.
func wrapUsageArgs(cmd *cobra.Command) {
	if cmd.Args != nil {
		inner := cmd.Args
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := inner(c, args); err != nil {
				return usageError(err)
			}
			return nil
		}
	}
	for _, sub := range cmd.Commands() {
		wrapUsageArgs(sub)
	}
}

// Execute runs the root command.
func Execute() error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("start does not support Windows")
	}
	return runRoot(NewRootCmd(), os.Args[1:])
}

// checkHelpArg reports whether the first arg requests help and shows it if so.
// Call at the top of RunE on commands using noArgsOrHelp. It recognises a
// leading "help", "-h", or "--help": the flag forms matter only for commands
// with DisableFlagParsing (alias set), where cobra never intercepts --help and
// the token would otherwise be treated as data. For flag-parsing commands cobra
// catches --help ahead of RunE, so accepting it here is a harmless no-op.
func checkHelpArg(cmd *cobra.Command, args []string) (bool, error) {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return true, cmd.Help()
	}
	return false, nil
}

// unknownCommandError returns a formatted error for unknown subcommands.
func unknownCommandError(cmdPath, arg string) error {
	return fmt.Errorf("unknown command %q for %q\nRun '%s --help' for usage", arg, cmdPath, cmdPath)
}

// noArgsOrHelp is like cobra.NoArgs but allows a lone "help" arg, enabling
// "cmd help" as an alias for "cmd --help" on leaf commands.
func noArgsOrHelp(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && args[0] == "help" {
		return nil
	}
	return cobra.NoArgs(cmd, args)
}

// resolveColorMode collapses --color and the environment into one decoration
// decision. Precedence: NO_COLOR disables and wins even over --color=always;
// --color=never disables; --color=always forces on; --color=auto enables only
// on a TTY, with TERM=dumb disabling and FORCE_COLOR/CLICOLOR_FORCE forcing on
// off-TTY. An out-of-set value is a usage error (exit 2).
func resolveColorMode(mode string, stdoutTTY bool) (decorated bool, err error) {
	switch mode {
	case "auto", "always", "never":
	default:
		return false, usageError(fmt.Errorf("invalid --color %q: must be one of auto, always, never", mode))
	}

	// NO_COLOR is absolute: it disables colour ahead of --color=always.
	if os.Getenv("NO_COLOR") != "" {
		return false, nil
	}

	switch mode {
	case "never":
		return false, nil
	case "always":
		return true, nil
	default: // auto
		if os.Getenv("TERM") == "dumb" {
			return false, nil
		}
		force := envTruthy("FORCE_COLOR") || envTruthy("CLICOLOR_FORCE")
		return stdoutTTY || force, nil
	}
}

// settleMarkdownStyle picks the glamour style for this invocation. It probes
// the terminal background (a raw-mode OSC query) only when decorating to a TTY
// stdout, returning "dark" or "light"; it defaults to "dark" when not
// decorating, when stdout is not a TTY (the --color=always | pipe case), or
// when detection fails. lipgloss.HasDarkBackground returns dark on any error or
// non-TTY, so the default holds without extra guarding.
func settleMarkdownStyle(decorated bool) string {
	if decorated && isTerminalWriter(os.Stdout) {
		if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
			return markdownStyleDark
		}
		return markdownStyleLight
	}
	return markdownStyleDark
}

// envTruthy reports whether a boolean env var is on, matching the de facto
// FORCE_COLOR / CLICOLOR_FORCE convention (any non-empty, non-falsy value).
func envTruthy(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v != "" && v != "0" && v != "false" && v != "no"
}

// isTerminal reports whether r is connected to a terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// isTerminalWriter reports whether w is connected to a terminal. In tests the
// writer is a buffer, so --color=auto resolves to no colour unless FORCE_COLOR.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// readPipedStdin returns the full contents of stdin when it is piped (not a
// TTY). Content is raw to preserve leading whitespace and trailing newlines,
// matching file-sourced prompts. A TTY returns ("", false, nil) so callers can
// fall back to interactive input. Callers decide their own empty-stdin policy.
func readPipedStdin(stdin io.Reader) (text string, piped bool, err error) {
	if isTerminal(stdin) {
		return "", false, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", true, fmt.Errorf("reading stdin: %w", err)
	}
	return string(data), true, nil
}
