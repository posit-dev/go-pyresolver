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
// (so marker.Evaluate runs against the environment), a Requires-Python (so the
// interpreter is a package in the graph), and several versions per package so
// the solver has to rank and compare them.
//
// ⚠️ CORRECTION. This said the Requires-Python was here "so Specifiers.Check
// runs". It does not. The provider converts Requires-Python into a version set
// in interpreterDependency and never calls Check, and Specifiers.Check has no
// non-test caller in this module outside index.PackageMetadata.SupportsPython --
// which is an API for external callers rather than a step of a resolve. The
// Requires-Python still earns its place, for the reason now given.
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
// its own requirements, its own Options, its own marker environment.
//
// ⚠️ THIS TEST DOES NOT COVER THE PADDING HAZARD, AND ITS COMMENT USED TO IMPLY
// THAT IT DID. Verified by pinning go-python-packaging back to v0.5.0 and
// running the whole suite under -race: this test stayed green while the shared
// hazard was live. Two independent reasons, either sufficient:
//
//   - It deliberately shares nothing parsed between goroutines, which is the
//     right call for what it does check but is the opposite of what reaching
//     the hazard requires.
//   - MockIndex.Versions re-parses every version from its stored string key on
//     every call, so even the index cannot hand two goroutines a Version that
//     shares a backing array. No fixture edit here could have changed that.
//
// The tests that DO cover it are TestSharedParsedVersionIsRaceFree and
// TestSupportsPythonSharedTargetIsRaceFree in the index package, each confirmed
// individually to fail at go-python-packaging v0.5.0 and pass at the pinned
// version -- re-checked at v0.6.0 and again at v0.7.0.
//
// ⚠️ NOT TestConcurrentResolutionsShareOneParsedInterpreter below, which an
// earlier draft of this paragraph listed here. That test shares the one thing a
// real caller shares, which makes it worth having, but it measures ZERO races at
// v0.5.0 and does not cover the hazard either. Its own doc comment says so at
// length; this sentence contradicted it for several commits.
//
// What this test still earns its place for is everything the index-layer tests
// cannot reach: ranking, version-set mapping, marker evaluation and interpreter
// checking under genuine concurrency, and agreement between the results.
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

// sharedInterpreter is the interpreter version the test below parses ONCE and
// hands to every goroutine.
//
// "3.11.0" and not testOptions' "3.11.4". The padding hazard needs the shared
// operand to carry spare capacity, which a version acquires only where the
// comparison key strips trailing zeros: "3.11.4" strips nothing and is immune,
// "3.11.0" strips to [3 11] with cap 3.
//
// ⚠️ That is a NECESSARY condition, not a sufficient one, and this test does not
// meet the rest of it -- see the note on the test function. Measured at
// go-python-packaging v0.5.0, this test reports zero data races with "3.11.0".
// The spelling is kept because it costs nothing and is the right shape if the
// resolver ever routes a shared target into a padding comparison; it is not
// what makes this test work, because this test does not discriminate.
const sharedInterpreter = "3.11.0"

// sharedInterpreterConstraint is the Requires-Python the fixture declares.
//
// "<" and not ">=", for the same reason and with the same caveat as
// sharedInterpreter. Per the reproduction table in
// index.PackageMetadata.SupportsPython's doc comment, only "<" and ">" compare
// the prospective version DIRECTLY; ">=", "<=", "==" and "!=" re-parse it
// through Public() first and are immune.
//
// ⚠️ Do NOT read that as "so this test would be green with >=3.8". It is green
// EITHER WAY: measured at go-python-packaging v0.5.0, this test reports zero
// data races with "<3.12.1" and zero with ">=3.8.0", because Specifiers.Check is
// never reached from a resolve at all. The operator matters where the
// reproduction table applies, which is index.PackageMetadata.SupportsPython and
// the tests in index/shared_version_test.go that call it -- not here.
const sharedInterpreterConstraint = "<3.12.1"

// TestConcurrentResolutionsShareOneParsedInterpreter is
// TestConcurrentResolutionsAgreeAndAreRaceFree with the one value a real caller
// actually shares -- its parsed interpreter version -- genuinely shared.
//
// A caller fanning resolutions across goroutines parses the interpreter once, in
// the setup, and puts it in every Options. That is the natural thing to write,
// and it is what index.PackageMetadata.SupportsPython told callers not to do
// under go-python-packaging v0.5.0 and now says is fine.
//
// # ⚠️ THIS TEST DOES NOT DISCRIMINATE, AND SAYING SO IS THE POINT
//
// Measured, not assumed: pinned back to go-python-packaging v0.5.0, with the
// padding hazard live, this test reports ZERO data races. It is kept anyway, and
// labelled, because "we added a resolver-level concurrency test" is exactly the
// sentence that turns into false assurance three months from now.
//
// It does not discriminate because Resolve never routes a shared Version into a
// padding comparison:
//
//   - Options.PythonVersion reaches pep440set.Exactly, and pep440set compares
//     through derived position keys. Its releaseKey strips trailing zeros the
//     same way the comparison key does, so the two operands always tie on
//     segment count and padding is never called. See the note on
//     TestContainsConcurrent in pep440set/verpos_race_test.go.
//   - Requires-Python never reaches Specifiers.Check from here. The provider
//     converts it to a version set in interpreterDependency instead, so
//     SupportsPython -- the method whose exposure IS real -- is an API for
//     external callers, not an internal step of a resolve.
//   - Every candidate version is re-parsed per call by both MockIndex.Versions
//     and RSFIndex.Versions, so no index hands two goroutines an aliased value.
//
// What it is worth keeping for: each of those three is a property of the current
// implementation, and the last two are explicitly documented as choices that may
// be revisited (RSFIndex.Versions calls the parsed-version memo "available to be
// taken"). If any of them changes, this is the test that starts failing under
// -race. It also pins the caller-visible shape -- share one parsed interpreter
// across concurrent Resolves -- which nothing else covers, because
// TestConcurrentResolutionsAgreeAndAreRaceFree deliberately shares nothing.
//
// The tests that DO discriminate are in the index package:
// TestSharedParsedVersionIsRaceFree and TestSupportsPythonSharedTargetIsRaceFree.
func TestConcurrentResolutionsShareOneParsedInterpreter(t *testing.T) {
	guardTrailingZeros(t, []string{sharedInterpreter})

	// A dedicated fixture rather than concurrentIndex, so that changing the
	// Requires-Python here cannot perturb the other test. It constrains the
	// interpreter, which puts the shared target on one side of a comparison --
	// through pep440set rather than through Specifiers.Check, which is why that
	// is not enough to make this test discriminate. See the note on the function.
	idx := index.NewMockIndex("shared-interpreter")
	for _, v := range concurrentVersions {
		idx.AddVersion("app", v, "lib>=1.0.0")
		idx.SetMetadata("lib", v, index.PackageMetadata{
			RequiresDist:   mustRequirements(t, "core>=1.0.0"),
			RequiresPython: mustSpecifiers(t, sharedInterpreterConstraint),
		})
		idx.AddVersion("core", v)
	}

	// ONE parse. Shared by reference through the closure, and copied by value
	// into each goroutine's Options -- which is exactly the aliasing the fix
	// makes safe.
	pyVer, err := version.Parse(sharedInterpreter)
	if err != nil {
		t.Fatalf("parse %q: %v", sharedInterpreter, err)
	}
	// Not testEnv: Resolve rejects an Options whose PythonVersion disagrees with
	// its Environment's python_full_version, and testEnv is pinned to the
	// hazard-immune "3.11.4". The interpreter identity has to move with the
	// target, not the other way round.
	env, err := marker.EnvironmentFromTarget(
		tags.Target{Implementation: "cp", PyMajor: 3, PyMinor: 11, OS: "linux", Arch: "x86_64"},
		marker.InterpreterIdentity{
			ImplementationName:           "cpython",
			PlatformPythonImplementation: "CPython",
			PythonFullVersion:            sharedInterpreter,
			ImplementationVersion:        sharedInterpreter,
		},
	)
	if err != nil {
		t.Fatalf("build marker environment: %v", err)
	}
	reqs := mustRequirements(t, "app>=1.0.0")

	const goroutines = 8
	results := make([]map[string]string, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := resolver.Resolve(context.Background(), reqs, idx, resolver.Options{
				Environment:   env,
				PythonVersion: pyVer,
			})
			if err != nil {
				errs[i] = err
				return
			}
			out := make(map[string]string, len(res.Pinned))
			for name, ver := range res.Pinned {
				out[name.String()] = ver.String()
			}
			results[i] = out
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Anti-vacuity, and a modest claim on purpose: pinning `lib` means the
	// resolution reached the package that carries the Requires-Python, so the
	// shared interpreter was in the graph as a constrained package rather than
	// the fixture failing early somewhere shallower.
	//
	// ⚠️ It does NOT mean Specifiers.Check ran on the shared target. The provider
	// converts Requires-Python to a version set and compares through pep440set
	// position keys instead, which is exactly why this test does not
	// discriminate -- see the note on the function.
	//
	// It is also mostly belt-and-braces: an unsatisfiable `lib` makes Resolve
	// return an error, and the loop above t.Fatalf's on that first. What it
	// actually guards is a fixture edited into resolving `app` alone.
	want := results[0]
	if want["lib"] == "" {
		t.Fatalf("the resolution did not pin lib, so the constrained package was never "+
			"reached; pinned %v", want)
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
