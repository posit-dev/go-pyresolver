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

// BenchmarkComplement covers the other half: every negative term is one.
func BenchmarkComplement(b *testing.B) {
	s := benchSet(b, ">=1.0,<2.0,!=1.5")
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Complement()
	}
}

// BenchmarkContains is a bound compared against a set's bounds with no set
// built in the middle: the closest thing to a direct cmpBound measurement the
// exported surface offers.
func BenchmarkContains(b *testing.B) {
	s := benchSet(b, ">=1.0,<2.0,!=1.5")
	v, err := version.Parse("1.4.2")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Contains(v)
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
