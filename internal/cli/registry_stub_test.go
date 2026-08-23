package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"cuelang.org/go/mod/modconfig"
	"github.com/p3bot/start/internal/registry"
)

// registryStub is an offline registry.Client for tests. It serves a canned
// in-memory index for FetchIndex consumers (search, update) and a fixture
// SourceDir for Fetch consumers (library's load-from-disk path). Lookups match
// on the module base path (version suffix stripped) so callers that resolve a
// path before fetching still hit the same entry; per-test overrides via
// SetFetch/SetResolve do not require rebuilding it.
type registryStub struct {
	idx       *registry.Index
	sourceDir string
	fetches   map[string]fetchResponse
	resolves  map[string]resolveResponse
	versions  map[string]versionsResponse

	// fetchIndexErr, when set, makes FetchIndex fail so graceful-degradation
	// paths can be exercised offline.
	fetchIndexErr error

	// providerCalls counts how many times the provider closure handed out this
	// stub. Non-zero proves the seam was consulted rather than falling back to
	// the live registry; the exact count pins each command's client-construction
	// count (e.g. doctor builds two per invocation).
	providerCalls int

	// resolvePaths records every ResolveLatestVersion the display commands make:
	// the metadata-resolve observable that distinguishes a cache-gated resolve
	// (absent on a fresh-cache hit) from a live one. providerCalls cannot tell
	// these apart — it is 1 on both paths. Resolver-path liveness is observed
	// separately, on recordingIndexSource, not through this stub.
	resolvePaths []string
}

type fetchResponse struct {
	result registry.FetchResult
	err    error
}

type resolveResponse struct {
	version string
	err     error
}

type versionsResponse struct {
	versions []string
	err      error
}

// stubVersion is the canonical version the stub resolves index/schema paths to.
const stubVersion = "v1.0.0"

// newRegistryStub serves idx for FetchIndex consumers and sourceDir (an on-disk
// index CUE fixture) for library's Fetch+LoadIndex path. The in-memory index and
// the fixture must carry the same entries; setupStartTestConfigWithRegistry
// keeps them in lockstep.
func newRegistryStub(idx *registry.Index, sourceDir string) *registryStub {
	return &registryStub{
		idx:       idx,
		sourceDir: sourceDir,
		fetches:   make(map[string]fetchResponse),
		resolves:  make(map[string]resolveResponse),
		versions:  make(map[string]versionsResponse),
	}
}

func (s *registryStub) SetFetch(path string, result registry.FetchResult, err error) {
	s.fetches[stubBasePath(path)] = fetchResponse{result: result, err: err}
}

func (s *registryStub) SetResolve(path string, version string, err error) {
	s.resolves[stubBasePath(path)] = resolveResponse{version: version, err: err}
}

func (s *registryStub) SetFetchIndexError(err error) {
	s.fetchIndexErr = err
}

func (s *registryStub) SetVersions(path string, versions []string, err error) {
	s.versions[stubBasePath(path)] = versionsResponse{versions: versions, err: err}
}

// FetchIndex returns the canned in-memory index. The returned version string
// mirrors a canonical resolved path (what the real client produces).
func (s *registryStub) FetchIndex(ctx context.Context, indexPath string) (*registry.Index, string, error) {
	if s.fetchIndexErr != nil {
		return nil, "", s.fetchIndexErr
	}
	return s.idx, stubBasePath(registry.IndexModulePath) + "@" + stubVersion, nil
}

// Fetch serves the on-disk index fixture for the index module and an error for
// everything else (so doctor's schema-validation section reaches its "Skipped"
// shape). Per-test overrides registered via SetFetch win.
func (s *registryStub) Fetch(ctx context.Context, modulePath string) (registry.FetchResult, error) {
	base := stubBasePath(modulePath)
	if resp, ok := s.fetches[base]; ok {
		return resp.result, resp.err
	}
	if base == stubBasePath(registry.IndexModulePath) {
		return registry.FetchResult{SourceDir: s.sourceDir}, nil
	}
	return registry.FetchResult{}, fmt.Errorf("registry stub: no fetch response for %q", modulePath)
}

// ModuleVersions returns a canned single version unless overridden. It exists
// for interface completeness; the offline target commands do not call it.
func (s *registryStub) ModuleVersions(ctx context.Context, modulePath string) ([]string, error) {
	if resp, ok := s.versions[stubBasePath(modulePath)]; ok {
		return resp.versions, resp.err
	}
	return []string{stubVersion}, nil
}

// ResolveLatestVersion returns a deterministic canonical version for any path.
func (s *registryStub) ResolveLatestVersion(ctx context.Context, modulePath string) (string, error) {
	s.resolvePaths = append(s.resolvePaths, modulePath)
	base := stubBasePath(modulePath)
	if resp, ok := s.resolves[base]; ok {
		return resp.version, resp.err
	}
	return base + "@" + stubVersion, nil
}

// Registry returns nil: LoadIndex accepts a nil registry for the self-contained
// on-disk fixture.
func (s *registryStub) Registry() modconfig.Registry {
	return nil
}

// TestRegistryStubOverrides verifies the stub's default responses and override
// setters. Matching is by module base path (version suffix stripped).
func TestRegistryStubOverrides(t *testing.T) {
	t.Parallel()
	stub := newRegistryStub(&registry.Index{}, t.TempDir())
	ctx := context.Background()

	if _, err := stub.Fetch(ctx, "github.com/x/y@v1"); err == nil {
		t.Error("default Fetch should error for an unknown module")
	}
	stub.SetFetch("github.com/x/y@v1", registry.FetchResult{SourceDir: "/tmp/x"}, nil)
	res, err := stub.Fetch(ctx, "github.com/x/y@v2")
	if err != nil || res.SourceDir != "/tmp/x" {
		t.Errorf("SetFetch override not honoured: res=%+v err=%v", res, err)
	}

	if got, _ := stub.ResolveLatestVersion(ctx, "github.com/x/y@v1"); got != "github.com/x/y@"+stubVersion {
		t.Errorf("default ResolveLatestVersion = %q, want base@%s", got, stubVersion)
	}
	stub.SetResolve("github.com/x/y@v1", "github.com/x/y@v9.9.9", nil)
	if got, _ := stub.ResolveLatestVersion(ctx, "github.com/x/y@v1"); got != "github.com/x/y@v9.9.9" {
		t.Errorf("SetResolve override not honoured: %q", got)
	}

	vers, err := stub.ModuleVersions(ctx, "github.com/x/y@v1")
	if err != nil || len(vers) != 1 || vers[0] != stubVersion {
		t.Errorf("default ModuleVersions = %v err=%v, want [%s]", vers, err, stubVersion)
	}
	stub.SetVersions("github.com/x/y@v1", []string{"v1.2.0", "v1.3.0"}, nil)
	vers, err = stub.ModuleVersions(ctx, "github.com/x/y@v2")
	if err != nil || len(vers) != 2 || vers[0] != "v1.2.0" || vers[1] != "v1.3.0" {
		t.Errorf("SetVersions override not honoured: %v err=%v", vers, err)
	}
}

func stubBasePath(modulePath string) string {
	if i := strings.LastIndex(modulePath, "@"); i >= 0 {
		return modulePath[:i]
	}
	return modulePath
}

// setupStartTestConfigWithRegistry performs the standard setupStartTestConfig
// isolation, writes an on-disk index CUE fixture derived from idx, and returns
// the stub wired to both the in-memory index and that fixture directory.
func setupStartTestConfigWithRegistry(t *testing.T, idx *registry.Index) (tmpDir string, stub *registryStub) {
	t.Helper()
	if idx.Skills == nil {
		// LoadIndex initialises every category; keep the in-memory index
		// lockstep with the on-disk fixture for tests that omit Skills.
		idx.Skills = map[string]registry.IndexEntry{}
	}
	tmpDir = setupStartTestConfig(t)
	// chdir so the written .start dir resolves as local config.
	chdir(t, tmpDir)

	fixtureDir := filepath.Join(tmpDir, "index-fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("creating index fixture dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "index.cue"), []byte(renderIndexCUE(idx)), 0o644); err != nil {
		t.Fatalf("writing index fixture: %v", err)
	}

	// Guard the lockstep invariant: the on-disk fixture and the in-memory index
	// must decode to the same thing, so renderIndexCUE drifting from
	// registry.IndexEntry fails loudly rather than diverging silently.
	loaded, err := registry.LoadIndex(fixtureDir, nil)
	if err != nil {
		t.Fatalf("loading index fixture: %v", err)
	}
	if !reflect.DeepEqual(loaded, idx) {
		t.Fatalf("on-disk index fixture diverged from in-memory index; renderIndexCUE is out of sync with registry.IndexEntry\nin-memory: %+v\non-disk:   %+v", idx, loaded)
	}

	return tmpDir, newRegistryStub(idx, fixtureDir)
}

// renderIndexCUE serialises an Index into the `package index` CUE form that
// registry.LoadIndex parses.
func renderIndexCUE(idx *registry.Index) string {
	var b strings.Builder
	b.WriteString("package index\n")
	renderIndexCategory(&b, "agents", idx.Agents)
	renderIndexCategory(&b, "roles", idx.Roles)
	renderIndexCategory(&b, "contexts", idx.Contexts)
	renderIndexCategory(&b, "tasks", idx.Tasks)
	renderIndexCategory(&b, "skills", idx.Skills)
	return b.String()
}

func renderIndexCategory(b *strings.Builder, name string, entries map[string]registry.IndexEntry) {
	if len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Fprintf(b, "\n%s: {\n", name)
	for _, n := range names {
		e := entries[n]
		fmt.Fprintf(b, "\t%q: {\n", n)
		if e.Module != "" {
			fmt.Fprintf(b, "\t\tmodule:      %q\n", e.Module)
		}
		if e.Description != "" {
			fmt.Fprintf(b, "\t\tdescription: %q\n", e.Description)
		}
		if e.Bin != "" {
			fmt.Fprintf(b, "\t\tbin:         %q\n", e.Bin)
		}
		if e.Version != "" {
			fmt.Fprintf(b, "\t\tversion:     %q\n", e.Version)
		}
		if len(e.Tags) > 0 {
			quoted := make([]string, len(e.Tags))
			for i, tag := range e.Tags {
				quoted[i] = fmt.Sprintf("%q", tag)
			}
			fmt.Fprintf(b, "\t\ttags: [%s]\n", strings.Join(quoted, ", "))
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
}
