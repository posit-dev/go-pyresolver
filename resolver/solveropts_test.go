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

type solverView struct {
	merge, running, stored, stable, own bool
	clash                               int
}

// solverSeen resolves one trivial requirement and returns the go-pubgrub
// settings Resolve gave the Solver.
func solverSeen(t *testing.T, mutate func(*resolver.Options)) solverView {
	t.Helper()
	var got solverView
	restore := resolver.OnSolver(func(s *solver.Solver[provider.Package, pep440set.Set]) {
		got = solverView{
			s.MergeDependencies, s.RunningSatisfier, s.StoredKeys,
			s.StableDiscovery, s.OwnOrder, s.ClashThreshold,
		}
	})
	defer restore()
	idx := index.NewMockIndex("test").AddVersion("a", "1.0")
	opts := testOptions(t)
	mutate(&opts)
	if _, err := resolver.Resolve(context.Background(), mustRequirements(t, "a"), idx, opts); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSolverOptionsDefaultOff(t *testing.T) {
	if got := solverSeen(t, func(*resolver.Options) {}); got != (solverView{}) {
		t.Errorf("default solver settings = %+v, want all off", got)
	}
}

func TestSolverOptionsPassThrough(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*resolver.Options)
		want   solverView
	}{
		{"MergeDependencies", func(o *resolver.Options) { o.MergeDependencies = true }, solverView{merge: true}},
		{"RunningSatisfier", func(o *resolver.Options) { o.RunningSatisfier = true }, solverView{running: true}},
		{"StoredKeys", func(o *resolver.Options) { o.StoredKeys = true }, solverView{stored: true}},
		{"StableDiscovery", func(o *resolver.Options) { o.StableDiscovery = true }, solverView{stable: true}},
		{"OwnOrder", func(o *resolver.Options) { o.OwnOrder = true }, solverView{own: true}},
		{"ClashThreshold", func(o *resolver.Options) { o.ClashThreshold = 7 }, solverView{clash: 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := solverSeen(t, tc.mutate); got != tc.want {
				t.Errorf("solver settings = %+v, want %+v", got, tc.want)
			}
		})
	}
}
