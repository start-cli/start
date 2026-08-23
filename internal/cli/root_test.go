package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestIsSilentError verifies silence detection walks the error chain, so a
// silenced error keeps its silence even when wrapped further up.
func TestIsSilentError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"silenced directly", silenced(errors.New("boom")), true},
		{"silenced then wrapped", fmt.Errorf("context: %w", silenced(errors.New("boom"))), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSilentError(tt.err); got != tt.want {
				t.Errorf("IsSilentError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestExecute_Help(t *testing.T) {
	t.Parallel()
	buf := new(bytes.Buffer)
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Execute(--help) error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected help output, got empty string")
	}
	if !strings.Contains(output, "start") {
		t.Error("Expected help output to contain 'start'")
	}
}

func TestExecute_Version(t *testing.T) {
	t.Parallel()
	buf := new(bytes.Buffer)
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Execute(--version) error = %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "start version") {
		t.Errorf("Expected 'start version' in output, got: %s", output)
	}

	if !strings.Contains(output, "https://github.com/p3bot/start") {
		t.Errorf("Expected repository URL in output, got: %s", output)
	}

	if !strings.Contains(output, "/issues/new") {
		t.Errorf("Expected issues URL in output, got: %s", output)
	}
}

// TestHelpArgLeafCommands verifies "help" as a positional arg works like --help
// on leaf commands using noArgsOrHelp.
func TestHelpArgLeafCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string // expected substring in help output
	}{
		{
			name: "library help",
			args: []string{"library", "help"},
			want: "module library",
		},
		{
			name: "list help",
			args: []string{"list", "help"},
			want: "installed",
		},
		{
			name: "doctor validate help",
			args: []string{"doctor", "validate", "help"},
			want: "named in the library index",
		},
		{
			name: "completion bash help",
			args: []string{"completion", "bash", "help"},
			want: "bash",
		},
		{
			name: "completion zsh help",
			args: []string{"completion", "zsh", "help"},
			want: "zsh",
		},
		{
			name: "completion fish help",
			args: []string{"completion", "fish", "help"},
			want: "fish",
		},
		{
			name: "config add help",
			args: []string{"config", "add", "help"},
			want: "interactively",
		},
		{
			name: "config edit help",
			args: []string{"config", "edit", "help"},
			want: "Search by name",
		},
		{
			name: "config remove help",
			args: []string{"config", "remove", "help"},
			want: "Remove an installed module",
		},
		{
			name: "config list help",
			args: []string{"config", "list", "help"},
			want: "category",
		},
		{
			name: "config get help",
			args: []string{"config", "get", "help"},
			want: "raw stored",
		},
		{
			name: "config order help",
			args: []string{"config", "order", "help"},
			want: "Reorder",
		},
		{
			name: "config export help",
			args: []string{"config", "export", "help"},
			want: "Output raw CUE",
		},
		{
			name: "config open help",
			args: []string{"config", "open", "help"},
			want: "EDITOR",
		},
		{
			name: "config settings help",
			args: []string{"config", "settings", "help"},
			want: "settings",
		},
		{
			name: "search help",
			args: []string{"search", "help"},
			want: "registry",
		},
		{
			name: "task help",
			args: []string{"task", "help"},
			want: "List configured tasks",
		},
		{
			name: "doctor help",
			args: []string{"doctor", "help"},
			want: "Check start installation",
		},
		{
			name: "install help",
			args: []string{"install", "help"},
			want: "Install",
		},
		{
			name: "update help",
			args: []string{"update", "help"},
			want: "Update installed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			buf := new(bytes.Buffer)
			cmd := NewRootCmd()
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err != nil {
				t.Fatalf("%v: unexpected error: %v", tt.args, err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("%v: expected %q in output, got:\n%s", tt.args, tt.want, buf.String())
			}
		})
	}
}

// TestNoArgsOrHelpRejectsInvalidArgs verifies noArgsOrHelp rejects positional
// args other than a lone "help".
func TestNoArgsOrHelpRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "unknown positional arg",
			args: []string{"doctor", "validate", "unexpected"},
		},
		{
			name: "help plus extra arg",
			args: []string{"doctor", "validate", "help", "extra"},
		},
		{
			name: "multiple unknown args",
			args: []string{"doctor", "validate", "foo", "bar"},
		},
		{
			name: "doctor with unknown arg",
			args: []string{"doctor", "bogus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			buf := new(bytes.Buffer)
			cmd := NewRootCmd()
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err == nil {
				t.Errorf("%v: expected error for invalid args, got nil", tt.args)
			}
		})
	}
}

func TestDebugImpliesVerbose(t *testing.T) {
	t.Parallel()
	var capturedFlags *Flags
	cmd := NewRootCmd()
	// Add a probe subcommand so PersistentPreRunE runs and we can inspect flags.
	probe := &cobra.Command{
		Use:   "probe",
		Short: "test probe",
		RunE: func(cmd *cobra.Command, args []string) error {
			capturedFlags = getFlags(cmd)
			return nil
		},
	}
	cmd.AddCommand(probe)
	cmd.SetArgs([]string{"--debug", "probe"})
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if capturedFlags == nil {
		t.Fatal("probe RunE was not called; PersistentPreRunE did not run")
	}
	if !capturedFlags.Verbose {
		t.Error("--debug should set Verbose=true, but Verbose was false")
	}
}
