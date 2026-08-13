// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// verPosVersions is the version side of the agreement checks: every spelling
// the ordering grid anchors a bound to, the canonicalization aliases that must
// share a position, and the zero Version.
func verPosVersions(t *testing.T) []version.Version {
	t.Helper()
	spellings := []string{
		"1.0", "1.0.0", "1.0.0.0", "1.00", "1",
		"1.0.dev0", "1.0.dev1",
		"1.0a1", "1.0a1+aaa", "1.0a1.post0.dev0", "1.0a2", "1.0b1", "1.0rc1",
		"1.0+aaa", "1.0+zzz", "1.0+ubuntu.1",
		"1.0.post0.dev0", "1.0.post1", "1.0.post1+aaa",
		"1.0.1", "1.1", "1.5", "2.0",
		"1.9223372036854775807", "1.9223372036854775808",
		"1.99999999999999999999",
		"9223372036854775808", "99999999999999999999.0",
		"1!0.1", "1!1.0", "01!1.0", "0!1.0", "9223372036854775808!1.0",
	}
	out := make([]version.Version, 0, len(spellings)+1)
	for _, s := range spellings {
		out = append(out, mustV(t, s))
	}
	out = append(out, version.Version{})
	return out
}

// TestCmpVerBoundAgreesWithCmpBound holds the specialized ladder to the
// general one: for every version above and every bound in the full ordering
// grid, cmpVerBound must answer exactly as cmpBound does for the materialized
// atBound. One verPos is reused across the whole row, as Contains reuses it
// across a set's spans, so the lazy public derivation is exercised in every
// order it can happen in.
func TestCmpVerBoundAgreesWithCmpBound(t *testing.T) {
	entries := ascendingPositions(t)
	for _, v := range verPosVersions(t) {
		var p verPos
		p.init(v)
		materialized := atBound(v)
		for _, e := range entries {
			want := cmpBound(materialized, e.b)
			if got := cmpVerBound(&p, e.b); got != want {
				t.Errorf("cmpVerBound(at(%s), %s) = %d, cmpBound = %d",
					v.String(), e.name, got, want)
			}
		}
	}
}

// TestVerPosReinit pins that re-initializing a USED verPos discards the
// previous version's lazily derived public spelling. No production caller
// re-inits today, but init's name promises it works, and the demonstrated
// failure mode -- hoisting one verPos out of a per-candidate loop -- returned
// the previous version's answer while every fresh-verPos test stayed green.
func TestVerPosReinit(t *testing.T) {
	entries := ascendingPositions(t)
	versions := verPosVersions(t)
	var p verPos
	for _, v := range versions {
		p.init(v)
		materialized := atBound(v)
		for _, e := range entries {
			// Probe first WITHOUT re-initing, so p carries whatever pub state
			// the previous version left behind if init failed to clear it.
			if got, want := cmpVerBound(&p, e.b), cmpBound(materialized, e.b); got != want {
				t.Fatalf("reused verPos: cmpVerBound(at(%s), %s) = %d, want %d",
					v.String(), e.name, got, want)
			}
		}
	}
}

// TestContainsAgreesWithContainsBound holds the exported fast path to the
// reference path over sets with every span shape construct.go produces.
func TestContainsAgreesWithContainsBound(t *testing.T) {
	specs := []string{
		">=1.0", ">1.0", "<=1.0", "<1.0", "==1.0", "!=1.0",
		"==1.0.*", "!=1.0.*", "~=1.0.1", "~=1.0",
		">=1.0,<2.0", ">=1.0,<2.0,!=1.5", ">=1.0rc1", ">1.0a1", "<1.0.post1",
		"==1.0+aaa", "!=1.0+aaa", ">=1!0.1", "<9223372036854775808",
		">=0.0.dev0", "",
	}
	versions := verPosVersions(t)

	for _, spec := range specs {
		ss, err := version.NewSpecifiers(spec)
		if err != nil {
			t.Fatalf("NewSpecifiers(%q): %v", spec, err)
		}
		s, err := FromSpecifiers(ss)
		if err != nil {
			t.Fatalf("FromSpecifiers(%q): %v", spec, err)
		}
		for _, cand := range []Set{s, s.Complement()} {
			for _, v := range versions {
				want := cand.containsBound(atBound(v))
				if got := cand.Contains(v); got != want {
					t.Errorf("set %q (%s): Contains(%s) = %v, containsBound = %v",
						spec, cand.String(), v.String(), got, want)
				}
			}
		}
	}
}
