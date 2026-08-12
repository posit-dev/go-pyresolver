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
	}
}

// operandGrid is the right-hand sides worth constraining on.
func operandGrid() []string {
	return []string{
		"1.0", "1.0.0", "1.0rc1", "1.0.post1", "1.0+a", "1.0.1",
		"2.0", "2.2", "2.2.3", "1!1.0", "1.*", "2.*", "1.0.*",
	}
}

func operatorGrid() []string {
	return []string{"", "==", "!=", "<", "<=", ">", ">=", "~="}
}

// TestDifferentialAgainstCheck is the acceptance criterion for this package.
//
// It runs the whole grid twice: once with default options and once with
// version.WithPreRelease(true), comparing each pass against that Specifiers'
// own Check. Check is pure matching and ignores the pre-release policy, so the
// two passes must agree with each other as well as with the set -- and that is
// the point of the second pass, since a mapping that quietly folded
// pre-release SELECTION into the spans would diverge here.
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
		{name: "default"},
		{name: "prereleases", opts: []version.SpecifierOption{version.WithPreRelease(true)}},
	}

	var compared, skipped int
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
				}
			}
		}
	}

	t.Logf("compared %d (specifier, version) pairs; skipped %d specifiers",
		compared, skipped)
	if compared < 1000 {
		t.Errorf("only %d comparisons ran; the grid is not exercising the mapping",
			compared)
	}
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
	}

	vs := make([]version.Version, 0, len(versions))
	for _, s := range versions {
		vs = append(vs, mustV(t, s))
	}

	var compared, skipped int
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
			}
		}
	}

	t.Logf("compared %d (specifier, version) pairs; skipped %d specifiers",
		compared, skipped)
	if compared < 10000 {
		t.Errorf("only %d comparisons ran; the wide grid shrank", compared)
	}
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
