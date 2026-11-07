// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"math/rand"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// randomSet unions a few Exactly versions, then maybe complements: dense spans,
// gaps, and the infinite ends all show up.
func randomSet(t *testing.T, r *rand.Rand, pool []version.Version) Set {
	t.Helper()
	s := Empty()
	for n := r.Intn(6); n > 0; n-- {
		s = s.Union(Exactly(pool[r.Intn(len(pool))]))
	}
	if r.Intn(2) == 0 {
		s = s.Complement()
	}
	if r.Intn(4) == 0 {
		lo := Exactly(pool[r.Intn(len(pool))]).Complement()
		s = s.Intersect(lo)
	}
	return s
}

// TestDifferenceAndSubsetAgreeWithComplementForm holds the fast paths to the
// definitions go-pubgrub would otherwise use.
func TestDifferenceAndSubsetAgreeWithComplementForm(t *testing.T) {
	var pool []version.Version
	for _, s := range []string{"0.1", "1.0", "1.0.post1", "1.1", "1.2rc1", "1.2", "2.0", "2.0+local", "3", "10.0"} {
		v, err := version.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		pool = append(pool, v)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		a, b := randomSet(t, r, pool), randomSet(t, r, pool)
		if got, want := a.Difference(b), a.Intersect(b.Complement()); !got.Equal(want) {
			t.Fatalf("Difference(%v, %v) = %v, want %v", a, b, got, want)
		}
		if got, want := a.IsSubsetOf(b), a.Intersect(b.Complement()).IsEmpty(); got != want {
			t.Fatalf("IsSubsetOf(%v, %v) = %v, want %v", a, b, got, want)
		}
	}
}

// Set must satisfy go-pubgrub's optional fast-path interfaces, or its solver
// silently falls back to the slow form. Same shapes as versionset.SubsetOfer
// and versionset.Differencer.
var (
	_ interface{ IsSubsetOf(Set) bool } = Set{}
	_ interface{ Difference(Set) Set }  = Set{}
)

// TestDifferenceSubsetTable pins the fast paths to the interface-free
// definitions over fixed operands, including empty, all and exclusions.
func TestDifferenceSubsetTable(t *testing.T) {
	v := func(s string) version.Version { return version.MustParse(s) }
	rng := func(a, b string) Set { return newSet(span{newBound(v(a), edgeAt), newBound(v(b), edgeAt)}) }
	sets := map[string]Set{
		"empty":      Empty(),
		"all":        All(),
		"range":      rng("1.0", "5.0"),
		"inner":      rng("2.0", "3.0"),
		"two ranges": rng("1.0", "2.0").Union(rng("4.0", "6.0")),
		"excl 2.0":   Exactly(v("2.0")).Complement(),
		"range excl": rng("1.0", "5.0").Intersect(Exactly(v("2.0")).Complement()),
		"only 2.0":   Exactly(v("2.0")),
		"touching":   rng("5.0", "6.0"),
		"open upper": newSet(span{newBound(v("3.0"), edgeAt), posInf()}),
		"open lower": newSet(span{negInf(), newBound(v("3.0"), edgeAt)}),
	}
	for an, a := range sets {
		for bn, b := range sets {
			if got, want := a.Difference(b), a.Intersect(b.Complement()); !got.Equal(want) {
				t.Errorf("%s \\ %s = %v, want %v", an, bn, got, want)
			}
			if got, want := a.IsSubsetOf(b), a.Intersect(b.Complement()).IsEmpty(); got != want {
				t.Errorf("%s subset of %s = %v, want %v", an, bn, got, want)
			}
		}
	}
	if !sets["range excl"].IsSubsetOf(sets["range"]) || sets["range"].IsSubsetOf(sets["range excl"]) {
		t.Error("exclusion must make the punctured range a strict subset")
	}
}
