// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// versionGrid spans the exotic corners of PEP 440: epochs, dev/pre/post
// segments, local labels, and multi-segment releases.
func versionGrid() []string {
	return []string{
		"0.9", "1.0.dev0", "1.0.dev1", "1.0a1", "1.0b1", "1.0rc1",
		"1.0", "1.0+a", "1.0+zzz", "1.0.post0", "1.0.post0.dev0",
		"1.0.post1", "1.0.post1+l", "1.0.0", "1.0.0.0", "1.0.1",
		"1.0.1rc1", "1.1", "1.9.9", "2.0.dev0", "2.0rc1", "2.0",
		"2.0+local", "2.2", "2.2.3", "2.9", "3.0", "1!0.1", "1!1.0",
		// A post-release, and a local, OF a pre-release: `>1.0rc1` rejects
		// both, and the boundary that expresses it is not the one a plain
		// release needs. Without these rows the operand "1.0rc1" only ever
		// exercises the easy side of the `>` guard.
		"1.0rc1+l", "1.0rc1.post1", "1.0rc2.dev0",
		// A pre-release of a post-release: `<1.0.post1` rejects it, which is
		// the post arm of the same guard.
		"1.0.post1.dev0",
		// ⚠️ Release segments ABOVE 2^63. bound.go's key used to parse each
		// segment with strconv.Atoi and stop at the first failure, so these
		// keyed as a PREFIX of their real release and sorted below everything:
		// `<1.5` admitted 1.99999999999999999999 and `>=1.5` rejected it. PEP
		// 440 caps neither a segment nor an epoch, and gpp holds both as
		// arbitrary-precision integers, so the grid has to reach past int64.
		"1.99999999999999999999", "99999999999999999999.1",
		"9223372036854775808",
	}
}

// operandGrid is the right-hand sides worth constraining on.
func operandGrid() []string {
	return []string{
		"1.0", "1.0.0", "1.0rc1", "1.0.post1", "1.0+a", "1.0.1",
		"2.0", "2.2", "2.2.3", "1!1.0", "1.*", "2.*", "1.0.*",
		// ⚠️ ALIAS SPELLINGS OF A PRE- OR POST-RELEASE. Under `~=` these are
		// NOT interchangeable with the canonical spellings, because the prefix
		// comes from the raw operand text and upstream's suffix test misses
		// them: `~=1.0c1` is `==1.0.*` where `~=1.0rc1` is `==1.*`, and the
		// case-sensitive test makes `~=0.0.posT` differ from `~=0.0.post0`.
		// The grid held only canonical spellings, so a mapping derived from the
		// PARSED operand -- which cannot see the difference -- passed it while
		// disagreeing with Check on all seven.
		"1.0c1", "1.0.c1", "1.0.pre1", "1.0.preview1", "1.0.r1", "1.0.rev1",
		"0.0.posT",
		// An alias pre AND an alias post, the one operand shape whose derived
		// ~= prefix is itself a pre-release rather than a plain release.
		"1.0.pre1.r1",
		// Past int64, on both sides of the dot, plus 2^63 exactly.
		"99999999999999999999.0", "1.0.99999999999999999999",
		"9223372036854775808", "99999999999999999999.*",
	}
}

func operatorGrid() []string {
	return []string{"", "==", "!=", "<", "<=", ">", ">=", "~="}
}

// checkPerOperatorFloor fails when any one operator stopped being exercised.
//
// ⚠️ A SINGLE GLOBAL FLOOR CANNOT DO THIS, which is why there no longer is
// one. The grids compare tens of thousands of pairs, so an operator that
// starts returning ErrUnrepresentable for every operand -- the cheapest way to
// make a differential failure disappear -- removes a few percent of the total
// and leaves the run green. Per-operator floors put every operator's own
// coverage on the record.
func checkPerOperatorFloor(t *testing.T, comparedBy map[string]int, floor int) {
	t.Helper()
	for _, op := range operatorGrid() {
		name := op
		if name == "" {
			name = "<empty>"
		}
		t.Logf("operator %-7s compared %d pairs", name, comparedBy[op])
		if comparedBy[op] < floor {
			t.Errorf("operator %s compared only %d pairs, want at least %d: "+
				"it is no longer being exercised", name, comparedBy[op], floor)
		}
	}
}

// TestDifferentialAgainstCheck is the acceptance criterion for this package.
//
// It runs the whole grid under all three pre-release policies, comparing each
// pass against that Specifiers' own Check. Check is pure matching and ignores
// the policy, so the three passes must agree with each other as well as with
// the set -- and that is the point of the extra passes, since a mapping that
// quietly folded pre-release SELECTION into the spans would diverge here.
//
// PreReleasesInclude is what the deprecated WithPreRelease(true) maps to.
func TestDifferentialAgainstCheck(t *testing.T) {
	versions := make([]version.Version, 0, len(versionGrid()))
	for _, s := range versionGrid() {
		v, err := version.Parse(s)
		if err != nil {
			t.Fatalf("grid version %q: %v", s, err)
		}
		versions = append(versions, v)
	}

	passes := []struct {
		name string
		opts []version.SpecifierOption
	}{
		{name: "auto"},
		{name: "include", opts: []version.SpecifierOption{
			version.WithPreReleases(version.PreReleasesInclude)}},
		{name: "exclude", opts: []version.SpecifierOption{
			version.WithPreReleases(version.PreReleasesExclude)}},
	}

	var compared, skipped int
	comparedBy := map[string]int{}
	for _, pass := range passes {
		for _, op := range operatorGrid() {
			for _, operand := range operandGrid() {
				spec := op + operand

				ss, err := version.NewSpecifiers(spec, pass.opts...)
				if err != nil {
					// gpp rejects the combination (e.g. ~=1.*); nothing to compare.
					skipped++
					continue
				}
				set, err := FromSpecifiers(ss)
				if err != nil {
					if errors.Is(err, ErrUnrepresentable) {
						skipped++
						continue
					}
					t.Errorf("[%s] %s: FromSpecifiers: %v", pass.name, spec, err)
					continue
				}

				for _, v := range versions {
					want := ss.Check(v)
					got := set.Contains(v)
					if got != want {
						t.Errorf("[%s] %s vs %s: Contains=%v Check=%v",
							pass.name, spec, v.Original(), got, want)
					}
					compared++
					comparedBy[op]++
				}
			}
		}
	}

	t.Logf("compared %d (specifier, version) pairs; skipped %d specifiers",
		compared, skipped)
	// `~=` is the floor-setting operator: it refuses every wildcard operand and
	// every local one, so it always compares the fewest pairs.
	checkPerOperatorFloor(t, comparedBy, 1500)
}

// TestDifferentialWideGrid widens both axes well past the hand-picked grid
// above: every operator against 32 operands and 57 versions, including the
// shapes no real package publishes -- a post-release of a pre-release, a
// pre-release of a post-release, an epoch with a wildcard, a zero-padded
// release segment, and a five-segment release.
//
// The narrow grid is the readable specification of the mapping; this one is
// the search for the case the specification did not think of.
func TestDifferentialWideGrid(t *testing.T) {
	operands := []string{
		"1.0", "1.0.0", "1.0.0.0", "1", "1.0rc1", "1.0a1", "1.0b2", "1.0.dev0",
		"1.0rc1.dev0", "1.0.post1", "1.0.post0", "1.0.post1.dev0", "1.0rc1.post1",
		"1.0+a", "1.0+zzz", "1.0.1", "2.0", "2.2", "2.2.3", "0.9",
		"1!1.0", "1!1.0rc1", "1!1.0.post1", "1.*", "2.*", "1.0.*", "1.0.0.*",
		"1!1.*", "10.2.*", "1.0.10", "1.00.1", "1.0.1.2.3",
		// Alias spellings, which `~=` does not treat as equivalent to the
		// canonical ones, plus the `v` prefix, whose derived ~= prefix ("0!v1")
		// is not a version at all.
		"1.0c1", "1.0.c1", "1.0.pre1", "1.0.preview1", "1.0.r1", "1.0.rev1",
		"1.0.pre1.r1", "1.0c1.rev1", "0.0.posT", "1.0.POST1", "v1.0",
		// Past int64.
		"99999999999999999999.0", "1.0.99999999999999999999",
		"9223372036854775808", "99999999999999999999.*",
		"1.0rc99999999999999999999",
	}
	versions := []string{
		"0.9", "0.9.9", "1.0.dev0", "1.0.dev1", "1.0a1", "1.0a2", "1.0a2.dev0",
		"1.0b1", "1.0b2", "1.0rc1", "1.0rc1.dev0", "1.0rc1+l", "1.0rc1.post0",
		"1.0rc1.post1", "1.0rc1.post1.dev0", "1.0rc2", "1.0rc2.dev0", "1.0rc10",
		"1.0", "1.0+a", "1.0+b", "1.0+zzz", "1.0.0", "1.0.0.0", "1.0.0.1",
		"1.0.0rc1", "1.0.post0", "1.0.post0.dev0", "1.0.post0+l", "1.0.post1",
		"1.0.post1.dev0", "1.0.post1+l", "1.0.post2", "1.0.post10", "1.0.1",
		"1.0.1rc1", "1.0.10", "1.0.1.2.3", "1.1", "1.9.9", "1.10",
		"2.0.dev0", "2.0rc1", "2.0", "2.0+local", "2.0.post1", "2.2", "2.2.3",
		"2.2.3.1", "2.9", "3.0", "10.2.1", "1!0.1", "1!1.0", "1!1.0rc1",
		"1!1.0.post1", "1!2.0",
		// Past int64, on both sides of the dot and in the epoch.
		"1.99999999999999999999", "99999999999999999999.0",
		"99999999999999999999.1", "9223372036854775808",
		"9223372036854775807", "99999999999999999999!1.0",
		"1.0rc99999999999999999999",
	}

	vs := make([]version.Version, 0, len(versions))
	for _, s := range versions {
		vs = append(vs, mustV(t, s))
	}

	var compared, skipped int
	comparedBy := map[string]int{}
	for _, op := range operatorGrid() {
		for _, operand := range operands {
			spec := op + operand

			ss, err := version.NewSpecifiers(spec)
			if err != nil {
				skipped++
				continue
			}
			set, err := FromSpecifiers(ss)
			if err != nil {
				if errors.Is(err, ErrUnrepresentable) {
					skipped++
					continue
				}
				t.Errorf("%s: FromSpecifiers: %v", spec, err)
				continue
			}

			for _, v := range vs {
				if got, want := set.Contains(v), ss.Check(v); got != want {
					t.Errorf("%s vs %s: Contains=%v Check=%v",
						spec, v.Original(), got, want)
				}
				compared++
				comparedBy[op]++
			}
		}
	}

	t.Logf("compared %d (specifier, version) pairs; skipped %d specifiers",
		compared, skipped)
	checkPerOperatorFloor(t, comparedBy, 1500)
}

// TestDifferentialConjunctions covers multi-specifier sets, where an
// intersection bug hides that single-specifier tests cannot see.
func TestDifferentialConjunctions(t *testing.T) {
	specs := []string{
		">=1.0,<2.0", ">1.0,<=2.0", ">=1.0,!=1.0.1", "~=2.2,!=2.2.3",
		">=1.0rc1,<2.0", "!=1.0,!=2.0", ">=1!1.0,<1!2.0", "==1.*,!=1.0",
	}
	for _, spec := range specs {
		ss, err := version.NewSpecifiers(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		set, err := FromSpecifiers(ss)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		for _, s := range versionGrid() {
			v := mustV(t, s)
			if got, want := set.Contains(v), ss.Check(v); got != want {
				t.Errorf("%s vs %s: Contains=%v Check=%v", spec, s, got, want)
			}
		}
	}
}
