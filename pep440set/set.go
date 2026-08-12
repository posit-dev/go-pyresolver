// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import "sort"

// span is the half-open position interval [lo, hi).
type span struct{ lo, hi bound }

// Set is a canonical union of spans: sorted, disjoint, non-adjacent, no empties.
//
// The zero value is the empty set.
type Set struct{ spans []span }

// newSet canonicalizes. Every constructor and every operation returns through
// it, because versionset.Set requires Equal to hold across representations.
func newSet(spans ...span) Set {
	kept := spans[:0:0]
	for _, sp := range spans {
		if cmpBound(sp.lo, sp.hi) < 0 {
			kept = append(kept, sp)
		}
	}
	if len(kept) == 0 {
		return Set{}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if c := cmpBound(kept[i].lo, kept[j].lo); c != 0 {
			return c < 0
		}
		return cmpBound(kept[i].hi, kept[j].hi) < 0
	})

	out := []span{kept[0]}
	for _, sp := range kept[1:] {
		last := &out[len(out)-1]
		// Overlapping OR merely adjacent (lo == last.hi) must fuse: a gap of
		// zero width is not a gap, and leaving it splits one set into two
		// representations that would fail Equal.
		if cmpBound(sp.lo, last.hi) <= 0 {
			if cmpBound(sp.hi, last.hi) > 0 {
				last.hi = sp.hi
			}
			continue
		}
		out = append(out, sp)
	}
	return Set{spans: out}
}

// All is every version.
func All() Set { return Set{spans: []span{{negInf(), posInf()}}} }

// Empty is no versions.
func Empty() Set { return Set{} }

// IsEmpty implements versionset.Set.
func (s Set) IsEmpty() bool { return len(s.spans) == 0 }

// Equal implements versionset.Set. Both sides are canonical, so this is a
// structural comparison.
func (s Set) Equal(other Set) bool {
	if len(s.spans) != len(other.spans) {
		return false
	}
	for i := range s.spans {
		if cmpBound(s.spans[i].lo, other.spans[i].lo) != 0 ||
			cmpBound(s.spans[i].hi, other.spans[i].hi) != 0 {
			return false
		}
	}
	return true
}

func (s Set) containsBound(b bound) bool {
	for _, sp := range s.spans {
		if cmpBound(sp.lo, b) <= 0 && cmpBound(b, sp.hi) < 0 {
			return true
		}
	}
	return false
}
