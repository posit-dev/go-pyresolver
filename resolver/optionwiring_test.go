// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// solveStats resolves and returns the solver's Stats, so a test can see an
// option that changes the search but not the answer.
func solveStats(t *testing.T, idx index.MetadataIndex, opts resolver.Options, reqs ...string) solver.Stats {
	t.Helper()
	var s *solver.Solver[provider.Package, pep440set.Set]
	restore := resolver.OnSolver(func(sv *solver.Solver[provider.Package, pep440set.Set]) { s = sv })
	defer restore()
	if _, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts); err != nil {
		t.Fatal(err)
	}
	return s.Stats
}

// With OwnOrder, MarkExact makes the exact pin decided first, which avoids a
// backtrack here. This goes through Resolve, so the option must reach the provider.
func TestResolveMarkExactReachesProvider(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("loose", "1.0").
		AddVersion("loose", "2.0", "x>=2").
		AddVersion("loose", "3.0", "x>=2").
		AddVersion("pinned", "1.0", "x<2").
		AddVersion("x", "1.0").
		AddVersion("x", "2.0")
	stats := func(markExact bool) solver.Stats {
		opts := testOptions(t)
		opts.OwnOrder = true
		opts.MarkExact = markExact
		return solveStats(t, idx, opts, "loose>=1", "pinned==1.0")
	}

	off, on := stats(false), stats(true)
	if off.Backtracks == 0 || on.Backtracks != 0 {
		t.Errorf("backtracks: MarkExact off = %d (want > 0), on = %d (want 0)", off.Backtracks, on.Backtracks)
	}
}

// ExtraEdgeOnly drops the base-package edge of a requirement with extras, so
// the solver sees fewer dependency records.
func TestResolveExtraEdgeOnlyReachesProvider(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("foo", "1.0")
	idx.SetMetadata("foo", "1.0", index.PackageMetadata{ProvidesExtra: []string{"x"}})
	records := func(extraEdgeOnly bool) int {
		opts := testOptions(t)
		opts.ExtraEdgeOnly = extraEdgeOnly
		return solveStats(t, idx, opts, "foo[x]").LiveDependencyRecords
	}

	off, on := records(false), records(true)
	if on >= off {
		t.Errorf("dependency records: ExtraEdgeOnly on = %d, off = %d, want fewer with it on", on, off)
	}
}

// A requirement from any selected package that names a pre-release admits the
// pre-releases of that package, as in uv; a plain requirement does not.
func TestPrereleaseIfNoFinalTransitive(t *testing.T) {
	cases := []struct {
		name string
		dep  string // what app 1.0 requires
		flag bool
		want string // "" means resolution must fail
	}{
		{"transitive requirement names a pre-release, option off", "flask>=2.0rc1", false, "2.0rc1"},
		{"transitive requirement names a pre-release, option on", "flask>=2.0rc1", true, "2.0rc1"},
		{"plain transitive requirement, option on: pre-release excluded", "flask>=1.0", true, "1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := index.NewMockIndex("test").
				AddVersion("app", "1.0", tc.dep).
				AddVersion("flask", "1.0").
				AddVersion("flask", "2.0rc1")
			opts := testOptions(t)
			opts.PrereleaseIfNoFinal = tc.flag
			res, err := resolver.Resolve(context.Background(), mustRequirements(t, "app"), idx, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Pinned["flask"].String(); got != tc.want {
				t.Errorf("flask = %s, want %s", got, tc.want)
			}
		})
	}
}

// A requirement that names a pre-release is only known once its requirer is
// selected, after the package may have been ranked already.
func TestPrereleaseIfNoFinalTransitiveAfterEarlierRanking(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "flask>=1.0", "mid").
		AddVersion("mid", "1.0", "plugin").
		AddVersion("plugin", "1.0", "flask>=2.0rc1").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1")
	opts := testOptions(t)
	opts.PrereleaseIfNoFinal = true
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "app"), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Pinned["flask"].String(); got != "2.0rc1" {
		t.Errorf("flask = %s, want 2.0rc1", got)
	}
}

// Yanked finals count as final releases: with only a yanked final, the
// pre-releases are still not candidates under PrereleaseIfNoFinal.
func TestPrereleaseIfNoFinalYankedFinalCounts(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetYanksCaptured(true).
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1").
		AddFiles("flask", "1.0", index.DistFile{Filename: "flask-1.0-py3-none-any.whl", Kind: index.DistKindWheel, Yanked: true})
	opts := testOptions(t)
	opts.PrereleaseIfNoFinal = true
	if res, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask>1.0"), idx, opts); err == nil {
		t.Fatalf("resolved %v, want a failure: the yanked 1.0 is a final release", res.Pinned)
	}
}

func TestPrereleaseIfNoFinalYankedFinalSetupIsLive(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetYanksCaptured(true).
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1").
		AddFiles("flask", "1.0", index.DistFile{Filename: "flask-1.0-py3-none-any.whl", Kind: index.DistKindWheel, Yanked: true})
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask>1.0"), idx, testOptions(t))
	if err != nil || res.Pinned["flask"].String() != "2.0rc1" {
		t.Fatalf("option off: %v, %v, want flask 2.0rc1", res, err)
	}
}

// A version that is rejected as unusable (a transitive `===` cannot be
// expressed) must not lift the pre-release bar through its requirements.
func TestPrereleaseIfNoFinalIgnoresUnusableVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "2.0", "flask>=2.0rc1", "foo===1.0").
		AddVersion("app", "1.0").
		AddVersion("other", "1.0", "flask>1.0").
		AddVersion("foo", "1.0").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1")
	opts := testOptions(t)
	opts.PrereleaseIfNoFinal = true
	if res, err := resolver.Resolve(context.Background(), mustRequirements(t, "app", "other"), idx, opts); err == nil {
		t.Fatalf("resolved %v, want a failure: app 2.0 is unusable, so nothing names a flask pre-release", res.Pinned)
	}
}
