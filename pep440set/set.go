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

// IsEmpty implements versionset.Set: it reports whether the set holds no
// POSITIONS.
//
// ⚠️ IT CAN REPORT false FOR A SET THAT HOLDS NO VERSION. A span between two
// positions with no version between them is a real, non-empty span, and
// Contains is false for every version in it. `>=1.0,!=1.0,<1.0.post0.dev0`
// builds one: it is the region above every local variant of 1.0 and below the
// first post-release of 1.0, which no version can occupy.
//
// That is the intended contract. go-pubgrub uses IsEmpty to decide whether a
// term is satisfiable, and the direction of the imprecision is safe: the
// solver explores a term it will find no candidate for, rather than pruning
// one it should have explored.
func (s Set) IsEmpty() bool { return len(s.spans) == 0 }

// Equal implements versionset.Set. Both sides are canonical, so this is a
// structural comparison.
//
// ⚠️ EQUAL IS EXACT ON POSITIONS, NOT ON VERSIONS. Two sets that admit exactly
// the same versions can compare unequal when they differ only across a gap that
// holds no version. `<=1.0` unioned with `>=1.0.post0.dev0` is such a pair
// against All(): the two spans meet at "above every 1.0+local" versus "at
// 1.0.post0.dev0", between which no version exists, so the union admits every
// version there is and still canonicalizes to TWO spans rather than one.
//
// Position equality is what go-pubgrub needs -- it compares incompatibilities
// for identity, and an Equal that merged distinguishable representations would
// make derivations collide. The cost is that a set can be un-mergeable without
// being distinguishable by Contains, which is the same imprecision IsEmpty
// carries and is safe in the same direction.
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

// Union implements versionset.Set. Canonicalization does the merging.
func (s Set) Union(other Set) Set {
	all := make([]span, 0, len(s.spans)+len(other.spans))
	all = append(all, s.spans...)
	all = append(all, other.spans...)
	return newSet(all...)
}

// Intersect implements versionset.Set.
func (s Set) Intersect(other Set) Set {
	var out []span
	for _, a := range s.spans {
		for _, b := range other.spans {
			lo, hi := a.lo, a.hi
			if cmpBound(b.lo, lo) > 0 {
				lo = b.lo
			}
			if cmpBound(b.hi, hi) < 0 {
				hi = b.hi
			}
			if cmpBound(lo, hi) < 0 {
				out = append(out, span{lo, hi})
			}
		}
	}
	return newSet(out...)
}

// Complement implements versionset.Set.
func (s Set) Complement() Set {
	if len(s.spans) == 0 {
		return All()
	}
	var out []span
	cursor := negInf()
	for _, sp := range s.spans {
		if cmpBound(cursor, sp.lo) < 0 {
			out = append(out, span{cursor, sp.lo})
		}
		cursor = sp.hi
	}
	if cmpBound(cursor, posInf()) < 0 {
		out = append(out, span{cursor, posInf()})
	}
	return newSet(out...)
}
