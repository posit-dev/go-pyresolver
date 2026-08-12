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
