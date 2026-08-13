// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

// concurrentVersions is every version string the fixture below declares.
//
// ⚠️ EVERY ONE OF THEM ENDS IN ".0", AND THAT IS LOAD-BEARING. The upstream
// hazard this test exists for -- go-version's part.Parts.Padding appending into
// capacity that a by-value copy of a version.Version shares -- can only fire
// when the operand being padded HAS spare capacity, and a version acquires
// spare capacity only where cmpkey reslices trailing zeros off its release
// segment. "3.11" has none and is immune; "3.11.0" has one and races.
//
// So a concurrency test built on versions that happen not to end in zero proves
// nothing while looking exactly like one that does. That is not hypothetical: a
// first attempt at this test passed against production data for precisely that
// reason, because none of the sampled boto3 versions ended in ".0".
//
// guardTrailingZeros asserts the property rather than trusting this comment,
// which cannot stop someone adding "1.2.3" to the list later.
var concurrentVersions = []string{"1.0.0", "1.1.0", "2.0.0", "2.1.0", "3.0.0"}

// guardTrailingZeros fails the test if the fixture has drifted into versions
// that cannot exercise the padding hazard.
func guardTrailingZeros(t *testing.T, versions []string) {
	t.Helper()
	for _, v := range versions {
		if !strings.HasSuffix(v, ".0") {
			t.Fatalf("fixture version %q does not end in \".0\", so it carries no spare "+
				"release capacity and cannot exercise the shared-Version data race; "+
				"see the note on concurrentVersions", v)
		}
	}
}

// concurrentIndex builds the graph the resolutions below walk.
//
// Shaped to reach what an index-layer slice-mutation test cannot: an extra (so
// the provider expands a virtual package and its own requirements), a marker
// (so marker.Evaluate runs against the environment), a Requires-Python (so
// Specifiers.Check runs), and several versions per package so the solver has to
// rank and compare them.
func concurrentIndex(t *testing.T) index.MetadataIndex {
	t.Helper()
	guardTrailingZeros(t, concurrentVersions)

	idx := index.NewMockIndex("concurrent")
	for _, v := range concurrentVersions {
		idx.AddVersion("app", v, "lib[fast]>=1.0.0", `plat>=2.0.0 ; sys_platform == "linux"`)
		idx.SetMetadata("lib", v, index.PackageMetadata{
			RequiresDist:   mustRequirements(t, "core>=1.0.0", `turbo>=1.0.0 ; extra == "fast"`),
			RequiresPython: mustSpecifiers(t, ">=3.8.0"),
			ProvidesExtra:  []string{"fast"},
		})
		idx.AddVersion("core", v)
		idx.AddVersion("turbo", v, "core>=1.0.0")
		idx.AddVersion("plat", v)
	}
	return idx
}

// Eight concurrent resolutions against ONE shared index.
//
// Run under -race. Without it this still checks that concurrent resolutions
// agree, which is worth something; the hazard it exists for is invisible.
//
// ⚠️ Why this exists alongside the index-layer test in index/rsfmemo_test.go.
// That one sorts and overwrites the slices an index hands back, which exercises
// version.Version.Compare on values RSFIndex produced -- the same padding
// hazard, at the index seam. What it cannot reach is everything the resolver
// does with a version AFTER the index returns it: ranking candidates, mapping a
// requirement's specifiers into a version set, evaluating a PEP 508 marker,
// checking an interpreter constraint. Those are where a resolution actually
// spends its comparisons, and only a real Resolve runs them.
//
// Every parsed value a goroutine works with is built INSIDE that goroutine --
// its own requirements, its own Options, its own marker environment. Sharing a
// parsed version.Version between goroutines is the hazard under test, so the
// harness must not introduce it itself; see PackageMetadata.SupportsPython for
// the same warning aimed at callers.
func TestConcurrentResolutionsAgreeAndAreRaceFree(t *testing.T) {
	idx := concurrentIndex(t)

	const goroutines = 8
	results := make([]map[string]string, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = resolveConcurrently(idx)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Agreement is asserted as well as absence of a race, because the race the
	// Go memory model permits here happens to write the same value to the same
	// address -- benign today, and so invisible to every check except -race. A
	// disagreement would mean something worse than the known hazard.
	want := results[0]
	if len(want) == 0 {
		t.Fatal("the resolution pinned nothing; the fixture is not exercising the solver")
	}
	for i, got := range results[1:] {
		if len(got) != len(want) {
			t.Fatalf("goroutine %d pinned %v, goroutine 0 pinned %v", i+1, got, want)
		}
		for name, ver := range want {
			if got[name] != ver {
				t.Fatalf("goroutine %d pinned %s %s, goroutine 0 pinned %s %s",
					i+1, name, got[name], name, ver)
			}
		}
	}
}

// resolveConcurrently runs one resolution and renders its pins as strings.
//
// It takes no testing.TB and returns an error instead, because t.Fatalf from a
// goroutine other than the one running the test is a testing API violation --
// FailNow stops only the calling goroutine, so the test would carry on with a
// nil result. Rendering to strings before returning is deliberate for the same
// reason the comparison is: handing parsed versions back to the collecting
// goroutine would introduce the sharing this test is checking for.
func resolveConcurrently(idx index.MetadataIndex) (map[string]string, error) {
	root, err := requirement.Parse("app>=1.0.0")
	if err != nil {
		return nil, err
	}
	reqs := []requirement.Requirement{root}

	env, err := marker.EnvironmentFromTarget(
		tags.Target{Implementation: "cp", PyMajor: 3, PyMinor: 11, OS: "linux", Arch: "x86_64"},
		marker.InterpreterIdentity{
			ImplementationName:           "cpython",
			PlatformPythonImplementation: "CPython",
			PythonFullVersion:            "3.11.4",
			ImplementationVersion:        "3.11.4",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build marker environment: %w", err)
	}

	pyVer, err := version.Parse("3.11.4")
	if err != nil {
		return nil, err
	}

	res, err := resolver.Resolve(context.Background(), reqs, idx, resolver.Options{
		Environment:   env,
		PythonVersion: pyVer,
	})
	if err != nil {
		return nil, fmt.Errorf("Resolve: %w", err)
	}

	out := make(map[string]string, len(res.Pinned))
	for name, ver := range res.Pinned {
		out[name.String()] = ver.String()
	}
	return out, nil
}
