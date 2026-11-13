// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// Depender runs: see the note at the top of provider/depender.go.

type depProvider struct {
	t *testing.T
	p *provider.Provider
}

func newDepProvider(t *testing.T, idx index.MetadataIndex, mut func(*provider.Options)) *depProvider {
	t.Helper()
	opts := testOptions(t)
	if mut != nil {
		mut(&opts)
	}
	p := provider.New(context.Background(), idx, opts)
	// The solver asks Candidates before Dependencies, which is what loads the
	// package's published list.
	if _, _, _, err := p.Candidates(provider.Project("a"), atLeast(t, "0")); err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	return &depProvider{t: t, p: p}
}

// depender asks for (pkg, v)'s dependency on dep and returns its Depender set.
func (d *depProvider) depender(pkg provider.Package, v, dep string) pep440set.Set {
	d.t.Helper()
	deps, err := d.p.Dependencies(pkg, pep440set.Exactly(mustVersion(d.t, v)))
	if err != nil {
		d.t.Fatalf("Dependencies(%s %s): %v", pkg, v, err)
	}
	for _, x := range deps {
		if x.Package == provider.Project(index.NewPackageName(dep)) {
			return x.Depender
		}
	}
	d.t.Fatalf("Dependencies(%s %s) has no dependency on %s", pkg, v, dep)
	return pep440set.Set{}
}

// members lists which of vs the set contains, in order.
func members(t *testing.T, s pep440set.Set, vs ...string) []string {
	t.Helper()
	var out []string
	for _, v := range vs {
		if s.Contains(mustVersion(t, v)) {
			out = append(out, v)
		}
	}
	return out
}

func assertMembers(t *testing.T, s pep440set.Set, all []string, want ...string) {
	t.Helper()
	if got := members(t, s, all...); !slices.Equal(got, want) {
		t.Errorf("Depender holds %v of %v, want %v", got, all, want)
	}
}

func baseDepIndex(extra func(*index.MockIndex)) *index.MockIndex {
	idx := index.NewMockIndex("test")
	for _, v := range []string{"1", "2", "3", "4"} {
		idx.AddVersion("b", v)
	}
	if extra != nil {
		extra(idx)
	}
	return idx
}

var runVersions = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}

// a 3..7 share "b>=1"; 1, 2 and 8, 9 differ.
func runIndex() *index.MockIndex {
	return baseDepIndex(func(idx *index.MockIndex) {
		for _, v := range runVersions {
			switch v {
			case "1", "2":
				idx.AddVersion("a", v, "b>=2")
			case "8", "9":
				idx.AddVersion("a", v, "b<3")
			default:
				idx.AddVersion("a", v, "b>=1")
			}
		}
	})
}

func TestDependerFirstCallNarrowSecondCallRuns(t *testing.T) {
	d := newDepProvider(t, runIndex(), nil)
	a := provider.Project("a")

	first := d.depender(a, "5", "b")
	assertMembers(t, first, runVersions)
	if !first.IsEmpty() {
		t.Errorf("the first call for a package must stay narrow, got %v", first)
	}

	second := d.depender(a, "5", "b")
	assertMembers(t, second, runVersions, "3", "4", "5", "6", "7")
}

func TestDependerRunsStopAtDifferingNeighbour(t *testing.T) {
	d := newDepProvider(t, runIndex(), nil)
	a := provider.Project("a")
	d.depender(a, "1", "b")
	// 1 and 2 share "b>=2" and nothing else does, so the run is exactly them.
	assertMembers(t, d.depender(a, "1", "b"), runVersions, "1", "2")
	assertMembers(t, d.depender(a, "9", "b"), runVersions, "8", "9")
}

func TestDependerRunsOffByOption(t *testing.T) {
	d := newDepProvider(t, runIndex(), func(o *provider.Options) { o.NoDependerRuns = true })
	a := provider.Project("a")
	d.depender(a, "5", "b")
	for range 3 {
		if s := d.depender(a, "5", "b"); !s.IsEmpty() {
			t.Fatalf("NoDependerRuns must leave Depender empty, got %v", s)
		}
	}
}

func TestDependerRunStopsAtUnreadableNeighbour(t *testing.T) {
	idx := runIndex().SetUnavailable("a", "6")
	d := newDepProvider(t, idx, nil)
	a := provider.Project("a")
	d.depender(a, "5", "b")
	// 6 cannot be read, so the run ends before it even though 7 matches again.
	assertMembers(t, d.depender(a, "5", "b"), runVersions, "3", "4", "5")
}

func TestDependerRunStopsAtWheelTagRejectedNeighbour(t *testing.T) {
	idx := index.NewMockIndex("test").SetWheelTagsComplete(true)
	idx.AddVersion("b", "1")
	wheel := func(tag string) index.PackageMetadata {
		m, err := index.ParseRecord(index.RawRecord{
			RequiresDist: []string{"b>=1"}, WheelTags: []string{tag}, TagsCaptured: true,
		})
		if err != nil {
			t.Fatalf("ParseRecord: %v", err)
		}
		return m
	}
	for _, v := range []string{"1", "2", "3", "4", "5"} {
		tag := "cp311-cp311-manylinux_2_17_x86_64"
		if v == "4" {
			tag = "cp311-cp311-win_amd64" // the only wheel is for another platform
		}
		idx.AddVersion("a", v, "b>=1")
		idx.SetMetadata("a", v, wheel(tag))
	}
	d := newDepProvider(t, idx, func(o *provider.Options) { o.WheelTags = tagFilter(t) })
	a := provider.Project("a")
	vs := []string{"1", "2", "3", "4", "5"}
	d.depender(a, "2", "b")
	assertMembers(t, d.depender(a, "2", "b"), vs, "1", "2", "3")
}

func TestDependerNeverLeavesOutTheAskedVersion(t *testing.T) {
	// A local variant sorts right after its public version, so a run that ends
	// at it can exclude the version being asked about, which the solver rejects.
	idx := baseDepIndex(func(idx *index.MockIndex) {
		idx.AddVersion("a", "0.9", "b>=2")
		idx.AddVersion("a", "1.0", "b>=1")
		idx.AddVersion("a", "1.0+local", "b>=1")
		idx.AddVersion("a", "1.1", "b>=1")
	})
	d := newDepProvider(t, idx, nil)
	a := provider.Project("a")
	for _, v := range []string{"1.0", "1.0+local", "1.1", "1.0", "1.0+local", "1.1"} {
		s := d.depender(a, v, "b")
		if !s.IsEmpty() && !s.Contains(mustVersion(t, v)) {
			t.Errorf("Depender %v leaves out the asked version %s", s, v)
		}
	}
}

func TestDependerExtrasLinkNeverWidens(t *testing.T) {
	idx := baseDepIndex(func(idx *index.MockIndex) {
		for _, v := range []string{"1", "2", "3", "4", "5"} {
			idx.AddVersion("a", v, "b>=1 ; extra == 'x'")
		}
	})
	d := newDepProvider(t, idx, nil)
	ax := provider.WithExtra("a", "x")
	for range 3 {
		// The link a[x] -> a is pinned to its own version, so no neighbour shares it.
		s := d.depender(ax, "3", "a")
		if len(members(t, s, "1", "2", "4", "5")) != 0 {
			t.Fatalf("the extras link widened to %v", s)
		}
	}
}

func TestDependerBudgetBoundsFreshReadsAndGrows(t *testing.T) {
	const n = 400
	idx := baseDepIndex(func(idx *index.MockIndex) {
		for i := 1; i <= n; i++ {
			idx.AddVersion("a", fmt.Sprint(i), "b>=1")
		}
	})
	ci := newCountingIndex(idx)
	d := newDepProvider(t, ci, nil)
	a := provider.Project("a")
	width := func() int {
		var all []string
		for i := 1; i <= n; i++ {
			all = append(all, fmt.Sprint(i))
		}
		return len(members(t, d.depender(a, "200", "b"), all...))
	}

	if w := width(); w != 0 {
		t.Fatalf("first call width = %d, want narrow (empty)", w)
	}
	before := ci.metadata.Load()
	w2 := width()
	r2 := ci.metadata.Load() - before
	before = ci.metadata.Load()
	w3 := width()
	r3 := ci.metadata.Load() - before

	if r2 > 32+1 || r3 > 64+1 { // +1: the asked version itself is re-read each call
		t.Errorf("fresh neighbour reads = %d then %d, want at most 32 then 64 (plus the asked version)", r2, r3)
	}
	if w2 <= 1 || w3 <= w2 {
		t.Errorf("run widths = %d then %d, want growth", w2, w3)
	}
}

// ---- soundness against the single-version path ----

func randomRequirement(r *rand.Rand, prev string) string {
	if prev != "" && r.Intn(10) < 7 {
		return prev
	}
	pool := []string{
		"b", "b>=1", "b<3", "b>=1,<3", "c>=2", "c<4",
		"b>=1 ; python_version >= '3.8'", "b>=1 ; python_version < '3.8'",
	}
	return pool[r.Intn(len(pool))]
}

func randomVersions(r *rand.Rand) []string {
	seen := map[string]bool{}
	var out []string
	for range 5 + r.Intn(12) {
		major := 1 + r.Intn(5)
		var v string
		switch r.Intn(7) {
		case 0:
			v = fmt.Sprintf("%d.0rc1", major)
		case 1:
			v = fmt.Sprintf("%d.0.dev%d", major, 1+r.Intn(3))
		case 2:
			v = fmt.Sprintf("%d.0.post1", major)
		case 3:
			v = fmt.Sprintf("%d.0+loc%d", major, 1+r.Intn(2))
		default:
			v = fmt.Sprintf("%d.%d", major, r.Intn(3))
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(x, y string) int { return version.MustParse(x).Compare(version.MustParse(y)) })
	return out
}

func randomDepIndex(r *rand.Rand) (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test")
	for _, n := range []string{"b", "c"} {
		for _, v := range []string{"1", "2", "3", "4"} {
			idx.AddVersion(n, v)
		}
	}
	vs := randomVersions(r)
	prev := [2]string{}
	for _, v := range vs {
		var reqs []string
		for k := range 1 + r.Intn(2) {
			prev[k] = randomRequirement(r, prev[k])
			reqs = append(reqs, prev[k])
		}
		idx.AddVersion("a", v, reqs...)
	}
	return idx, vs
}

// TestDependerRunsAreSound checks the claim a Depender makes: every published
// version in it really has that dependency. The oracle is the same provider
// with runs off, asked about each member on its own.
func TestDependerRunsAreSound(t *testing.T) {
	a := provider.Project("a")
	widened := 0
	for seed := int64(0); seed < 400; seed++ {
		r := rand.New(rand.NewSource(seed))
		idx, vs := randomDepIndex(r)
		wide := newDepProvider(t, idx, nil)
		oracle := newDepProvider(t, idx, func(o *provider.Options) { o.NoDependerRuns = true })

		for range 14 {
			v := vs[r.Intn(len(vs))]
			set := pep440set.Exactly(mustVersion(t, v))
			deps, err := wide.p.Dependencies(a, set)
			if err != nil {
				continue // not offered (for example a pre-release only choice)
			}
			for _, d := range deps {
				if d.Depender.IsEmpty() {
					continue
				}
				widened++
				if !d.Depender.Contains(mustVersion(t, v)) {
					t.Fatalf("seed %d: Depender %v leaves out the asked version %s", seed, d.Depender, v)
				}
				for _, w := range vs {
					if !d.Depender.Contains(mustVersion(t, w)) {
						continue
					}
					nd, err := oracle.p.Dependencies(a, pep440set.Exactly(mustVersion(t, w)))
					if err != nil {
						t.Fatalf("seed %d: run %v holds %s, which cannot be offered: %v", seed, d.Depender, w, err)
					}
					if !slices.ContainsFunc(nd, func(x solver.Dependency[provider.Package, pep440set.Set]) bool {
						return x.Package == d.Package && x.Allowed.Equal(d.Allowed)
					}) {
						t.Fatalf("seed %d: run %v holds %s, which does not depend on %s %v (asked about %s)",
							seed, d.Depender, w, d.Package, d.Allowed, v)
					}
				}
			}
		}
	}
	if widened < 100 {
		t.Fatalf("only %d widened dependencies over 400 universes: the property is not exercised", widened)
	}
}

// ---- same pins with runs on and off ----

func randomSolveIndex(r *rand.Rand) (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test")
	names := []string{"p0", "p1", "p2", "p3", "p4"}
	specs := []string{"", ">=1", "<3", ">=2", ">=1,<4", "<2", "==2.*", ">=3"}
	for i, n := range names {
		vs := randomVersions(r)
		prev := ""
		for _, v := range vs {
			var reqs []string
			for _, m := range names[i+1:] {
				if r.Intn(3) == 0 {
					if prev != "" && r.Intn(10) < 7 {
						reqs = append(reqs, prev)
						continue
					}
					prev = m + specs[r.Intn(len(specs))]
					reqs = append(reqs, prev)
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

func solveWith(t *testing.T, idx index.MetadataIndex, noRuns, tier2 bool, reqs []string) (map[string]string, solver.Stats, error) {
	t.Helper()
	opts := testOptions(t)
	opts.NoDependerRuns = noRuns
	opts.MarkExact = tier2
	opts.Requirements = mustRequirements(t, reqs...)
	p := provider.New(context.Background(), idx, opts)
	s := solver.New(provider.Root(), pep440set.Exactly(mustVersion(t, "0")), p)
	s.MaxRounds = maxRounds
	if tier2 {
		s.MergeDependencies, s.RunningSatisfier, s.StoredKeys = true, true, true
		s.StableDiscovery, s.OwnOrder, s.ClashThreshold = true, true, 3
	}
	sol, err := s.Solve()
	if err != nil {
		return nil, s.Stats, err
	}
	out := map[string]string{}
	for pkg, set := range sol.Selected {
		v, ok := set.Singleton()
		if !ok {
			t.Fatalf("%s decided as %v", pkg, set)
		}
		out[pkg.String()] = v.String()
	}
	return out, s.Stats, nil
}

func TestDependerRunsKeepPins(t *testing.T) {
	t.Run("default", func(t *testing.T) { dependerRunsKeepPins(t, false) })
	t.Run("tier2", func(t *testing.T) { dependerRunsKeepPins(t, true) })
}

func dependerRunsKeepPins(t *testing.T, tier2 bool) {
	solved, roundsOn, roundsOff := 0, 0, 0
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		idx, roots := randomSolveIndex(r)
		on, ron, errOn := solveWith(t, idx, false, tier2, roots)
		off, roff, errOff := solveWith(t, idx, true, tier2, roots)
		if (errOn == nil) != (errOff == nil) {
			t.Fatalf("seed %d %v: runs on err=%v, off err=%v", seed, roots, errOn, errOff)
		}
		roundsOn += ron.Decisions
		roundsOff += roff.Decisions
		if errOn != nil {
			continue
		}
		solved++
		if fmt.Sprint(on) != fmt.Sprint(off) {
			t.Fatalf("seed %d %v: pins differ\n on:  %v\n off: %v", seed, roots, on, off)
		}
	}
	if solved < 50 {
		t.Fatalf("only %d of 300 universes solved: the property is not exercised", solved)
	}
	t.Logf("solved %d/300, total decisions on=%d off=%d", solved, roundsOn, roundsOff)
}

// A package whose every version needs something unavailable used to be ruled
// out one version per round; a run rules out the whole stretch at once.
func TestDependerRunsCollapseTheWalk(t *testing.T) {
	const n = 80
	idx := index.NewMockIndex("test")
	idx.AddVersion("b", "1").AddVersion("b", "2")
	for i := 1; i <= n; i++ {
		idx.AddVersion("a", fmt.Sprint(i), "b>=9")
	}
	_, on, errOn := solveWith(t, idx, false, false, []string{"a"})
	_, off, errOff := solveWith(t, idx, true, false, []string{"a"})
	if errOn == nil || errOff == nil {
		t.Fatalf("both solves must fail, got on=%v off=%v", errOn, errOff)
	}
	t.Logf("with runs %+v\nwithout   %+v", on, off)
	if off.DependenciesCalls < n || on.DependenciesCalls*5 > off.DependenciesCalls {
		t.Errorf("Dependencies calls with runs = %d, without = %d, want runs under a fifth of the walk (%d)",
			on.DependenciesCalls, off.DependenciesCalls, n)
	}
}

// Finals and pre-releases never share a run: a run that spans the boundary made
// the solver pick a different valid answer than the one-version-at-a-time path.
func TestDependerRunsDoNotCrossTheFinalPreReleaseBoundary(t *testing.T) {
	all := []string{"1", "2", "3rc1", "3rc2", "3", "4"}
	idx := baseDepIndex(func(idx *index.MockIndex) {
		for _, v := range all {
			idx.AddVersion("a", v, "b>=1")
		}
	})
	d := newDepProvider(t, idx, nil)
	a := provider.Project("a")
	d.depender(a, "2", "b")
	assertMembers(t, d.depender(a, "2", "b"), all, "1", "2")
	assertMembers(t, d.depender(a, "3", "b"), all, "3", "4")
	assertMembers(t, d.depender(a, "3rc1", "b"), all, "3rc1", "3rc2")
}

func TestDependerBudgetStopsGrowingAtTheCap(t *testing.T) {
	const n = 20000
	idx := baseDepIndex(func(idx *index.MockIndex) {
		for i := 1; i <= n; i++ {
			idx.AddVersion("a", fmt.Sprint(i), "b>=1")
		}
	})
	ci := newCountingIndex(idx)
	d := newDepProvider(t, ci, nil)
	a := provider.Project("a")
	var last int64
	for range 8 {
		before := ci.metadata.Load()
		d.depender(a, "10000", "b")
		last = ci.metadata.Load() - before
	}
	if last > 1024+1 {
		t.Errorf("fresh neighbour reads on a late call = %d, want at most 1024 (plus the asked version)", last)
	}
}
