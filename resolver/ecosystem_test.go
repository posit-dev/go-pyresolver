// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// TestEcosystem resolves seven real projects from uv's ecosystem suite and
// compares the whole pin map with what uv 0.12.23 locked for the same roots.
// testdata/ecosystem/README.md says how the inputs, goldens and fixtures were
// made. This test runs offline: the fixtures are a recorded metadata closure.

// ecosystemDir holds one directory per project.
const ecosystemDir = "testdata/ecosystem"

// ecosystemPython is the interpreter both sides resolve for. It is the
// --python-version the goldens were compiled with.
const ecosystemPython = "3.12.11"

// ecosystemExcludeNewer is the --exclude-newer the goldens were compiled with,
// and the cutoff the fixtures were recorded under.
const ecosystemExcludeNewer = "2026-06-30T00:00:00Z"

// ecosystemProjects are the projects under testdata/ecosystem. Sentry is not
// among them: its license (FSL) is not permissive.
var ecosystemProjects = []string{
	"black", "cookiecutter", "flask", "httpx", "llm", "packse", "pytest-cov",
}

// ecosystemDifference is one place go-pyresolver deliberately answers
// differently from uv. Ours and UV are "" when that side does not pin the
// package at all.
type ecosystemDifference struct {
	Project string
	Package string
	Ours    string
	UV      string
	// Ruling is "R1" (2026-09-24) or "R2" (2026-09-25), the two rulings an
	// entry may rest on. Anything else is a bug to fix or park, not an entry.
	Ruling string
	Reason string
}

// expectedDifferences lists every difference from uv, each tied to a ruling:
//
//	R1: pip enforces the whole Requires-Python specifier, upper bound
//	    included; uv ignores the upper bound.
//	R2: when no final release is in range, pip and current uv fall back to a
//	    pre-release; uv before astral-sh/uv#19993 did not.
//
// Never add a third reason.
var expectedDifferences = []ecosystemDifference{}

// ecosystemFixture is a recorded metadata closure for one project: every
// version of each package the resolver asked about (already cut at
// --exclude-newer) and the metadata of each version it looked at.
type ecosystemFixture struct {
	Packages map[string]*ecosystemPackage `json:"packages"`
}

type ecosystemPackage struct {
	Versions []string                    `json:"versions"`
	Metadata map[string]ecosystemVersion `json:"metadata"`
}

// ecosystemVersion is one version's recorded metadata. Source says where the
// requirements came from: "pep658" (the wheel's .metadata file) or "json-api".
type ecosystemVersion struct {
	RequiresDist   []string `json:"requires_dist,omitempty"`
	RequiresPython string   `json:"requires_python,omitempty"`
	ProvidesExtra  []string `json:"provides_extra,omitempty"`
	WheelTags      []string `json:"wheel_tags,omitempty"`
	HasSdist       bool     `json:"has_sdist,omitempty"`
	Yanked         bool     `json:"yanked,omitempty"`
	Source         string   `json:"source"`
	// Unusable holds the reason when the metadata exists but cannot be used
	// (a Requires-Dist PEP 508 rejects). Replayed as ErrMetadataUnusable.
	Unusable string `json:"unusable,omitempty"`
}

// ecosystemReplay serves a fixture and remembers every question it could not
// answer, so a resolver that now looks somewhere the recording never went
// fails the test instead of quietly choosing another version.
type ecosystemReplay struct {
	*index.MockIndex
	unusable map[string]string

	mu     sync.Mutex
	misses map[string]bool
}

func newEcosystemReplay(t *testing.T, project string, fx ecosystemFixture) *ecosystemReplay {
	t.Helper()
	mock := index.NewMockIndex("ecosystem-" + project).SetWheelTagsComplete(true).SetYanksCaptured(true)
	r := &ecosystemReplay{MockIndex: mock, unusable: map[string]string{}, misses: map[string]bool{}}
	for name, pkg := range fx.Packages {
		mock.AddPackage(name)
		for _, ver := range pkg.Versions {
			meta, ok := pkg.Metadata[ver]
			if !ok {
				mock.SetUnavailable(name, ver)
				continue
			}
			if meta.Unusable != "" {
				mock.SetUnavailable(name, ver)
				r.unusable[name+" "+ver] = meta.Unusable
				continue
			}
			rec, err := index.ParseRecord(index.RawRecord{
				RequiresDist:   meta.RequiresDist,
				RequiresPython: meta.RequiresPython,
				ProvidesExtra:  meta.ProvidesExtra,
				WheelTags:      meta.WheelTags,
				HasSdist:       meta.HasSdist,
				TagsCaptured:   true,
				Yanked:         meta.Yanked,
			})
			if err != nil {
				t.Fatalf("%s: %s %s: fixture metadata does not parse: %v", project, name, ver, err)
			}
			mock.SetMetadata(name, ver, rec)
		}
	}
	return r
}

func (r *ecosystemReplay) miss(what string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.misses[what] = true
}

// Versions implements index.MetadataIndex.
func (r *ecosystemReplay) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	vs, err := r.MockIndex.Versions(ctx, pkg)
	if errors.Is(err, index.ErrPackageNotFound) {
		r.miss("the versions of " + pkg.String())
	}
	return vs, err
}

// Metadata implements index.MetadataIndex.
func (r *ecosystemReplay) Metadata(
	ctx context.Context, pkg index.PackageName, ver version.Version,
) (index.PackageMetadata, error) {
	key := pkg.String() + " " + ver.String()
	if why, ok := r.unusable[key]; ok {
		return index.PackageMetadata{}, fmt.Errorf("%s: %s: %w", key, why, index.ErrMetadataUnusable)
	}
	meta, err := r.MockIndex.Metadata(ctx, pkg, ver)
	if errors.Is(err, index.ErrMetadataUnavailable) || errors.Is(err, index.ErrPackageNotFound) {
		r.miss("the metadata of " + key)
	}
	return meta, err
}

func (r *ecosystemReplay) missed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.misses))
	for m := range r.misses {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// readLines returns the non-blank, non-comment lines of a file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// ecosystemGolden parses uv's compiled output: one name==version per line.
func ecosystemGolden(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range readLines(t, path) {
		name, ver, ok := strings.Cut(line, "==")
		if !ok {
			t.Fatalf("%s: not a pin: %q", path, line)
		}
		out[index.NewPackageName(name).String()] = ver
	}
	return out
}

// resolveEcosystem resolves a project's roots over its fixture on the 3.12
// target (not testEnv, which targets 3.11.4).
func resolveEcosystem(t *testing.T, project string) (ours map[string]string, roots []string, misses []string) {
	t.Helper()
	dir := filepath.Join(ecosystemDir, project)

	raw, err := os.ReadFile(filepath.Join(dir, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx ecosystemFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("%s: fixture.json: %v", project, err)
	}
	idx := newEcosystemReplay(t, project, fx)

	py := pythonSpec{full: ecosystemPython, major: 3, minor: 12}
	target, err := buildTarget(py, nil)
	if err != nil {
		t.Fatal(err)
	}
	env, err := buildEnvironment(target, py)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := wheelTagFilter(target, "cp312 on linux/x86_64")
	if err != nil {
		t.Fatal(err)
	}

	rootReqs := mustRequirements(t, readLines(t, filepath.Join(dir, "requirements.in"))...)
	res, err := resolver.Resolve(context.Background(), rootReqs, idx, resolver.Options{
		Environment:   env,
		PythonVersion: version.MustParse(py.full),
		WheelTags:     filter,
	})
	if err != nil {
		// A missing fixture entry is the likely cause; say so first.
		for _, m := range idx.missed() {
			t.Errorf("fixture lacks %s, regenerate (see testdata/ecosystem/README.md)", m)
		}
		t.Fatalf("%s: Resolve failed: %v", project, err)
	}
	// Roots whose marker is false on this target (tomli on 3.12) are not pinned.
	for _, r := range rootReqs {
		if r.Marker.Evaluate(env, nil) {
			roots = append(roots, index.NewPackageName(r.Name).String())
		}
	}
	return pins(t, res), roots, idx.missed()
}

// ecosystemDiff returns the packages on which ours and theirs disagree,
// including a package only one side pins.
func ecosystemDiff(ours, theirs map[string]string) map[string][2]string {
	out := map[string][2]string{}
	for name, o := range ours {
		if u := theirs[name]; u != o {
			out[name] = [2]string{o, u}
		}
	}
	for name, u := range theirs {
		if _, ok := ours[name]; !ok {
			out[name] = [2]string{"", u}
		}
	}
	return out
}

func TestEcosystem(t *testing.T) {
	// A directory nobody listed would go untested.
	entries, err := os.ReadDir(ecosystemDir)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		if e.IsDir() {
			onDisk = append(onDisk, e.Name())
		}
	}
	if strings.Join(onDisk, ",") != strings.Join(ecosystemProjects, ",") {
		t.Fatalf("testdata/ecosystem holds projects %v, but ecosystemProjects is %v", onDisk, ecosystemProjects)
	}

	// Each entry must match one real difference, and each difference one entry.
	used := map[int]bool{}
	var compared, diffs int

	for _, project := range ecosystemProjects {
		t.Run(project, func(t *testing.T) {
			golden := ecosystemGolden(t, filepath.Join(ecosystemDir, project, "golden.txt"))
			if len(golden) == 0 {
				t.Fatal("golden.txt has no pins, so nothing would be compared")
			}
			ours, roots, misses := resolveEcosystem(t, project)
			for _, m := range misses {
				t.Errorf("fixture lacks %s, regenerate (see testdata/ecosystem/README.md)", m)
			}

			// An empty closure matches an empty golden only; check the roots.
			for _, r := range roots {
				if _, ok := ours[r]; !ok {
					t.Errorf("root %s is not in our pins", r)
				}
				if _, ok := golden[r]; !ok {
					t.Errorf("root %s is not in uv's golden", r)
				}
			}

			compared += len(golden)
			for pkg, pair := range ecosystemDiff(ours, golden) {
				found := -1
				for i, d := range expectedDifferences {
					if d.Project == project && d.Package == pkg {
						found = i
					}
				}
				if found < 0 {
					t.Errorf("%s: ours %q, uv %q, and no expectedDifferences entry covers it", pkg, pair[0], pair[1])
					continue
				}
				d := expectedDifferences[found]
				if d.Ours != pair[0] || d.UV != pair[1] {
					t.Errorf("%s: ours %q, uv %q, but the entry says ours %q, uv %q (%s: %s)",
						pkg, pair[0], pair[1], d.Ours, d.UV, d.Ruling, d.Reason)
					continue
				}
				used[found] = true
				diffs++
			}
		})
	}

	known := map[string]bool{}
	for _, p := range ecosystemProjects {
		known[p] = true
	}
	for i, d := range expectedDifferences {
		if d.Ruling != "R1" && d.Ruling != "R2" {
			t.Errorf("entry %s/%s cites ruling %q; only R1 and R2 may be cited", d.Project, d.Package, d.Ruling)
		}
		switch {
		case !known[d.Project]:
			t.Errorf("expectedDifferences names project %q, which does not exist", d.Project)
		case !ecosystemMentions(t, d.Project, d.Package):
			t.Errorf("expectedDifferences names %s/%s, no such package in that project", d.Project, d.Package)
		case !used[i]:
			t.Errorf("%s/%s unexpectedly agrees with uv, remove it from expectedDifferences", d.Project, d.Package)
		}
	}

	t.Logf("ecosystem: %d projects, %d pins compared, %d expected differences",
		len(ecosystemProjects), compared, diffs)
}

// ecosystemMentions reports whether pkg appears in the project's fixture or
// golden.
func ecosystemMentions(t *testing.T, project, pkg string) bool {
	t.Helper()
	dir := filepath.Join(ecosystemDir, project)
	if _, ok := ecosystemGolden(t, filepath.Join(dir, "golden.txt"))[pkg]; ok {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx ecosystemFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	_, ok := fx.Packages[pkg]
	return ok
}
