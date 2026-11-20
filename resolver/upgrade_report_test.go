// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// expiringCtx is a ctx the test expires by hand, as a deadline would.
type expiringCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
	hit  atomic.Bool
}

func newExpiringCtx() *expiringCtx {
	return &expiringCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiringCtx) Done() <-chan struct{} { return c.done }

func (c *expiringCtx) Err() error {
	if c.hit.Load() {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *expiringCtx) expire() {
	c.once.Do(func() { c.hit.Store(true); close(c.done) })
}

// passOff resolves reqs with the pass off.
func passOff(t *testing.T, idx index.MetadataIndex, reqs []string) *resolver.Resolution {
	t.Helper()
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestUpgradeReportIsNilWithThePassOff(t *testing.T) {
	idx, reqs := earlyGain()
	if res := passOff(t, idx, reqs); res.Upgrade != nil {
		t.Errorf("Upgrade = %+v with the pass off, want nil", res.Upgrade)
	}
}

// A ctx that ends during a re-solve stops the pass by time. The pass keeps its
// best answer so far, and First is the pass-off answer.
func TestUpgradeReportCtxEndedMidReSolve(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, func())
	}{
		{"deadline", func() (context.Context, func()) { c := newExpiringCtx(); return c, c.expire }},
		{"cancel", func() (context.Context, func()) { return context.WithCancel(context.Background()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx, reqs := earlyGain()
			want := passOff(t, idx, reqs)
			ctx, stop := tc.ctx()
			defer stop()
			solves := 0
			// Solve 1 is the first solve, 2 raises a, 3 is stopped.
			defer resolver.OnSolve(func() {
				if solves++; solves == 3 {
					stop()
				}
			})()
			opts := testOptions(t)
			opts.UpgradeRoots = 40
			opts.UpgradeStepFactor = 100
			res, err := resolver.Resolve(ctx, mustRequirements(t, reqs...), idx, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := pins(t, res); !reflect.DeepEqual(got, earlyA) {
				t.Errorf("pins %v, want the best so far %v", got, earlyA)
			}
			if res.Upgrade == nil {
				t.Fatal("Upgrade is nil")
			}
			if !res.Upgrade.StoppedByTime {
				t.Error("StoppedByTime = false, want true")
			}
			if !reflect.DeepEqual(res.Upgrade.First, want) {
				t.Errorf("First = %+v, want the pass-off answer %+v", res.Upgrade.First, want)
			}
		})
	}
}

// resolveClocked resolves reqs with the pass on, on a fake clock where each
// solve takes solveCost.
func resolveClocked(t *testing.T, idx index.MetadataIndex, reqs []string, mutate func(*resolver.Options)) *resolver.Resolution {
	t.Helper()
	clock := time.Unix(0, 0)
	defer resolver.SetClock(func() time.Time { return clock })()
	defer resolver.OnSolve(func() { clock = clock.Add(solveCost) })()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	mutate(&opts)
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Upgrade == nil {
		t.Fatal("Upgrade is nil with the pass on")
	}
	return res
}

func TestUpgradeReportTimeBoundsStopByTime(t *testing.T) {
	idx, reqs := lateGain()
	want := passOff(t, idx, reqs)
	for name, set := range map[string]func(*resolver.Options){
		"max time": func(o *resolver.Options) { o.UpgradeMaxTime = solveCost },
		"factor":   func(o *resolver.Options) { o.UpgradeTimeFactor = 1 },
	} {
		res := resolveClocked(t, idx, reqs, set)
		if !res.Upgrade.StoppedByTime {
			t.Errorf("%s: StoppedByTime = false, want true", name)
		}
		if !reflect.DeepEqual(res.Upgrade.First, want) {
			t.Errorf("%s: First = %+v, want %+v", name, res.Upgrade.First, want)
		}
	}
}

// A re-solve cut by the time bound while it runs also stops the pass by time.
// The clock moves on every read, so the bound passes between the check that
// starts the re-solve and the re-solve itself.
func TestUpgradeReportReSolveCutByTime(t *testing.T) {
	idx, reqs := lateGain()
	const tick = time.Second
	clock := time.Unix(0, 0)
	defer resolver.SetClock(func() time.Time { c := clock; clock = clock.Add(tick); return c })()
	solves := 0
	defer resolver.OnSolve(func() { solves++ })()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	opts.UpgradeMaxTime = tick + tick/2
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if solves != 2 {
		t.Fatalf("%d solves, want the first solve and one cut re-solve", solves)
	}
	if got := pins(t, res); !reflect.DeepEqual(got, lateBase) {
		t.Errorf("pins %v, want %v", got, lateBase)
	}
	if res.Upgrade == nil || !res.Upgrade.StoppedByTime {
		t.Errorf("Upgrade = %+v, want StoppedByTime", res.Upgrade)
	}
}

// A pass that ends on the step bound, the budget, or by itself is not stopped
// by time, even when each solve is slow.
func TestUpgradeReportOtherEndsAreNotTime(t *testing.T) {
	_, re := unbounded(t)
	idx, reqs := lateGain()
	for _, tc := range []struct {
		name string
		set  func(*resolver.Options)
		want map[string]string
	}{
		{"step bound", func(o *resolver.Options) { o.UpgradeMaxSteps = re[0] + re[1] - 1 }, lateBase},
		{"step bound, loose time bound", func(o *resolver.Options) {
			o.UpgradeMaxSteps = re[0] + re[1] - 1
			o.UpgradeMaxTime = 100 * solveCost
		}, lateBase},
		{"budget", func(o *resolver.Options) { o.UpgradeRoots = 1 }, lateBase},
		{"no candidates left", func(*resolver.Options) {}, lateZ},
	} {
		res := resolveClocked(t, idx, reqs, tc.set)
		if got := pins(t, res); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: pins %v, want %v", tc.name, got, tc.want)
		}
		if res.Upgrade.StoppedByTime {
			t.Errorf("%s: StoppedByTime = true, want false", tc.name)
		}
	}
}

// First and the outer answer share no maps or slices, even when the pass
// keeps the first answer.
func TestUpgradeReportFirstIsNotAliased(t *testing.T) {
	idx, reqs := lateGain()
	res := resolveClocked(t, idx, reqs, func(o *resolver.Options) { o.UpgradeRoots = 1 })
	first := res.Upgrade.First
	if first == res {
		t.Fatal("First is the outer Resolution")
	}
	if first.Upgrade != nil {
		t.Errorf("First.Upgrade = %+v, want nil", first.Upgrade)
	}
	if !reflect.DeepEqual(pins(t, first), lateBase) {
		t.Fatalf("First pins %v, want %v", pins(t, first), lateBase)
	}
	res.Pinned[index.NewPackageName("a")] = version.MustParse("9.0")
	if len(res.Order) > 0 {
		res.Order[0] = index.NewPackageName("zzz")
	}
	if !reflect.DeepEqual(pins(t, first), lateBase) {
		t.Errorf("mutating the outer answer changed First: %v", pins(t, first))
	}
	if want := passOff(t, idx, reqs); !reflect.DeepEqual(first.Order, want.Order) {
		t.Errorf("mutating the outer answer changed First.Order: %v", first.Order)
	}
}
