// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

func mustV(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestBoundOrdering pins the total order on positions. Each entry must be
// strictly less than the one after it.
func TestBoundOrdering(t *testing.T) {
	type entry struct {
		name string
		b    bound
	}
	v := func(s string) version.Version { return mustV(t, s) }

	ascending := []entry{
		{"-inf", negInf()},
		{"belowRelease(1.0)", bound{v: v("1.0"), edge: edgeBelowRelease}},
		{"at(1.0.dev1)", bound{v: v("1.0.dev1"), edge: edgeAt}},
		{"at(1.0a1)", bound{v: v("1.0a1"), edge: edgeAt}},
		{"at(1.0rc1)", bound{v: v("1.0rc1"), edge: edgeAt}},
		{"at(1.0)", bound{v: v("1.0"), edge: edgeAt}},
		{"aboveExact(1.0)", bound{v: v("1.0"), edge: edgeAboveExact}},
		{"at(1.0+aaa)", bound{v: v("1.0+aaa"), edge: edgeAt}},
		{"aboveExact(1.0+aaa)", bound{v: v("1.0+aaa"), edge: edgeAboveExact}},
		{"at(1.0+zzz)", bound{v: v("1.0+zzz"), edge: edgeAt}},
		{"aboveExact(1.0+zzz)", bound{v: v("1.0+zzz"), edge: edgeAboveExact}},
		{"aboveLocals(1.0)", bound{v: v("1.0"), edge: edgeAboveLocals}},
		{"at(1.0.post0.dev0)", bound{v: v("1.0.post0.dev0"), edge: edgeAt}},
		{"at(1.0.post1)", bound{v: v("1.0.post1"), edge: edgeAt}},
		{"aboveRelease(1.0)", bound{v: v("1.0"), edge: edgeAboveRelease}},
		{"belowRelease(1.0.1)", bound{v: v("1.0.1"), edge: edgeBelowRelease}},
		{"at(1.0.1)", bound{v: v("1.0.1"), edge: edgeAt}},
		{"belowRelease(1!0.1)", bound{v: v("1!0.1"), edge: edgeBelowRelease}},
		{"+inf", posInf()},
	}

	for i := 0; i < len(ascending); i++ {
		for j := 0; j < len(ascending); j++ {
			got := cmpBound(ascending[i].b, ascending[j].b)
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got != want {
				t.Errorf("cmpBound(%s, %s) = %d, want %d",
					ascending[i].name, ascending[j].name, got, want)
			}
		}
	}
}

// TestBoundOrderingPastInt64 pins the ordering of release segments and epochs
// that do not fit in an int64.
//
// ⚠️ These are the positions the old strconv.Atoi key silently collapsed. It
// broke out of its parse loop on the first oversized segment, so
// 99999999999999999999.0 keyed as the EMPTY release and 1.99999999999999999999
// keyed as 1 -- both sorting below 1.0. The order is deliberately checked at
// three magnitudes around 2^63, because a key that is merely wider (int64 ->
// some larger fixed width) still fails somewhere.
func TestBoundOrderingPastInt64(t *testing.T) {
	v := func(s string) version.Version { return mustV(t, s) }

	ascending := []string{
		"1.0",
		"1.5",
		"1.9223372036854775807",
		"1.9223372036854775808",
		"1.99999999999999999999",
		"2.0",
		"9223372036854775807",
		"9223372036854775808",
		"99999999999999999999.0",
		"99999999999999999999.1",
		"1!1.0",
		"9223372036854775808!1.0",
	}

	for i := range ascending {
		for j := range ascending {
			a := bound{v: v(ascending[i]), edge: edgeAt}
			b := bound{v: v(ascending[j]), edge: edgeAt}
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := cmpBound(a, b); got != want {
				t.Errorf("cmpBound(at(%s), at(%s)) = %d, want %d",
					ascending[i], ascending[j], got, want)
			}
		}
	}

	// Leading zeros are not significant, at any width: the key compares digit
	// runs by LENGTH first, so a padded segment would otherwise look larger.
	if cmpBound(
		bound{v: v("1.099999999999999999990"), edge: edgeAt},
		bound{v: v("1.0099999999999999999990"), edge: edgeAt}) != 0 {
		t.Error("leading zeros must not change a release segment's value")
	}
	if cmpBound(
		bound{v: v("1.00000000000000000001"), edge: edgeAt},
		bound{v: v("1.1"), edge: edgeAt}) != 0 {
		t.Error("a segment of leading zeros followed by 1 is the segment 1")
	}
}

// TestLeadingZeroSpellings replaces the leading-zero stripping this package
// used to do for itself.
//
// It stripped because it built its key out of DIGIT RUNS, compared
// length-first, where "007" would have sorted above "7". The key now comes from
// gpp's parsed release segments, which are math/big integers: "007" and "7" are
// the same integer before a key exists, and there is no run to strip. The
// concern is gone rather than moved, but the BEHAVIOUR it protected is not
// optional, so it is asserted here through the real path instead of through a
// helper.
//
// Every pair below must occupy one position: same value, different spelling,
// at widths on both sides of int64 and uint64.
func TestLeadingZeroSpellings(t *testing.T) {
	pairs := [][2]string{
		{"1.7", "1.007"},
		{"1.100", "1.0100"},
		{"1.1", "1.00000000000000000001"},
		{"1.099999999999999999990", "1.0099999999999999999990"},
		{"0!1.0", "00!1.0"},
		{"7!1.0", "007!1.0"},
	}
	for _, p := range pairs {
		a := bound{v: mustV(t, p[0]), edge: edgeAt}
		b := bound{v: mustV(t, p[1]), edge: edgeAt}
		if got := cmpBound(a, b); got != 0 {
			t.Errorf("cmpBound(at(%s), at(%s)) = %d, want 0", p[0], p[1], got)
		}
	}

	// ...and the spelling must not have flattened a real difference either:
	// 1.007 is 1.7, NOT 1.70 or 1.0.7.
	for _, p := range [][2]string{{"1.007", "1.70"}, {"1.007", "1.0.7"}} {
		a := bound{v: mustV(t, p[0]), edge: edgeAt}
		b := bound{v: mustV(t, p[1]), edge: edgeAt}
		if got := cmpBound(a, b); got == 0 {
			t.Errorf("cmpBound(at(%s), at(%s)) = 0; the two are different releases", p[0], p[1])
		}
	}
}

// TestBoundEqualSpellings: 1.0 and 1.0.0 are the same version, so bounds
// built from them must compare equal. Canonicalization depends on this.
func TestBoundEqualSpellings(t *testing.T) {
	a := bound{v: mustV(t, "1.0"), edge: edgeAt}
	b := bound{v: mustV(t, "1.0.0"), edge: edgeAt}
	if cmpBound(a, b) != 0 {
		t.Errorf("cmpBound(at(1.0), at(1.0.0)) = %d, want 0", cmpBound(a, b))
	}
}

// TestBoundAboveLocalsIgnoresAnchorLocal: edgeAboveLocals is a property of the
// public version, so anchoring it to 1.0+a names the same position as
// anchoring it to 1.0. edgeAboveExact is the edge that does not.
func TestBoundAboveLocalsIgnoresAnchorLocal(t *testing.T) {
	plain := bound{v: mustV(t, "1.0"), edge: edgeAboveLocals}
	local := bound{v: mustV(t, "1.0+a"), edge: edgeAboveLocals}
	if cmpBound(plain, local) != 0 {
		t.Errorf("cmpBound(aboveLocals(1.0), aboveLocals(1.0+a)) = %d, want 0",
			cmpBound(plain, local))
	}

	exact := bound{v: mustV(t, "1.0+a"), edge: edgeAboveExact}
	if cmpBound(exact, plain) >= 0 {
		t.Error("aboveExact(1.0+a) must sit below aboveLocals(1.0)")
	}
}
