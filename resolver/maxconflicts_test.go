// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// The newer versions of a need a package that does not exist, so the solver
// resolves a conflict before it settles on a 1.0.
func conflictFixture() *index.MockIndex {
	return index.NewMockIndex("test").
		AddVersion("a", "1.0").
		AddVersion("a", "2.0", "missing").
		AddVersion("a", "3.0", "missing").
		AddVersion("a", "4.0", "missing")
}

func resolveCapped(t *testing.T, cap int, idx index.MetadataIndex, upgrade int, reqs ...string) (*resolver.Resolution, error) {
	t.Helper()
	opts := testOptions(t)
	opts.MaxConflicts = cap
	opts.UpgradeRoots = upgrade
	return resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
}

func TestResolveMaxConflictsGivesUpWithTypedError(t *testing.T) {
	idx := conflictFixture()

	// Unlimited, the fixture needs a conflict; a cap of 1 is reached, not exceeded.
	var s *solver.Solver[provider.Package, pep440set.Set]
	defer resolver.OnSolver(func(got *solver.Solver[provider.Package, pep440set.Set]) { s = got })()
	if _, err := resolveCapped(t, 0, idx, 0, "a"); err != nil {
		t.Fatal(err)
	}
	if s.Stats.Backtracks < 1 {
		t.Fatalf("fixture resolved %d conflicts, want at least 1", s.Stats.Backtracks)
	}

	_, err := resolveCapped(t, 1, idx, 0, "a")
	if err == nil {
		t.Fatal("MaxConflicts 1 was ignored")
	}
	if !errors.Is(err, solver.ErrMaxConflicts) {
		t.Errorf("errors.Is(err, solver.ErrMaxConflicts) = false for %T: %v", err, err)
	}
	// PPM matches this text.
	if !strings.Contains(err.Error(), "solver: gave up after ") {
		t.Errorf("message %q lost its stable prefix", err)
	}
	var re *resolver.ResolutionError
	if errors.As(err, &re) {
		t.Errorf("hitting the conflict cap was reported as a conflict between requirements:\n%s", re)
	}
}

func TestResolveMaxConflictsDefaultUnchanged(t *testing.T) {
	idx := conflictFixture()
	want := map[string]string{"a": "1.0"}
	for _, cap := range []int{0, 1000} {
		res, err := resolveCapped(t, cap, idx, 0, "a")
		if err != nil {
			t.Fatalf("cap %d: %v", cap, err)
		}
		if got := pins(t, res); !reflect.DeepEqual(got, want) {
			t.Errorf("cap %d: %v, want %v", cap, got, want)
		}
	}
}

// The cap reaches every solve
// of the UpgradeRoots pass, not only the first.
func TestResolveMaxConflictsReachesEveryUpgradeSolve(t *testing.T) {
	idx := upgradeFixture()
	var caps []int
	defer resolver.OnSolver(func(s *solver.Solver[provider.Package, pep440set.Set]) {
		caps = append(caps, s.MaxConflicts)
	})()
	res, err := resolveCapped(t, 1000, idx, 12, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), (map[string]string{"a": "3.0", "b": "1.0"}); !reflect.DeepEqual(got, want) {
		t.Errorf("pins %v, want %v", got, want)
	}
	if len(caps) < 2 {
		t.Fatalf("saw %d solves, want the base and at least one re-solve", len(caps))
	}
	for i, c := range caps {
		if c != 1000 {
			t.Errorf("solve %d had MaxConflicts %d, want 1000", i, c)
		}
	}
}

func TestResolveClashFirstReachesSolver(t *testing.T) {
	var got []bool
	defer resolver.OnSolver(func(s *solver.Solver[provider.Package, pep440set.Set]) {
		got = append(got, s.ClashFirst)
	})()
	opts := testOptions(t)
	opts.ClashThreshold = 2
	opts.ClashFirst = true
	if _, err := resolver.Resolve(context.Background(), mustRequirements(t, "a"), conflictFixture(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []bool{true}) {
		t.Errorf("ClashFirst on the solver = %v, want [true]", got)
	}
}
