// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

// IsSubsetOf reports whether every span of s lies inside one span of other.
// Same answer as s.Intersect(other.Complement()).IsEmpty(), with no allocation.
// It is go-pubgrub's optional SubsetOfer fast path.
func (s Set) IsSubsetOf(other Set) bool {
	j := 0
	for _, a := range s.spans {
		for j < len(other.spans) && cmpBound(other.spans[j].hi, a.lo) <= 0 {
			j++
		}
		if j == len(other.spans) {
			return false
		}
		b := other.spans[j]
		if cmpBound(b.lo, a.lo) > 0 || cmpBound(a.hi, b.hi) > 0 {
			return false
		}
	}
	return true
}

// Difference returns s minus other, without building other's complement.
// It is go-pubgrub's optional Differencer fast path.
func (s Set) Difference(other Set) Set {
	var out []span
	j := 0
	for _, a := range s.spans {
		for j < len(other.spans) && cmpBound(other.spans[j].hi, a.lo) <= 0 {
			j++
		}
		cur := a.lo
		for k := j; k < len(other.spans) && cmpBound(other.spans[k].lo, a.hi) < 0; k++ {
			b := other.spans[k]
			if cmpBound(cur, b.lo) < 0 {
				out = appendSpan(out, len(s.spans)+len(other.spans), span{cur, b.lo})
			}
			if cmpBound(b.hi, cur) > 0 {
				cur = b.hi
			}
		}
		if cmpBound(cur, a.hi) < 0 {
			out = appendSpan(out, len(s.spans)+len(other.spans), span{cur, a.hi})
		}
	}
	return newSet(out...)
}
