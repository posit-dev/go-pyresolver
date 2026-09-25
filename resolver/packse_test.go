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
}

// divergence is one scenario's entry on intentionalDivergence: the reason and
// pip's outcome, asserted exactly rather than merely "not packse".
type divergence struct {
	reason string
	// pins is the full pin set pip is asserted to reach. Nil means pip fails:
	// Resolve must return a *resolver.ResolutionError.
	pins map[string]string
}

// intentionalDivergence lists scenarios where go-pyresolver deliberately
// differs from packse's (uv's) expectation because it matches pip.
//
// Ruling 2026-09-24: requires_python/python-less-than-current. pip enforces
// the whole Requires-Python specifier, upper bound included; uv ignores the
// upper bound. Reclassified rather than fixed.
//
// Ruling 2026-09-25: the five prereleases/ scenarios. packse encodes uv's
// admission rule from before astral-sh/uv#19993 ("no final release at all").
// pip (packaging's SpecifierSet.filter with prereleases=None, per PEP 440)
// and current uv (crates/uv-resolver/src/prerelease.rs, PreferStable) instead
// fall back to a pre-release when nothing final satisfies the RANGE, which is
// what this change implements. Confirmed against packaging 26.3's
// SpecifierSet.filter for each scenario's package (PR description).
var intentionalDivergence = map[string]divergence{
	"requires_python/python-less-than-current": {
		reason: "pip enforces the whole Requires-Python specifier, upper bound included, and uv ignores the " +
			"upper bound",
		pins: nil, // pip: unsatisfiable
	},
	"prereleases/package-only-prereleases-in-range": {
		reason: "no final release of a is in range (only 0.1.0, excluded by a>0.1.0), so pip and current uv fall " +
			"back to the pre-release",
		pins: map[string]string{"a": "1.0.0a1"},
	},
	"prereleases/transitive-package-only-prereleases-in-range": {
		reason: "same fallback one level down: no final release of b is in range",
		pins:   map[string]string{"a": "0.1.0", "b": "1.0.0a1"},
	},
	"prereleases/transitive-prerelease-and-stable-dependency": {
		reason: "c's range (==2.0.0b1 intersected with >=1.0.0,<=3.0.0) admits no final release at all",
		pins:   map[string]string{"a": "1.0.0", "b": "1.0.0", "c": "2.0.0b1"},
	},
	"prereleases/transitive-prerelease-and-stable-dependency-many-versions": {
		reason: "c's range (>=2.0.0b1) excludes every final and every alpha, so the fallback picks the highest " +
			"beta in range",
		pins: map[string]string{"a": "1.0.0", "b": "1.0.0", "c": "2.0.0b9"},
	},
	"prereleases/transitive-prerelease-and-stable-dependency-many-versions-holes": {
		reason: "c's range excludes the only final and several pre-releases by name; the fallback picks the " +
			"highest surviving one",
		pins: map[string]string{"a": "1.0.0", "b": "1.0.0", "c": "2.0.0b4"},
	},
}

// resolvePackseScenario runs s against the real resolver and returns the raw
// result, shared by runPackseScenario and the intentionalDivergence check so
// both assert against the SAME resolve rather than running it twice.
func resolvePackseScenario(t *testing.T, s tomlScenario) (*resolver.Resolution, error) {
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
	return resolver.Resolve(context.Background(), reqs, idx, opts)
}

// matchOutcome reports whether resolving s produced wantSatisfiable and, when
// satisfiable, exactly wantPackages.
func matchOutcome(
	t *testing.T, res *resolver.Resolution, resolveErr error, wantSatisfiable bool, wantPackages map[string]string,
) (matched bool, detail string) {
	t.Helper()

	switch {
	case !wantSatisfiable:
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

	case len(wantPackages) > 0:
		if resolveErr != nil {
			return false, fmt.Sprintf("expected packages %v, but Resolve failed: %v", wantPackages, resolveErr)
		}
		got := pins(t, res)
		if !reflect.DeepEqual(got, wantPackages) {
			return false, fmt.Sprintf("Pinned = %v, want %v", got, wantPackages)
		}
		return true, ""

	default:
		if resolveErr != nil {
			return false, fmt.Sprintf("expected to resolve, but Resolve failed: %v", resolveErr)
		}
		return true, ""
	}
}

// runPackseScenario resolves s and reports whether the result matches
// packse's own expected outcome.
func runPackseScenario(t *testing.T, s tomlScenario) (matched bool, detail string) {
	t.Helper()
	res, resolveErr := resolvePackseScenario(t, s)
	return matchOutcome(t, res, resolveErr, s.Expected.Satisfiable, s.Expected.Packages)
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
	var pass, known, diverged, unsupported, outScope int

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

		if _, ok := unsupportedOption[name]; ok {
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
		div, isDivergence := intentionalDivergence[name]
		if isKnownFail && isDivergence {
			t.Errorf("%s: listed on both knownFail and intentionalDivergence", name)
		}

		t.Run(name, func(t *testing.T) {
			if isDivergence {
				// Reuse runPackseScenario's resolve, but assert pip's outcome
				// exactly rather than merely "disagrees with packse" -- the latter
				// would also pass if the harness itself broke.
				res, resolveErr := resolvePackseScenario(t, s)
				matched, detail := matchOutcome(t, res, resolveErr, div.pins != nil, div.pins)
				if !matched {
					t.Errorf("%s: intentional divergence from packse did not match pip's outcome (%s): %s",
						name, div.reason, detail)
				}
				matchedPackse, _ := matchOutcome(t, res, resolveErr, s.Expected.Satisfiable, s.Expected.Packages)
				if matchedPackse {
					t.Errorf("%s unexpectedly matched packse, remove it from intentionalDivergence", name)
				}
				return
			}

			if name == "extras/missing-extra" {
				// The harness can assert the warning here, so it does: the root
				// asked for a[extra], and 1.0.0 (the only version) does not
				// declare it.
				res, resolveErr := resolvePackseScenario(t, s)
				matched, detail := matchOutcome(t, res, resolveErr, s.Expected.Satisfiable, s.Expected.Packages)
				if !matched {
					t.Errorf("%s: %s", name, detail)
				}
				if resolveErr == nil {
					want := []resolver.MissingExtra{{
						Package:     index.NewPackageName("a"),
						Version:     version.MustParse("1.0.0"),
						Extra:       "extra",
						RequestedBy: resolver.Requester{Root: true},
					}}
					if !reflect.DeepEqual(res.MissingExtras, want) {
						t.Errorf("%s: MissingExtras = %+v, want %+v", name, res.MissingExtras, want)
					}
				}
				return
			}

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

		switch {
		case isDivergence:
			diverged++
		case isKnownFail:
			known++
		default:
			pass++
		}
	}

	for name := range knownFail {
		if !seen[name] {
			t.Errorf("knownFail names %q, which does not exist in testdata/packse", name)
		}
	}
	for name := range intentionalDivergence {
		if !seen[name] {
			t.Errorf("intentionalDivergence names %q, which does not exist in testdata/packse", name)
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

	t.Logf("packse: %d scenarios = %d pass + %d known-fail + %d divergence + %d unsupported + %d out-of-scope",
		len(scenarios), pass, known, diverged, unsupported, outScope)
}
