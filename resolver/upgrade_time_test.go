// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

const solveCost = 10 * time.Second

// resolveTimed resolves reqs on a fake clock where each solve takes solveCost.
// It returns the pins and the number of re-solves started.
func resolveTimed(t *testing.T, idx *index.MockIndex, reqs []string, mutate func(*resolver.Options)) (map[string]string, int) {
	t.Helper()
	clock := time.Unix(0, 0)
	defer resolver.SetClock(func() time.Time { return clock })()
	solves := 0
	defer resolver.OnSolve(func() { solves++; clock = clock.Add(solveCost) })()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	mutate(&opts)
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pins(t, res), solves - 1
}

// The first re-solve raises a. Later ones raise nothing.
func earlyGain() (*index.MockIndex, []string) {
	return upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").AddVersion("c", "3.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2"), []string{"a", "b", "c", "d"}
}

// The pass tries a first (furthest behind) and fails: b==5.0 needs a<2, so the
// re-solve has no answer. Only the second re-solve raises z.
func lateGain() (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").AddVersion("a", "2.0").AddVersion("a", "3.0").AddVersion("a", "4.0").
		AddVersion("b", "5.0", "a<2").
		AddVersion("z", "1.0").AddVersion("z", "2.0").AddVersion("z", "3.0").
		AddVersion("y", "1.0").AddVersion("y", "2.0", "z<2")
	return idx, []string{"a", "b==5.0", "y", "z"}
}

var (
	earlyA   = map[string]string{"a": "3.0", "b": "1.0", "c": "1.0", "d": "2.0"}
	lateBase = map[string]string{"a": "1.0", "b": "5.0", "y": "2.0", "z": "1.0"}
	lateZ    = map[string]string{"a": "1.0", "b": "5.0", "y": "1.0", "z": "3.0"}
)

func TestUpgradeTimeFactorZeroIsUnbounded(t *testing.T) {
	idx, reqs := lateGain()
	got, attempts := resolveTimed(t, idx, reqs, func(*resolver.Options) {})
	if !reflect.DeepEqual(got, lateZ) || attempts != 3 {
		t.Errorf("got %v after %d attempts, want %v after 3", got, attempts, lateZ)
	}
}

// Each re-solve takes as long as the first solve, so factor F allows ceil(F)
// attempts: one starts while the pass has run for less than F solves.
func TestUpgradeTimeFactorStopsAfterExpectedAttempts(t *testing.T) {
	type want struct {
		attempts int
		pins     map[string]string
	}
	for _, tc := range []struct {
		name    string
		fixture func() (*index.MockIndex, []string)
		factor  float64
		want    want
	}{
		{"late 0.5", lateGain, 0.5, want{1, lateBase}}, // 0 < 0.5 starts one; 1 >= 0.5 stops
		{"late 1", lateGain, 1, want{1, lateBase}},
		{"late 1.5", lateGain, 1.5, want{2, lateZ}},
		{"late 2", lateGain, 2, want{2, lateZ}},
		{"late 50", lateGain, 50, want{3, lateZ}}, // the pass ends by itself
		{"late +Inf", lateGain, math.Inf(1), want{3, lateZ}},
		{"early 1", earlyGain, 1, want{1, earlyA}}, // best so far is kept
		{"early 50", earlyGain, 50, want{3, earlyA}},
	} {
		idx, reqs := tc.fixture()
		got, attempts := resolveTimed(t, idx, reqs, func(o *resolver.Options) { o.UpgradeTimeFactor = tc.factor })
		if !reflect.DeepEqual(got, tc.want.pins) || attempts != tc.want.attempts {
			t.Errorf("%s: got %v after %d attempts, want %v after %d",
				tc.name, got, attempts, tc.want.pins, tc.want.attempts)
		}
	}
}

func TestUpgradeMaxTimeStopsPass(t *testing.T) {
	idx, reqs := lateGain()
	got, attempts := resolveTimed(t, idx, reqs, func(o *resolver.Options) { o.UpgradeMaxTime = solveCost })
	if !reflect.DeepEqual(got, lateBase) || attempts != 1 {
		t.Errorf("got %v after %d attempts, want %v after 1", got, attempts, lateBase)
	}
}

// The tighter of the two bounds wins.
func TestUpgradeTimeBoundsCombine(t *testing.T) {
	idx, reqs := lateGain()
	got, attempts := resolveTimed(t, idx, reqs, func(o *resolver.Options) {
		o.UpgradeTimeFactor = 10
		o.UpgradeMaxTime = solveCost
	})
	if !reflect.DeepEqual(got, lateBase) || attempts != 1 {
		t.Errorf("max tighter: got %v after %d attempts", got, attempts)
	}
	got, attempts = resolveTimed(t, idx, reqs, func(o *resolver.Options) {
		o.UpgradeTimeFactor = 1
		o.UpgradeMaxTime = time.Hour
	})
	if !reflect.DeepEqual(got, lateBase) || attempts != 1 {
		t.Errorf("factor tighter: got %v after %d attempts", got, attempts)
	}
}

// A bound spent before the first re-solve returns the first answer, no error.
func TestUpgradeTimeBoundBeforeFirstAttemptReturnsBase(t *testing.T) {
	idx := upgradeFixture()
	clock := time.Unix(0, 0)
	defer resolver.SetClock(func() time.Time { return clock })()
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	opts.UpgradeTimeFactor = 1 // the fake clock never moves: base took 0
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "a", "b"), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"a": "1.0", "b": "2.0"}; !reflect.DeepEqual(pins(t, res), want) {
		t.Errorf("got %v, want the base answer %v", pins(t, res), want)
	}
}

func TestUpgradeTimeBoundIsDeterministicOnAFakeClock(t *testing.T) {
	idx, reqs := earlyGain()
	set := func(o *resolver.Options) { o.UpgradeTimeFactor = 1 }
	first, n1 := resolveTimed(t, idx, reqs, set)
	for range 5 {
		got, n := resolveTimed(t, idx, reqs, set)
		if !reflect.DeepEqual(got, first) || n != n1 {
			t.Fatalf("run differs: %v/%d vs %v/%d", got, n, first, n1)
		}
	}
}
