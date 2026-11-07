// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"math/rand"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// refIntersect is the original all-pairs intersection through newSet.
func refIntersect(s, other Set) Set {
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

func TestIntersectMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	vs := []string{"0.1", "1.0", "1.0.post1", "1.5", "2.0rc1", "2.0", "2.0+a", "3.1", "10.0"}
	pick := func() Set {
		s := Empty()
		for k := r.Intn(4); k >= 0; k-- {
			a, b := version.MustParse(vs[r.Intn(len(vs))]), version.MustParse(vs[r.Intn(len(vs))])
			var piece Set
			switch r.Intn(5) {
			case 0:
				piece = Exactly(a)
			case 1:
				piece = newSet(span{newBound(a, edgeAt), posInf()})
			case 2:
				piece = newSet(span{negInf(), newBound(b, edgeAt)})
			case 3:
				piece = newSet(span{newBound(a, edgeAt), newBound(b, edgeAt)})
			default:
				piece = All()
			}
			if r.Intn(3) == 0 {
				piece = piece.Complement()
			}
			s = s.Union(piece)
		}
		return s
	}
	for i := 0; i < 20000; i++ {
		a, b := pick(), pick()
		if got, want := a.Intersect(b), refIntersect(a, b); !got.Equal(want) {
			t.Fatalf("Intersect(%v, %v) = %v, want %v", a, b, got, want)
		}
	}
}

// Hits the equal-operand shortcut (the result is an operand unchanged) and its
// edges. Each result is checked against the reference, not just for size.
func TestIntersectOperandShortcuts(t *testing.T) {
	v := func(s string) version.Version { return version.MustParse(s) }
	rng := func(a, b string) Set { return newSet(span{newBound(v(a), edgeAt), newBound(v(b), edgeAt)}) }
	wide := rng("1.0", "5.0").Union(rng("7.0", "9.0"))
	sub := rng("2.0", "3.0").Union(rng("7.0", "8.0"))
	hole := All().Intersect(Exactly(v("2.0")).Complement())
	cases := []struct {
		name string
		a, b Set
	}{
		{"identical", wide, wide},
		{"identical rebuilt", wide, rng("1.0", "5.0").Union(rng("7.0", "9.0"))},
		{"a subset of b", sub, wide},
		{"a superset of b", wide, sub},
		{"all with set", All(), wide},
		{"set with all", wide, All()},
		{"all with all", All(), All()},
		{"empty with set", Empty(), wide},
		{"set with empty", wide, Empty()},
		{"empty with empty", Empty(), Empty()},
		{"disjoint", rng("1.0", "2.0"), rng("3.0", "4.0")},
		{"touching", rng("1.0", "2.0"), rng("2.0", "3.0")},
		{"exclusion inside range", hole, rng("1.0", "3.0")},
		{"range inside exclusion", rng("1.0", "3.0"), hole},
	}
	for _, c := range cases {
		want := refIntersect(c.a, c.b)
		if got := c.a.Intersect(c.b); !got.Equal(want) {
			t.Errorf("%s: Intersect = %v, want %v", c.name, got, want)
		}
	}
	// The subset/superset cases must return exactly the smaller operand.
	if got := wide.Intersect(sub); !got.Equal(sub) {
		t.Errorf("superset ∩ subset = %v, want the subset %v", got, sub)
	}
	if got := sub.Intersect(wide); !got.Equal(sub) {
		t.Errorf("subset ∩ superset = %v, want the subset %v", got, sub)
	}
}
