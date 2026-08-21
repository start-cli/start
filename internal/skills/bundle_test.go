package skills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLeaf(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"workflows/one-by-one": "one-by-one",
		"one-by-one":           "one-by-one",
		"a/b/c":                "c",
		"":                     "",
	}
	for in, want := range cases {
		if got := Leaf(in); got != want {
			t.Errorf("Leaf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDestDirRejectsUnsafeLeaves(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "skills")
	ok, err := DestDir(root, "workflows/one-by-one")
	if err != nil {
		t.Fatalf("valid name: %v", err)
	}
	if ok != filepath.Join(root, "one-by-one") {
		t.Errorf("DestDir = %q, want %s/one-by-one", ok, root)
	}

	for _, name := range []string{"..", "foo/..", ".", "", "foo/."} {
		if dest, err := DestDir(root, name); err == nil {
			t.Errorf("DestDir(%q) = %q, want error", name, dest)
		}
	}
}

func TestMaterialisableFilesOmitsCueModAndSkillCue(t *testing.T) {
	t.Parallel()
	src := writeBundle(t)
	got, err := MaterialisableFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SKILL.md", "references/notes.md", "scripts/run.sh"}
	if !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestMaterialiseOverwritesAndDropsLeftovers(t *testing.T) {
	t.Parallel()
	src := writeBundle(t)
	dest := filepath.Join(t.TempDir(), "one-by-one")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dest, "old.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Materialise(src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("leftover old.txt should be gone")
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts/run.sh")); err != nil {
		t.Fatalf("scripts/run.sh missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skill.cue")); !os.IsNotExist(err) {
		t.Fatal("skill.cue should not be copied")
	}
	if _, err := os.Stat(filepath.Join(dest, "cue.mod")); !os.IsNotExist(err) {
		t.Fatal("cue.mod should not be copied")
	}

	// Source tree is untouched.
	if _, err := os.Stat(filepath.Join(src, "skill.cue")); err != nil {
		t.Fatal("source skill.cue should remain")
	}
}

func TestMaterialiseDestFilesAreWritable(t *testing.T) {
	t.Parallel()
	src := writeBundle(t)
	if err := os.Chmod(filepath.Join(src, "SKILL.md"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "scripts/run.sh"), 0o555); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "one-by-one")
	if err := Materialise(src, dest); err != nil {
		t.Fatal(err)
	}

	skill, err := os.Stat(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Mode().Perm()&0o200 == 0 {
		t.Errorf("dest SKILL.md perm = %o, want owner-writable", skill.Mode().Perm())
	}

	script, err := os.Stat(filepath.Join(dest, "scripts/run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if script.Mode().Perm()&0o200 == 0 {
		t.Errorf("dest run.sh perm = %o, want owner-writable", script.Mode().Perm())
	}
	if script.Mode().Perm()&0o100 == 0 {
		t.Errorf("dest run.sh perm = %o, want executable", script.Mode().Perm())
	}
}

func writeBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"SKILL.md":            "---\nname: one-by-one\ndescription: walk findings\n---\nBody.\n",
		"scripts/run.sh":      "#!/bin/sh\necho hi\n",
		"references/notes.md": "# notes\n",
		"skill.cue":           "package onebyone\n",
		"cue.mod/module.cue":  "module: \"github.com/example/skill@v1\"\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
