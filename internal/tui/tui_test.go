package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCategoryColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  any // expected *color.Color pointer
	}{
		{"agents", ColorAgents},
		{"AGENTS", ColorAgents},
		{"agent", ColorAgents}, // singular form, as carried by selection menus
		{"roles", ColorRoles},
		{"Roles", ColorRoles},
		{"role", ColorRoles},
		{"contexts", ColorContexts},
		{"CONTEXTS", ColorContexts},
		{"context", ColorContexts},
		{"tasks", ColorTasks},
		{"Tasks", ColorTasks},
		{"task", ColorTasks},
		{"skills", ColorSkills},
		{"Skills", ColorSkills},
		{"skill", ColorSkills},
		{"settings", ColorSettings},
		{"Settings", ColorSettings},
		{"setting", ColorSettings},
		{"unknown", ColorDim},
		{"", ColorDim},
		{"prompts", ColorDim}, // not in switch, falls to default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CategoryColor(tt.input)
			if got != tt.want {
				t.Errorf("CategoryColor(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAnnotateCategory(t *testing.T) {
	t.Parallel()

	t.Run("wraps label in parentheses", func(t *testing.T) {
		result := AnnotateCategory("role")
		if !strings.Contains(result, "role") {
			t.Errorf("AnnotateCategory(%q) = %q, want to contain the label", "role", result)
		}
		if !strings.Contains(result, "(") || !strings.Contains(result, ")") {
			t.Errorf("AnnotateCategory(%q) = %q, want parentheses", "role", result)
		}
	})

	t.Run("uses category colour for known categories", func(t *testing.T) {
		// The label must be wrapped in cyan parens with the category's own
		// colour applied to the inner text, not dim. Assert the exact
		// composition so a regression to ColorDim is caught when colour is
		// enabled; under the default NoColor the strings collapse to plain
		// text and this pins the rendered structure.
		want := ColorCyan.Sprint("(") + ColorTasks.Sprint("task") + ColorCyan.Sprint(")")
		if result := AnnotateCategory("task"); result != want {
			t.Errorf("AnnotateCategory(%q) = %q, want %q", "task", result, want)
		}
	})
}

func TestAnnotate(t *testing.T) {
	t.Parallel()

	t.Run("plain string", func(t *testing.T) {
		result := Annotate("hello")
		if !strings.Contains(result, "hello") {
			t.Errorf("Annotate(%q) = %q, want to contain %q", "hello", result, "hello")
		}
		if !strings.Contains(result, "(") || !strings.Contains(result, ")") {
			t.Errorf("Annotate(%q) = %q, want parentheses", "hello", result)
		}
	})

	t.Run("formatted string", func(t *testing.T) {
		result := Annotate("count %d", 42)
		if !strings.Contains(result, "count 42") {
			t.Errorf("Annotate() = %q, want to contain %q", result, "count 42")
		}
		if !strings.Contains(result, "(") || !strings.Contains(result, ")") {
			t.Errorf("Annotate() = %q, want parentheses", result)
		}
	})

	t.Run("key=value format", func(t *testing.T) {
		result := Annotate("key=%s", "val")
		if !strings.Contains(result, "key=val") {
			t.Errorf("Annotate() = %q, want to contain %q", result, "key=val")
		}
	})
}

func TestBracket(t *testing.T) {
	t.Parallel()

	t.Run("plain string", func(t *testing.T) {
		result := Bracket("hello")
		if !strings.Contains(result, "hello") {
			t.Errorf("Bracket(%q) = %q, want to contain %q", "hello", result, "hello")
		}
		if !strings.Contains(result, "[") || !strings.Contains(result, "]") {
			t.Errorf("Bracket(%q) = %q, want square brackets", "hello", result)
		}
	})

	t.Run("formatted string", func(t *testing.T) {
		result := Bracket("count %d", 42)
		if !strings.Contains(result, "count 42") {
			t.Errorf("Bracket() = %q, want to contain %q", result, "count 42")
		}
		if !strings.Contains(result, "[") || !strings.Contains(result, "]") {
			t.Errorf("Bracket() = %q, want square brackets", result)
		}
	})

	t.Run("key=value format", func(t *testing.T) {
		result := Bracket("key=%s", "val")
		if !strings.Contains(result, "key=val") {
			t.Errorf("Bracket() = %q, want to contain %q", result, "key=val")
		}
	})
}

func TestProgress_NonTTY(t *testing.T) {
	t.Parallel()
	// bytes.Buffer is not *os.File, so NewProgress treats it as non-TTY.
	// Update and Done must be no-ops.
	buf := &bytes.Buffer{}
	p := NewProgress(buf, false)

	p.Update("loading %d%%", 50)
	p.Update("loading %d%%", 100)
	p.Done()

	if buf.Len() != 0 {
		t.Errorf("Progress should be no-op for non-TTY writer, got %q", buf.String())
	}
}

func TestProgress_Quiet(t *testing.T) {
	t.Parallel()
	// Use a real *os.File so the writer would pass the type assertion;
	// quiet=true must still suppress all output.
	f, err := os.CreateTemp(t.TempDir(), "prog")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	p := NewProgress(f, true)
	p.Update("loading %d%%", 50)
	p.Done()

	n, _ := f.Seek(0, io.SeekEnd)
	if n != 0 {
		t.Errorf("Progress should be no-op when quiet, but %d bytes were written", n)
	}
}

func TestProgress_TTY_Update(t *testing.T) {
	t.Parallel()
	// Bypass NewProgress's TTY detection by constructing the struct directly.
	// Tests are in package tui so unexported fields are accessible.
	buf := &bytes.Buffer{}
	p := &Progress{w: buf, tty: true}

	p.Update("hello world")

	out := buf.String()
	if !strings.Contains(out, "\r") {
		t.Errorf("Update() on TTY should start with carriage return, got %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("Update() on TTY should write the message, got %q", out)
	}
	if p.width != len("hello world") {
		t.Errorf("width = %d, want %d", p.width, len("hello world"))
	}
}

func TestProgress_TTY_Update_Padding(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	// Start with width 20 to force padding when a shorter message is written.
	p := &Progress{w: buf, tty: true, width: 20}

	p.Update("hi") // shorter than previous width, so output must pad to overwrite it

	out := buf.String()
	if len(out) < 20 {
		t.Errorf("Update() should pad to overwrite previous line; output len %d, want >= 20", len(out))
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("Update() should contain the message, got %q", out)
	}
}

func TestProgress_TTY_Done(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	p := &Progress{w: buf, tty: true, width: 10}

	p.Done()

	out := buf.String()
	if !strings.Contains(out, "\r") {
		t.Errorf("Done() on TTY should emit carriage return, got %q", out)
	}
	if p.width != 0 {
		t.Errorf("Done() should reset width to 0, got %d", p.width)
	}
}

func TestProgress_TTY_Done_OnEmpty(t *testing.T) {
	t.Parallel()
	// Done when width == 0 is a no-op even in TTY mode.
	buf := &bytes.Buffer{}
	p := &Progress{w: buf, tty: true, width: 0}

	p.Done()

	if buf.Len() != 0 {
		t.Errorf("Done() with width=0 should be no-op, got %q", buf.String())
	}
}

func TestProgress_NonTTY_DoneOnEmpty(t *testing.T) {
	t.Parallel()
	// Calling Done without any Update should not write anything.
	buf := &bytes.Buffer{}
	p := NewProgress(buf, false)
	p.Done()

	if buf.Len() != 0 {
		t.Errorf("Done() on empty progress wrote output: %q", buf.String())
	}
}
