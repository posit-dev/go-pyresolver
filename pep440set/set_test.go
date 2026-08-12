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
