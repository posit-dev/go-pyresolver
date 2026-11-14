// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// A version with no identical neighbour is a run of one: itself plus the gap up
// to the next published version. See the note at the top of provider/depender.go.

// gapVersions mixes finals, pre-releases, a post-release and a local version.
var gapVersions = []string{"1.0", "1.0+loc", "1.0.post1", "1.1rc1", "1.1", "2.0"}

// distinctIndex gives every version of "a" a different "b" requirement.
func distinctIndex(vs []string) *index.MockIndex {
	return baseDepIndex(func(idx *index.MockIndex) {
		for i, v := range vs {
			idx.AddVersion("a", v, fmt.Sprintf("b>=1.%d", i+1))
		}
	})
}

func TestDependerRunOfOneCoversTheGapToTheNextPublishedVersion(t *testing.T) {
	d := newDepProvider(t, distinctIndex(gapVersions), nil)
	a := provider.Project("a")
	d.depender(a, "1.0", "b") // the first call for a package stays narrow

	// Unpublished versions that sort inside a version's gap.
	probes := map[string][]string{
		"1.0.post1": {"1.0.post2", "1.0.1"},
		"1.1":       {"1.5", "1.9.post3"},
		"2.0":       {"3.0", "99"},
	}
	union := pep440set.Empty()
	for i, v := range gapVersions {
		got := d.depender(a, v, "b")
		var hi *version.Version
		if i+1 < len(gapVersions) {
			n := mustVersion(t, gapVersions[i+1])
			hi = &n
		}
		lo := mustVersion(t, v)
		// ==1.0 also matches 1.0+loc, so a run that ends at the local variant would
		// leave out part of the asked set: it gets no run.
		if v == "1.0" {
			if !got.IsEmpty() {
				t.Errorf("Depender of 1.0 = %v, want none (its next version is a local variant)", got)
			}
			continue
		}
		if want := pep440set.Run(&lo, hi); !got.Equal(want) {
			t.Errorf("Depender of %s = %v, want the run %v", v, got, want)
		}
		// The published members are exactly v: no other published version is claimed.
		assertMembers(t, got, gapVersions, v)
		for _, p := range probes[v] {
			if !got.Contains(mustVersion(t, p)) {
				t.Errorf("Depender of %s should cover the unpublished %s", v, p)
			}
		}
		union = union.Union(got)
	}
	// Adjacent spans merge into one.
	first := mustVersion(t, gapVersions[1])
	if want := pep440set.Run(&first, nil); !union.Equal(want) {
		t.Errorf("union of the spans = %v, want one run %v", union, want)
	}
}

func TestDependerGapOffByOption(t *testing.T) {
	d := newDepProvider(t, distinctIndex(gapVersions), func(o *provider.Options) { o.NoDependerRuns = true })
	a := provider.Project("a")
	for range 3 {
		if s := d.depender(a, "1.1", "b"); !s.IsEmpty() {
			t.Fatalf("NoDependerRuns must leave Depender empty, got %v", s)
		}
	}
}

// randomDistinctDepIndex is randomDepIndex with a different "b" requirement on
// every version, so every Depender is a run of one.
func randomDistinctDepIndex(r *rand.Rand) (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test")
	for _, v := range []string{"1", "2", "3", "4"} {
		idx.AddVersion("b", v)
	}
	vs := randomVersions(r)
	for i, v := range vs {
		idx.AddVersion("a", v, fmt.Sprintf("b>=1.%d", i+1))
	}
	return idx, vs
}

// Every published version in a gap Depender really has the dependency, checked
// against the single-version path (runs off), over local, post, pre and dev
// versions.
func TestDependerGapsAreSound(t *testing.T) {
	a := provider.Project("a")
	checked := 0
	for seed := int64(0); seed < 400; seed++ {
		r := rand.New(rand.NewSource(seed))
		idx, vs := randomDistinctDepIndex(r)
		wide := newDepProvider(t, idx, nil)
		oracle := newDepProvider(t, idx, func(o *provider.Options) { o.NoDependerRuns = true })
		wide.depender(a, vs[0], "b") // first call is narrow

		for _, v := range vs {
			deps, err := wide.p.Dependencies(a, pep440set.Exactly(mustVersion(t, v)))
			if err != nil {
				continue
			}
			for _, d := range deps {
				if d.Package != provider.Project("b") || d.Depender.IsEmpty() {
					continue
				}
				checked++
				if !d.Depender.Contains(mustVersion(t, v)) {
					t.Fatalf("seed %d: Depender %v leaves out the asked version %s", seed, d.Depender, v)
				}
				for _, w := range vs {
					if !d.Depender.Contains(mustVersion(t, w)) {
						continue
					}
					nd, err := oracle.p.Dependencies(a, pep440set.Exactly(mustVersion(t, w)))
					if err != nil {
						t.Fatalf("seed %d: gap %v of %s holds %s, which cannot be offered: %v", seed, d.Depender, v, w, err)
					}
					if !slices.ContainsFunc(nd, func(x solver.Dependency[provider.Package, pep440set.Set]) bool {
						return x.Package == d.Package && x.Allowed.Equal(d.Allowed)
					}) {
						t.Fatalf("seed %d: gap %v of %s holds %s, which does not depend on %v", seed, d.Depender, v, w, d.Allowed)
					}
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d gap Dependers checked: the property is not exercised", checked)
	}
}

// randomDistinctSolveIndex is randomSolveIndex with a fresh requirement on every
// version, the shape boto3 has.
func randomDistinctSolveIndex(r *rand.Rand) (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test")
	names := []string{"p0", "p1", "p2", "p3", "p4"}
	specs := []string{"", ">=1", "<3", ">=2", ">=1,<4", "<2", "==2.*", ">=3", ">=1.1", ">=1.2", "<2.1", "<=1.0", ">=1.0.post1", "!=1.1"}
	for i, n := range names {
		for _, v := range randomVersions(r) {
			var reqs []string
			for _, m := range names[i+1:] {
				if r.Intn(2) == 0 {
					reqs = append(reqs, m+specs[r.Intn(len(specs))])
				}
			}
			idx.AddVersion(n, v, reqs...)
		}
	}
	var roots []string
	for _, n := range names[:2+r.Intn(2)] {
		roots = append(roots, n+specs[r.Intn(len(specs))])
	}
	return idx, roots
}

// Gaps change how many rounds a solve takes, never its answer or whether it
// fails, under each pre-release policy.
func TestDependerGapsKeepPinsAndFailures(t *testing.T) {
	allPre := candidate.EnabledPrereleases(nil, []index.PackageName{"p0", "p1", "p2", "p3", "p4"})
	policies := map[string]func(*provider.Options){
		"default":             nil,
		"prereleases-allowed": func(o *provider.Options) { o.Prereleases = allPre },
		"if-no-final":         func(o *provider.Options) { o.PrereleaseIfNoFinal = true },
		"allowed+if-no-final": func(o *provider.Options) { o.Prereleases, o.PrereleaseIfNoFinal = allPre, true },
	}
	for name, mut := range policies {
		for _, tier2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tier2=%v", name, tier2), func(t *testing.T) {
				solved, failed := 0, 0
				for seed := int64(0); seed < 300; seed++ {
					r := rand.New(rand.NewSource(seed))
					idx, roots := randomDistinctSolveIndex(r)
					on, _, errOn := solveWithOpts(t, idx, false, tier2, mut, roots)
					off, _, errOff := solveWithOpts(t, idx, true, tier2, mut, roots)
					if (errOn == nil) != (errOff == nil) {
						t.Fatalf("seed %d %v: gaps on err=%v, off err=%v", seed, roots, errOn, errOff)
					}
					if errOn != nil {
						failed++
						continue
					}
					solved++
					if fmt.Sprint(on) != fmt.Sprint(off) {
						t.Fatalf("seed %d %v: pins differ\n on:  %v\n off: %v", seed, roots, on, off)
					}
				}
				if solved < 50 || failed < 5 {
					t.Fatalf("solved %d, failed %d of 300: the property is not exercised", solved, failed)
				}
			})
		}
	}
}

// Gaps do not add rounds when every version differs and every one is turned
// down. They shrink the learned clauses, which Stats does not show.
func TestDependerGapsAddNoRoundsWhenEveryVersionDiffers(t *testing.T) {
	const n = 80
	idx := index.NewMockIndex("test")
	idx.AddVersion("b", "1").AddVersion("b", "2")
	for i := 1; i <= n; i++ {
		idx.AddVersion("a", fmt.Sprint(i), fmt.Sprintf("b>=%d.5", 9+i))
	}
	_, on, errOn := solveWith(t, idx, false, false, []string{"a"})
	_, off, errOff := solveWith(t, idx, true, false, []string{"a"})
	if errOn == nil || errOff == nil {
		t.Fatalf("both solves must fail, got on=%v off=%v", errOn, errOff)
	}
	t.Logf("with gaps %+v\nwithout   %+v", on, off)
	if on.DependenciesCalls > off.DependenciesCalls || on.Decisions > off.Decisions {
		t.Errorf("gaps cost rounds: Dependencies calls %d vs %d, decisions %d vs %d",
			on.DependenciesCalls, off.DependenciesCalls, on.Decisions, off.Decisions)
	}
}
