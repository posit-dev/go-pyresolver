// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// containsLinear is the original linear-scan Contains, kept as the oracle for
// the binary-search path.
func (s Set) containsLinear(v version.Version) bool {
	if len(s.spans) == 0 {
		return false
	}
	var p verPos
	p.init(v)
	for _, sp := range s.spans {
		if cmpVerBound(&p, sp.lo) < 0 {
			return false
		}
		if cmpVerBound(&p, sp.hi) < 0 {
			return true
		}
	}
	return false
}

func specSet(t testing.TB, spec string) Set {
	t.Helper()
	ss, err := version.NewSpecifiers(spec)
	if err != nil {
		t.Fatalf("NewSpecifiers(%q): %v", spec, err)
	}
	s, err := FromSpecifiers(ss)
	if err != nil {
		t.Fatalf("FromSpecifiers(%q): %v", spec, err)
	}
	return s
}

func checkAgainstOracles(t *testing.T, name string, s Set, vs []version.Version) {
	t.Helper()
	for _, v := range vs {
		want := s.containsBound(atBound(v))
		if lin := s.containsLinear(v); lin != want {
			t.Fatalf("%s: oracle disagreement on %s: linear=%v bound=%v", name, v, lin, want)
		}
		if got := s.Contains(v); got != want {
			t.Errorf("%s (%s): Contains(%s) = %v, want %v", name, s, v, got, want)
		}
	}
}

func TestContainsEdgeCases(t *testing.T) {
	vs := verPosVersions(t)
	vs = append(vs, mustV(t, "0.0.dev0"), mustV(t, "0"), mustV(t, "1.0.1.dev3"), mustV(t, "1.5.0rc1"), mustV(t, "3.0"))

	sets := map[string]Set{
		"empty":             Empty(),
		"all":               All(),
		"point":             Exactly(mustV(t, "1.0")),
		"pointLocal":        Exactly(mustV(t, "1.0+aaa")),
		"pointHole":         specSet(t, "!=1.5"),
		"twoHoles":          specSet(t, "!=1.0,!=2.0"),
		"adjacent":          Exactly(mustV(t, "1.0")).Union(Exactly(mustV(t, "1.0.1"))),
		"touchingRanges":    specSet(t, "<1.5").Union(specSet(t, ">=1.5")),
		"unboundedLow":      specSet(t, "<1.0"),
		"unboundedHigh":     specSet(t, ">=2.0"),
		"bothUnboundedHole": specSet(t, "!=1.0rc1"),
		"prereleaseRange":   specSet(t, ">=1.0a1,<1.0rc1"),
		"devHole":           specSet(t, "!=1.0.dev0,!=1.0.dev1"),
		"post":              specSet(t, ">1.0,<1.0.post1"),
		"epoch":             specSet(t, ">=1!0.1"),
		"wildcardHole":      specSet(t, "!=1.0.*"),
		"complexUnion":      specSet(t, ">=1.0,<1.5").Union(specSet(t, ">=1.5.1,<2.0")).Union(Exactly(mustV(t, "9223372036854775808"))),
	}
	for name, s := range sets {
		checkAgainstOracles(t, name, s, vs)
		checkAgainstOracles(t, name+"/complement", s.Complement(), vs)
	}
}

// randomSet builds an arbitrary canonical set from points and ranges.
func randomMixedSet(rng *rand.Rand, pool []version.Version) Set {
	s := Empty()
	if rng.Intn(3) == 0 {
		s = All()
	}
	for i, n := 0, rng.Intn(12); i < n; i++ {
		a, b := pool[rng.Intn(len(pool))], pool[rng.Intn(len(pool))]
		var piece Set
		switch rng.Intn(4) {
		case 0:
			piece = Exactly(a)
		case 1:
			piece = Exactly(a).Complement()
		case 2:
			piece = specSetFromVersions(a, b)
		default:
			piece = specSetFromVersions(a, b).Complement()
		}
		if rng.Intn(2) == 0 {
			s = s.Union(piece)
		} else {
			s = s.Intersect(piece.Union(Exactly(b)))
		}
	}
	return s
}

func specSetFromVersions(a, b version.Version) Set {
	lo, hi := a, b
	if lo.Compare(hi) > 0 {
		lo, hi = hi, lo
	}
	ss, err := version.NewSpecifiers(fmt.Sprintf(">=%s,<%s", lo, hi))
	if err != nil {
		return Empty()
	}
	s, err := FromSpecifiers(ss)
	if err != nil {
		return Empty()
	}
	return s
}

func TestContainsRandomSetsAgreeWithLinear(t *testing.T) {
	pool := verPosVersions(t)
	pool = append(pool, mustV(t, "0.1"), mustV(t, "3.0"), mustV(t, "1.2.3"), mustV(t, "1.2.3.dev1"))
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		s := randomMixedSet(rng, pool)
		checkAgainstOracles(t, fmt.Sprintf("random#%d", i), s, pool)
	}
}

// TestContainsGrowingHoles mirrors the solver: ranked versions are ruled out
// one at a time, so the set accumulates `!=` holes.
func TestContainsGrowingHoles(t *testing.T) {
	var vs []version.Version
	for i := 0; i < 60; i++ {
		vs = append(vs, mustV(t, fmt.Sprintf("1.%d", i)), mustV(t, fmt.Sprintf("1.%d.dev1", i)))
	}
	s := All()
	rng := rand.New(rand.NewSource(2))
	for _, i := range rng.Perm(len(vs)) {
		s = s.Intersect(Exactly(vs[i]).Complement())
		checkAgainstOracles(t, "holes", s, vs)
	}
}

func FuzzContainsAgreesWithLinear(f *testing.F) {
	f.Add(int64(1), uint8(3))
	f.Add(int64(99), uint8(10))
	pool := []version.Version{}
	for _, sp := range []string{"0.1", "1.0", "1.0.dev0", "1.0a1", "1.0rc1", "1.0+a", "1.0.post1", "1.5", "2.0", "1!0.1", "3.0"} {
		v, err := version.Parse(sp)
		if err != nil {
			f.Fatal(err)
		}
		pool = append(pool, v)
	}
	f.Fuzz(func(t *testing.T, seed int64, _ uint8) {
		rng := rand.New(rand.NewSource(seed))
		checkAgainstOracles(t, "fuzz", randomMixedSet(rng, pool), pool)
	})
}

// BenchmarkContainsHoles probes the newest version against a set with n
// holes below it, the shape a backtracking solve produces.
func BenchmarkContainsHoles(b *testing.B) {
	for _, n := range []int{10, 100, 1000, 2500} {
		b.Run(fmt.Sprintf("holes=%d", n), func(b *testing.B) {
			s := All()
			var parts []Set
			for i := 0; i < n; i++ {
				v, _ := version.Parse(fmt.Sprintf("0.5.%d", i))
				parts = append(parts, Exactly(v).Complement())
			}
			for _, p := range parts {
				s = s.Intersect(p)
			}
			top, _ := version.Parse("0.5.99999")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !s.Contains(top) {
					b.Fatal("expected contained")
				}
			}
		})
	}
}
