// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// outOfScope lists every packse scenario with resolver_options.universal =
// true, keyed by its testdata/packse-relative name (no extension). These are
// NOT run: go-pyresolver resolves one concrete marker environment at a time
// (resolver.Options.Environment's doc comment), and universal
// (environment-independent) resolution is deferred by RFD 0001.
//
// Measured against the vendored corpus (testdata/packse/README.md's pinned
// commit): exactly 41 scenarios set resolver_options.universal, all of
// fork/, tag_and_markers/, and 2 each of backtracking/ and wheels/.
var outOfScope = map[string]string{
	"backtracking/wrong-backtracking-basic":                    "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"backtracking/wrong-backtracking-indirect":                 "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/allows-non-conflicting-non-overlapping-dependencies": "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/allows-non-conflicting-repeated-dependencies":        "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/basic":                                        "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/conflict-in-fork":                             "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/conflict-unsatisfiable":                       "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/filter-sibling-dependencies":                  "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/fork-upgrade":                                 "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/incomplete-markers":                           "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-accrue":                                "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-disjoint":                              "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit":                               "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit-combined":                      "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit-combined-allowed":              "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit-combined-disallowed":           "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit-isolated":                      "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-inherit-transitive":                    "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-limited-inherit":                       "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-selection":                             "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/marker-track":                                 "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/non-fork-marker-transitive":                   "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/non-local-fork-marker-direct":                 "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/non-local-fork-marker-transitive":             "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/overlapping-markers-basic":                    "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/preferences-dependent-forking":                "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/preferences-dependent-forking-bistable":       "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/preferences-dependent-forking-conflicting":    "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/preferences-dependent-forking-tristable":      "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/remaining-universe-partitioning":              "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/requires-python":                              "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/requires-python-full":                         "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/requires-python-full-prerelease":              "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"fork/requires-python-patch-overlap":                "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"tag_and_markers/requires-python-wheels":            "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"tag_and_markers/unreachable-package":               "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"tag_and_markers/unreachable-wheels":                "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"tag_and_markers/virtual-package-extra-priorities":  "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"tag_and_markers/virtual-package-marker-priorities": "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"wheels/requires-python-subset":                     "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
	"wheels/specific-architecture":                      "universal resolution (resolver_options.universal); RFD 0001 defers universal resolution",
}

// unsupportedOption lists scenarios whose resolver_options ask for something
// resolver.Options has no field for. They are not resolved at all -- the
// harness itself refuses, which IS the assertion, rather than silently
// running without the option and reporting a result that happens to match for
// the wrong reason.
var unsupportedOption = map[string]string{
	"wheels/no-binary":             "resolver_options.no_binary has no resolver.Options equivalent",
	"wheels/no-build":              "resolver_options.no_build has no resolver.Options equivalent",
	"wheels/no-wheels-no-build":    "resolver_options.no_build has no resolver.Options equivalent",
	"wheels/only-wheels-no-binary": "resolver_options.no_binary has no resolver.Options equivalent",
}

// knownFail lists scenarios that DO run against the real resolver, and are
// asserted to still disagree with packse's expected outcome. Never t.Skip:
// when the underlying gap closes, the assertion below flips to
// "unexpectedly passed" and fails, which is what makes this list honest.
var knownFail = map[string]string{
	"yanked/transitive-package-only-yanked-in-range-opt-in": "FilteredIndex.ExcludeYanked drops a yanked version outright; " +
		"PEP 592 (and uv) still allow it when a requirement pins it exactly, which this scenario's root does for b==1.0.0",
	"yanked/transitive-yanked-and-unyanked-dependency-opt-in": "FilteredIndex.ExcludeYanked drops a yanked version outright; " +
		"PEP 592 (and uv) still allow it when a requirement pins it exactly, which this scenario's root does for c==2.0.0",

	"extras/missing-extra": "go-pyresolver models name[extra] as a virtual package requiring a candidate that " +
		"declares the extra, so a version that omits it is excluded rather than the extra being silently dropped; " +
		"uv ignores an extra no candidate provides",
	"extras/extra-does-not-exist-backtrack": "same gap as extras/missing-extra: the newest version (3.0.0) does not " +
		"provide the extra, so go-pyresolver backtracks to the one that does (1.0.0) instead of dropping the extra",

	"prereleases/package-only-prereleases": "candidate.PrereleaseSet admits a pre-release only when a specifier " +
		"names one or the caller opts in; it does not implement pip/uv's further fallback of admitting one when a " +
		"package publishes no final release at all (see PrereleaseSet.Admits's doc comment)",
	"prereleases/package-only-prereleases-boundary": "same gap as prereleases/package-only-prereleases",
	"prereleases/transitive-package-only-prereleases": "same gap as prereleases/package-only-prereleases, one level " +
		"down the dependency graph",

	"requires_python/python-less-than-current": "go-pyresolver's SupportsPython enforces the full Requires-Python " +
		"specifier per PEP 440, including an upper bound; uv deliberately ignores an upper bound on Requires-Python",
}

// runPackseScenario resolves s and reports whether the result matches
// expected.
func runPackseScenario(t *testing.T, s tomlScenario) (matched bool, detail string) {
	t.Helper()

	py, err := scenarioPython(s)
	if err != nil {
		t.Fatalf("build python target: %v", err)
	}
	target, err := buildTarget(py, s.ResolverOptions.PythonPlatform)
	if err != nil {
		t.Fatalf("build tags target: %v", err)
	}
	env, err := buildEnvironment(target, py)
	if err != nil {
		t.Fatalf("build marker environment: %v", err)
	}
	filter, err := wheelTagFilter(target, fmt.Sprintf("cp%d%d on %s/%s", py.major, py.minor, target.OS, target.Arch))
	if err != nil {
		t.Fatalf("build wheel tag filter: %v", err)
	}

	idx := index.NewFilteredIndex(buildMockIndex(t, s.Name, s.Packages), index.FilterPolicy{ExcludeYanked: true})

	opts := resolver.Options{
		Environment:   env,
		PythonVersion: version.MustParse(py.full),
		WheelTags:     filter,
	}
	if s.ResolverOptions.Prereleases {
		opts.AllowPrerelease = allPackageNames(s.Packages)
	}

	reqs := mustRequirements(t, s.Root.Requires...)
	res, resolveErr := resolver.Resolve(context.Background(), reqs, idx, opts)

	switch {
	case !s.Expected.Satisfiable:
		if resolveErr == nil {
			return false, fmt.Sprintf("expected unsatisfiable, but resolved to %v", pins(t, res))
		}
		var re *resolver.ResolutionError
		if !errors.As(resolveErr, &re) {
			return false, fmt.Sprintf(
				"expected unsatisfiable, but Resolve failed with a non-ResolutionError "+
					"(that is the harness failing, not the resolver saying unsatisfiable): %v", resolveErr)
		}
		return true, ""

	case len(s.Expected.Packages) > 0:
		if resolveErr != nil {
			return false, fmt.Sprintf("expected packages %v, but Resolve failed: %v", s.Expected.Packages, resolveErr)
		}
		got := pins(t, res)
		if !reflect.DeepEqual(got, s.Expected.Packages) {
			return false, fmt.Sprintf("Pinned = %v, want %v", got, s.Expected.Packages)
		}
		return true, ""

	default:
		if resolveErr != nil {
			return false, fmt.Sprintf("expected to resolve, but Resolve failed: %v", resolveErr)
		}
		return true, ""
	}
}

// TestPackse runs every non-universal vendored packse scenario against the
// real resolver.Resolve, classified per testdata/packse/README.md and the
// three lists above. See the package doc comment on this file's neighbors for
// why a scenario belongs on one list rather than another.
func TestPackse(t *testing.T) {
	scenarios := loadPackseScenarios(t)
	if len(scenarios) == 0 {
		t.Fatal("no scenarios loaded -- testdata/packse is missing or empty")
	}

	seen := make(map[string]bool, len(scenarios))
	var pass, known, unsupported, outScope int

	for _, ps := range scenarios {
		name := ps.relName
		s := ps.scenario
		seen[name] = true

		if reason, ok := outOfScope[name]; ok {
			if !s.ResolverOptions.Universal {
				t.Errorf("%s: listed in outOfScope (%s) but resolver_options.universal is not set", name, reason)
			}
			outScope++
			continue
		}
		if s.ResolverOptions.Universal {
			t.Errorf("%s: resolver_options.universal is set but not listed in outOfScope", name)
			continue
		}

		if reason, ok := unsupportedOption[name]; ok {
			_ = reason
			unsupported++
			continue
		}
		if len(s.ResolverOptions.NoBuild) > 0 || len(s.ResolverOptions.NoBinary) > 0 {
			t.Errorf("%s: uses no_build/no_binary but is not listed in unsupportedOption", name)
			continue
		}
		if s.ResolverOptions.PythonPlatform != nil {
			if _, _, ok := packsePlatform(*s.ResolverOptions.PythonPlatform); !ok {
				t.Errorf("%s: python_platform %q is not recognized and not listed in unsupportedOption",
					name, *s.ResolverOptions.PythonPlatform)
				continue
			}
		}

		reason, isKnownFail := knownFail[name]
		t.Run(name, func(t *testing.T) {
			matched, detail := runPackseScenario(t, s)
			switch {
			case isKnownFail && matched:
				t.Errorf("%s unexpectedly passed, remove it from knownFail (reason was: %s)", name, reason)
			case isKnownFail && !matched:
				// Expected: still fails. Nothing to assert further.
			case !isKnownFail && !matched:
				t.Errorf("%s: %s", name, detail)
			}
		})

		if isKnownFail {
			known++
		} else {
			pass++
		}
	}

	for name := range knownFail {
		if !seen[name] {
			t.Errorf("knownFail names %q, which does not exist in testdata/packse", name)
		}
	}
	for name := range unsupportedOption {
		if !seen[name] {
			t.Errorf("unsupportedOption names %q, which does not exist in testdata/packse", name)
		}
	}
	for name := range outOfScope {
		if !seen[name] {
			t.Errorf("outOfScope names %q, which does not exist in testdata/packse", name)
		}
	}

	t.Logf("packse: %d scenarios = %d pass + %d known-fail + %d unsupported + %d out-of-scope",
		len(scenarios), pass, known, unsupported, outScope)
}
