package skills

import (
	"os"
	"strings"
	"testing"
)

func TestInventoryUpsertLoadRemove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1.0.0", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	entries, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := entries["workflows/one-by-one"]
	if !ok {
		t.Fatalf("missing key: %v", entries)
	}
	if e.Origin == "" || e.Version != "v1.0.0" {
		t.Errorf("entry = %+v", e)
	}
	data, err := os.ReadFile(InventoryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "targets") || strings.Contains(string(data), "dest") {
		t.Errorf("inventory must not record dests:\n%s", data)
	}

	if err := Upsert(dir, "workflows/one-by-one", "github.com/p3bot/library/skills/workflows/one-by-one@v1.1.0", "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	entries, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries["workflows/one-by-one"].Version != "v1.1.0" {
		t.Errorf("version not updated: %+v", entries["workflows/one-by-one"])
	}

	if err := Remove(dir, "workflows/one-by-one"); err != nil {
		t.Fatal(err)
	}
	entries, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["workflows/one-by-one"]; ok {
		t.Fatal("entry should be gone")
	}
}

func TestResolveKeyExactAndLeaf(t *testing.T) {
	t.Parallel()
	entries := map[string]Entry{
		"workflows/one-by-one": {Origin: "a", Version: "v1"},
		"other/one-by-one":     {Origin: "b", Version: "v1"},
		"review/pre-commit":    {Origin: "c", Version: "v1"},
	}

	if got := ResolveKey(entries, "workflows/one-by-one"); len(got) != 1 || got[0] != "workflows/one-by-one" {
		t.Errorf("exact = %v", got)
	}
	if got := ResolveKey(entries, "pre-commit"); len(got) != 1 || got[0] != "review/pre-commit" {
		t.Errorf("unique leaf = %v", got)
	}
	got := ResolveKey(entries, "one-by-one")
	if len(got) != 2 {
		t.Errorf("ambiguous leaf = %v", got)
	}
	if got := ResolveKey(entries, "missing"); len(got) != 0 {
		t.Errorf("missing = %v", got)
	}
}

func TestConflictingKeys(t *testing.T) {
	t.Parallel()
	names := []string{"workflows/one-by-one", "other/one-by-one", "review/pre-commit"}

	got := ConflictingKeys(names, "workflows/one-by-one")
	if len(got) != 1 || got[0] != "other/one-by-one" {
		t.Errorf("same leaf = %v", got)
	}
	if got := ConflictingKeys(names, "review/pre-commit"); len(got) != 0 {
		t.Errorf("unique leaf = %v", got)
	}
	if got := ConflictingKeys(names, "workflows/ONE-BY-ONE"); len(got) != 1 || got[0] != "other/one-by-one" {
		t.Errorf("case-insensitive = %v", got)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
