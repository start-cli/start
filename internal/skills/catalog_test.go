package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/p3bot/agentdex"
	"github.com/p3bot/start/internal/fault"
)

func TestResolveRootsDetectedAndNamed(t *testing.T) {
	home := t.TempDir()
	cat := openTestCatalog(t, home, map[string]bool{"claude-code": true, "agy": true})
	ctx := context.Background()

	detected, err := cat.ResolveRoots(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Both agents share ~/.agents/skills as Primary, so dests dedupe to one.
	if len(detected) != 1 {
		t.Fatalf("detected dests = %d, want 1 (shared primary): %+v", len(detected), detected)
	}

	named, err := cat.ResolveRoots(ctx, []string{"claude-code", "agy"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 2 {
		t.Fatalf("named dests = %d, want 2: %+v", len(named), named)
	}
	native := filepath.Join(home, ".claude", "skills")
	foundNative := false
	for _, d := range named {
		if d.Root == native {
			foundNative = true
		}
	}
	if !foundNative {
		t.Errorf("named claude-code should use native %s: %+v", native, named)
	}
}

func TestResolveRootsUnknownAndNotCapable(t *testing.T) {
	home := t.TempDir()
	cat := openTestCatalog(t, home, nil)
	ctx := context.Background()

	_, err := cat.ResolveRoots(ctx, []string{"claude/interactive"}, false)
	if err == nil || !errors.Is(err, fault.ErrUsage) {
		t.Fatalf("start library name should be unknown agentdex id, got %v", err)
	}

	_, err = cat.ResolveRoots(ctx, []string{"noskills"}, false)
	if err == nil || !strings.Contains(err.Error(), "not skill-capable") {
		t.Fatalf("noskills: %v", err)
	}

	_, err = cat.ResolveRoots(ctx, []string{"alt-only"}, false)
	if err == nil || !strings.Contains(err.Error(), "no writable skills path") {
		t.Fatalf("alt-only named: %v", err)
	}
}

func TestResolveRootsSkipsOtherScopeAgent(t *testing.T) {
	home := t.TempDir()
	cat := openTestCatalog(t, home, map[string]bool{"claude-code": true, "local-only": true})
	ctx := context.Background()

	dests, err := cat.ResolveRoots(ctx, nil, false)
	if err != nil {
		t.Fatalf("global install with local-only sibling: %v", err)
	}
	if len(dests) != 1 {
		t.Fatalf("global dests = %d, want 1 (claude-code only): %+v", len(dests), dests)
	}

	_, err = cat.ResolveRoots(ctx, []string{"local-only"}, false)
	if err == nil || !strings.Contains(err.Error(), "not skill-capable") {
		t.Fatalf("named local-only in global scope: %v", err)
	}

	local, err := cat.ResolveRoots(ctx, []string{"local-only"}, true)
	if err != nil {
		t.Fatalf("named local-only in local scope: %v", err)
	}
	if len(local) != 1 {
		t.Fatalf("local dests = %d, want 1: %+v", len(local), local)
	}
}

func TestResolveRootsEmptyDetected(t *testing.T) {
	home := t.TempDir()
	cat := openTestCatalog(t, home, nil)
	_, err := cat.ResolveRoots(context.Background(), nil, false)
	if err == nil || !errors.Is(err, fault.ErrUsage) {
		t.Fatalf("empty detected: %v", err)
	}
}

func TestListRootsEmptyDetected(t *testing.T) {
	home := t.TempDir()
	cat := openTestCatalog(t, home, nil)
	dests, err := cat.ListRoots(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("list empty detected: %v", err)
	}
	if len(dests) != 0 {
		t.Fatalf("list empty detected dests = %+v, want none", dests)
	}
}

func TestMapCatalogErr(t *testing.T) {
	t.Parallel()
	err := mapCatalogErr(agentdex.ErrCatalogUnavailable)
	if err == nil || !strings.Contains(err.Error(), "start get skills:") {
		t.Fatalf("guidance missing: %v", err)
	}
}

func openTestCatalog(t *testing.T, home string, found map[string]bool) *Catalog {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	catalogDir := filepath.Join(filepath.Dir(file), "testdata", "catalog")
	bins := map[string]string{}
	for id := range found {
		p := filepath.Join(home, "bin-"+id)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		bins[id] = p
	}
	cat, err := Open(home,
		agentdex.WithCatalogDir(catalogDir),
		agentdex.WithLookPath(func(string) (string, error) { return "", os.ErrNotExist }),
		agentdex.WithBinPaths(bins),
		agentdex.WithEnvLookup(func(k string) (string, bool) {
			if k == "HOME" {
				return home, true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}
