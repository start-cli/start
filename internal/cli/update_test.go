package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestUpdateResultJSON tests that UpdateResult marshals with error as string and omits Error field.
func TestUpdateResultJSON(t *testing.T) {
	t.Parallel()
	results := []UpdateResult{
		{
			Module:     InstalledModule{Category: "agents", Name: "ai/claude", Scope: "global", Origin: "test"},
			OldVersion: "v0.1.0",
			NewVersion: "v0.2.0",
			Updated:    true,
		},
		{
			Module:       InstalledModule{Category: "roles", Name: "golang", Scope: "global", Origin: "test"},
			OldVersion:   "v0.1.0",
			Updated:      false,
			Error:        fmt.Errorf("network timeout"),
			ErrorMessage: "network timeout",
		},
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	output := string(data)

	if !strings.Contains(output, `"oldVersion": "v0.1.0"`) {
		t.Errorf("output missing oldVersion, got: %s", output)
	}
	if !strings.Contains(output, `"newVersion": "v0.2.0"`) {
		t.Errorf("output missing newVersion, got: %s", output)
	}
	if !strings.Contains(output, `"updated": true`) {
		t.Errorf("output missing updated=true, got: %s", output)
	}

	if !strings.Contains(output, `"error": "network timeout"`) {
		t.Errorf("output missing error string, got: %s", output)
	}

	// The Error interface field must be excluded from JSON via json:"-".
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	for _, item := range decoded {
		if _, ok := item["Error"]; ok {
			t.Errorf("Error interface field should be excluded via json:\"-\", got: %v", item)
		}
	}
}

func TestSelectUpdateTargetsSkillsCategoryOrder(t *testing.T) {
	t.Parallel()
	collected := []InstalledModule{
		{Category: "skills", Name: "a/skill", Scope: "global"},
		{Category: "skills", Name: "b/skill", Scope: "global"},
		{Category: "skills", Name: "a/skill", Scope: "local"},
		{Category: "skills", Name: "b/skill", Scope: "local"},
	}
	orig := append([]InstalledModule{}, collected...)

	got, err := selectUpdateTargets(nil, "skills", nil, collected)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, scope string }{
		{"a/skill", "global"},
		{"a/skill", "local"},
		{"b/skill", "global"},
		{"b/skill", "local"},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].name || got[i].Scope != want[i].scope {
			t.Errorf("got[%d]=%s [%s], want %s [%s]", i, got[i].Name, got[i].Scope, want[i].name, want[i].scope)
		}
	}

	unfiltered, err := selectUpdateTargets(nil, "", nil, collected)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfiltered) != len(got) {
		t.Fatalf("unfiltered len=%d, skills len=%d", len(unfiltered), len(got))
	}
	for i := range got {
		if unfiltered[i].Name != got[i].Name || unfiltered[i].Scope != got[i].Scope {
			t.Errorf("unfiltered[%d]=%s [%s], skills[%d]=%s [%s]", i, unfiltered[i].Name, unfiltered[i].Scope, i, got[i].Name, got[i].Scope)
		}
	}
	for i := range orig {
		if collected[i].Name != orig[i].Name || collected[i].Scope != orig[i].Scope {
			t.Fatalf("input mutated: %+v", collected)
		}
	}
}
