package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPresentDests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	present := filepath.Join(root, "present")
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(filepath.Join(present, "one-by-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(present, "one-by-one", "SKILL.md"), []byte("---\nname: one-by-one\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(empty, "one-by-one"), 0o755); err != nil {
		t.Fatal(err)
	}

	roots := []Dest{{Root: present}, {Root: empty}, {Root: filepath.Join(root, "missing")}}
	got, err := PresentDests(roots, "workflows/one-by-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Join(present, "one-by-one") {
		t.Fatalf("PresentDests = %v, want the present dest only", got)
	}
	got, err = PresentDests(roots, "other/unknown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown leaf should not be present: %v", got)
	}
}

func TestPresentDestsStatError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dest := filepath.Join(root, "one-by-one")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err == nil {
		t.Skip("stat succeeds without directory traverse permission")
	}
	_, err := PresentDests([]Dest{{Root: root}}, "workflows/one-by-one")
	if err == nil {
		t.Fatal("permission failure must not look like absence")
	}
}
