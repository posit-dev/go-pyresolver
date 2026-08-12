// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import "testing"

// TestCanonicalMerging: adjacent and overlapping spans must fuse, so that two
// spellings of one set compare Equal.
func TestCanonicalMerging(t *testing.T) {
	v := func(s string) bound { return atBound(mustV(t, s)) }

	adjacent := newSet(
		span{v("1.0"), v("2.0")},
		span{v("2.0"), v("3.0")},
	)
	whole := newSet(span{v("1.0"), v("3.0")})
	if !adjacent.Equal(whole) {
		t.Errorf("[1,2) u [2,3) should Equal [1,3); got %d spans vs %d",
			len(adjacent.spans), len(whole.spans))
	}
	if len(adjacent.spans) != 1 {
		t.Errorf("adjacent spans did not fuse: %d spans", len(adjacent.spans))
	}

	overlapping := newSet(
		span{v("1.0"), v("2.5")},
		span{v("2.0"), v("3.0")},
	)
	if !overlapping.Equal(whole) {
		t.Error("[1,2.5) u [2,3) should Equal [1,3)")
	}

	// Distinct spellings of the same version must fuse too: 1.0 == 1.0.0.
	spelled := newSet(
		span{v("1.0"), v("2.0")},
		span{v("2.0.0"), v("3.0")},
	)
	if !spelled.Equal(whole) {
		t.Error("a span boundary spelled 2.0.0 should fuse with one spelled 2.0")
	}

	// An empty span is dropped entirely.
	if !newSet(span{v("2.0"), v("1.0")}).IsEmpty() {
		t.Error("a reversed span should canonicalize to empty")
	}
}

func TestAllAndEmpty(t *testing.T) {
	if All().IsEmpty() {
		t.Error("All() must not be empty")
	}
	if !Empty().IsEmpty() {
		t.Error("Empty() must be empty")
	}
	if All().Equal(Empty()) {
		t.Error("All() must not Equal Empty()")
	}
}

// sampleSets returns a spread of sets to quantify the algebra laws over,
// including empty, universal, multi-span, and exotic-boundary cases.
func sampleSets(t *testing.T) []Set {
	t.Helper()
	at := func(s string) bound { return atBound(mustV(t, s)) }
	rel := func(s string, e edge) bound { return bound{v: mustV(t, s), edge: e} }

	return []Set{
		Empty(),
		All(),
		newSet(span{at("1.0"), at("2.0")}),
		newSet(span{negInf(), at("1.0")}),
		newSet(span{at("2.0"), posInf()}),
		newSet(span{at("1.0"), at("2.0")}, span{at("3.0"), at("4.0")}),
		newSet(span{at("1.0"), rel("1.0", edgeAboveLocals)}), // ==1.0
		newSet(span{rel("1.0", edgeAboveRelease), posInf()}), // >1.0
		newSet(span{rel("1.0", edgeBelowRelease), rel("2.0", edgeBelowRelease)}),
		newSet(span{at("1.0rc1"), at("1.0")}),
		newSet(span{at("1!0.1"), posInf()}),
		// ⚠️ The one span whose boundary is edgeAboveExact -- `==1.0+a`, the
		// only set that names a single local variant. Without it the sample
		// exercised four of the five edges, so the laws never saw the position
		// that sits between at(1.0+a) and at(1.0+b) and nothing pinned
		// Complement across it.
		newSet(span{at("1.0+a"), rel("1.0+a", edgeAboveExact)}),
		// A release segment past int64, so the laws run over a key the old
		// Atoi-based one truncated to nothing.
		newSet(span{at("1.0"), at("1.99999999999999999999")}),
	}
}

func TestAlgebraLaws(t *testing.T) {
	sets := sampleSets(t)

	for i, a := range sets {
		// Complement is an involution.
		if !a.Complement().Complement().Equal(a) {
			t.Errorf("set %d: complement is not an involution", i)
		}
		// a n a' is empty; a u a' is everything.
		if !a.Intersect(a.Complement()).IsEmpty() {
			t.Errorf("set %d: a n a' is not empty", i)
		}
		if !a.Union(a.Complement()).Equal(All()) {
			t.Errorf("set %d: a u a' is not All", i)
		}

		for j, b := range sets {
			// Commutativity.
			if !a.Intersect(b).Equal(b.Intersect(a)) {
				t.Errorf("sets %d,%d: Intersect not commutative", i, j)
			}
			if !a.Union(b).Equal(b.Union(a)) {
				t.Errorf("sets %d,%d: Union not commutative", i, j)
			}
			// De Morgan, both directions.
			if !a.Union(b).Complement().Equal(a.Complement().Intersect(b.Complement())) {
				t.Errorf("sets %d,%d: (a u b)' != a' n b'", i, j)
			}
			if !a.Intersect(b).Complement().Equal(a.Complement().Union(b.Complement())) {
				t.Errorf("sets %d,%d: (a n b)' != a' u b'", i, j)
			}

			for k, c := range sets {
				// Associativity.
				if !a.Intersect(b).Intersect(c).Equal(a.Intersect(b.Intersect(c))) {
					t.Errorf("sets %d,%d,%d: Intersect not associative", i, j, k)
				}
				if !a.Union(b).Union(c).Equal(a.Union(b.Union(c))) {
					t.Errorf("sets %d,%d,%d: Union not associative", i, j, k)
				}
			}
		}
	}
}
