// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// The set algebra is the resolver's inner loop: go-pubgrub intersects,
// complements and compares terms far more often than it asks the index
// anything, and every one of those operations walks bounds through cmpBound.
// These benchmarks measure that walk through the exported surface, so the
// numbers are comparable across a change to how a bound derives its sort key.

func benchSpecifiers(b *testing.B, s string) version.Specifiers {
	b.Helper()
	ss, err := version.NewSpecifiers(s)
	if err != nil {
		b.Fatalf("NewSpecifiers(%q): %v", s, err)
	}
	return ss
}

func benchSet(b *testing.B, s string) Set {
	b.Helper()
	set, err := FromSpecifiers(benchSpecifiers(b, s))
	if err != nil {
		b.Fatalf("FromSpecifiers(%q): %v", s, err)
	}
	return set
}

// BenchmarkFromSpecifiers is the CONSTRUCTION side. A key derived once per
// bound has to be paid for here, so this is the number that would go up if the
// trade were a bad one.
func BenchmarkFromSpecifiers(b *testing.B) {
	for _, spec := range []string{">=1.0", ">=1.0,<2.0,!=1.5", "~=1.4.2"} {
		b.Run(spec, func(b *testing.B) {
			ss := benchSpecifiers(b, spec)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := FromSpecifiers(ss); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkIntersect is the operation the solver runs most.
func BenchmarkIntersect(b *testing.B) {
	a := benchSet(b, ">=1.0,<2.0")
	c := benchSet(b, ">=1.5,!=1.7,<3.0")
	b.ReportAllocs()
	for b.Loop() {
		_ = a.Intersect(c)
	}
}

// BenchmarkIntersectEmptyOperand is the SAME operation with an empty operand,
// and it must not allocate.
//
// ⚠️ A BENCHMARK THAT ONLY MEASURES THE NON-EMPTY CASE IS WHY SIZING THE SPAN
// SLICE UP FRONT SHIPPED AS A REGRESSION HERE. Intersecting with Empty keeps
// nothing, so the loop never runs and a slice allocated before it is discarded
// whole -- and that path is reachable in the solver, because Difference(a, b)
// is a.Intersect(b.Complement()) and All().Complement() is empty. The LHS has
// eight spans on purpose: the wasted allocation was sized from both operands,
// so a one-span LHS would barely show it.
func BenchmarkIntersectEmptyOperand(b *testing.B) {
	a := benchSet(b, ">=1.0,!=1.1,!=1.2,!=1.3,!=1.4,!=1.5,!=1.6,!=1.7,<2.0")
	if len(a.spans) != 8 {
		b.Fatalf("LHS has %d spans, want 8", len(a.spans))
	}
	empty := Empty()
	b.ReportAllocs()
	for b.Loop() {
		_ = a.Intersect(empty)
	}
}

// BenchmarkComplement covers the other half: every negative term is one.
func BenchmarkComplement(b *testing.B) {
	s := benchSet(b, ">=1.0,<2.0,!=1.5")
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Complement()
	}
}

// BenchmarkComplementAll is the complement whose RESULT is empty, the canonical
// route to Empty, and it must not allocate either.
//
// All() fills the order, so there is no gap below its span and no tail above
// it: the loop appends nothing. Sizing the gap slice before the loop spent a
// span-sized allocation on every one of these.
func BenchmarkComplementAll(b *testing.B) {
	s := All()
	b.ReportAllocs()
	for b.Loop() {
		if got := s.Complement(); !got.IsEmpty() {
			b.Fatalf("All().Complement() = %s, want empty", got)
		}
	}
}

// BenchmarkContains is a version probed against a set's bounds with no set
// built in the middle: the closest thing to a direct comparison-ladder
// measurement the exported surface offers.
//
// The two cases bound the lazy public derivation from both ends. cross-group
// probes 1.4.2, whose release group ties NO bound of the set, so ensurePub
// never runs -- the BEST case for the verPos path, and also the common one.
// same-group probes 1.0.post1, which ties 1.0's group and descends to the
// public comparison, so the render (and the ladder's full depth) is paid --
// the WORST case. Quote them together or not at all.
func BenchmarkContains(b *testing.B) {
	s := benchSet(b, ">=1.0,<2.0,!=1.5")
	for _, tc := range []struct{ name, probe string }{
		{"cross-group", "1.4.2"},
		{"same-group", "1.0.post1"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			v, err := version.Parse(tc.probe)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				_ = s.Contains(v)
			}
		})
	}
}

// BenchmarkEqual is what go-pubgrub calls to decide whether two
// incompatibilities are the same, on sets it has already built.
func BenchmarkEqual(b *testing.B) {
	x := benchSet(b, ">=1.0,<2.0,!=1.5")
	y := benchSet(b, ">=1.0,<2.0,!=1.5")
	b.ReportAllocs()
	for b.Loop() {
		_ = x.Equal(y)
	}
}
