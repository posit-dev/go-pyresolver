// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"math"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// resolveSteps resolves reqs with the pass on and returns the pins and the
// steps of each resolution: the first solve, then each re-solve.
func resolveSteps(t *testing.T, idx index.MetadataIndex, reqs []string, mutate func(*resolver.Options)) (map[string]string, []int) {
	t.Helper()
	got, restore := resolver.RecordSteps()
	defer restore()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	mutate(&opts)
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pins(t, res), got()
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// unbounded measures the pass with no bound: lateGain runs three re-solves
// (a fails, z gains, y fails).
func unbounded(t *testing.T) (base int, re []int) {
	t.Helper()
	idx, reqs := lateGain()
	got, steps := resolveSteps(t, idx, reqs, func(*resolver.Options) {})
	if !reflect.DeepEqual(got, lateZ) || len(steps) != 4 {
		t.Fatalf("unbounded: got %v with steps %v, want %v after 3 re-solves", got, steps, lateZ)
	}
	for _, n := range steps {
		if n == 0 {
			t.Fatalf("a resolution took no steps: %v", steps)
		}
	}
	return steps[0], steps[1:]
}

// The budget is shared by all re-solves: one step short of the first two
// re-solves cuts the second, which held the only gain.
func TestUpgradeMaxStepsIsChargedAcrossReSolves(t *testing.T) {
	_, re := unbounded(t)
	idx, reqs := lateGain()

	got, steps := resolveSteps(t, idx, reqs, func(o *resolver.Options) { o.UpgradeMaxSteps = re[0] + re[1] - 1 })
	if !reflect.DeepEqual(got, lateBase) {
		t.Errorf("one step short: got %v, want the base %v", got, lateBase)
	}
	if want := []int{re[0], re[1] - 1}; !reflect.DeepEqual(steps[1:], want) {
		t.Errorf("one step short: re-solve steps %v, want %v", steps[1:], want)
	}

	got, steps = resolveSteps(t, idx, reqs, func(o *resolver.Options) { o.UpgradeMaxSteps = re[0] + re[1] })
	if !reflect.DeepEqual(got, lateZ) {
		t.Errorf("exact budget: got %v, want %v", got, lateZ)
	}
	// The budget is spent, so the third re-solve never starts.
	if !reflect.DeepEqual(steps[1:], re[:2]) {
		t.Errorf("exact budget: re-solve steps %v, want %v", steps[1:], re[:2])
	}
}

// The factor scales the first solve's steps, rounding down.
func TestUpgradeStepFactorScalesBaseSteps(t *testing.T) {
	base, re := unbounded(t)
	idx, reqs := lateGain()
	for _, tc := range []struct {
		name   string
		budget int
		want   map[string]string
	}{
		{"short", re[0] + re[1] - 1, lateBase},
		{"exact", re[0] + re[1], lateZ},
	} {
		factor := (float64(tc.budget) + 0.5) / float64(base)
		got, steps := resolveSteps(t, idx, reqs, func(o *resolver.Options) { o.UpgradeStepFactor = factor })
		if !reflect.DeepEqual(got, tc.want) || sum(steps[1:]) != tc.budget {
			t.Errorf("%s: got %v using %d steps, want %v using %d", tc.name, got, sum(steps[1:]), tc.want, tc.budget)
		}
	}
}

// A budget of no steps returns the first answer and starts no re-solve.
func TestUpgradeZeroStepBudgetReturnsBase(t *testing.T) {
	idx, reqs := lateGain()
	for _, set := range []func(*resolver.Options){
		func(o *resolver.Options) { o.UpgradeStepFactor = 1e-9 },
		func(o *resolver.Options) { o.UpgradeStepFactor = math.SmallestNonzeroFloat64 },
	} {
		got, steps := resolveSteps(t, idx, reqs, set)
		if !reflect.DeepEqual(got, lateBase) || len(steps) != 1 {
			t.Errorf("got %v after %d re-solves, want the base %v after none", got, len(steps)-1, lateBase)
		}
	}
	// One step: the first re-solve starts and is cut.
	got, steps := resolveSteps(t, idx, reqs, func(o *resolver.Options) { o.UpgradeMaxSteps = 1 })
	if !reflect.DeepEqual(got, lateBase) || !reflect.DeepEqual(steps[1:], []int{1}) {
		t.Errorf("one step: got %v with re-solve steps %v", got, steps[1:])
	}
}

// Zero, negative, NaN and huge bounds are no bound: the pass is unchanged.
func TestUpgradeStepBoundOffIsUnbounded(t *testing.T) {
	_, re := unbounded(t)
	idx, reqs := lateGain()
	for _, set := range []func(*resolver.Options){
		func(o *resolver.Options) { o.UpgradeMaxSteps = -1 },
		func(o *resolver.Options) { o.UpgradeStepFactor = -1 },
		func(o *resolver.Options) { o.UpgradeStepFactor = math.NaN() },
		func(o *resolver.Options) { o.UpgradeStepFactor = math.Inf(1) },
		func(o *resolver.Options) { o.UpgradeStepFactor = math.MaxFloat64 },
	} {
		got, steps := resolveSteps(t, idx, reqs, set)
		if !reflect.DeepEqual(got, lateZ) || !reflect.DeepEqual(steps[1:], re) {
			t.Errorf("got %v with re-solve steps %v, want %v with %v", got, steps[1:], lateZ, re)
		}
	}
}

// The lower of the two step bounds wins.
func TestUpgradeStepBoundsCombine(t *testing.T) {
	base, re := unbounded(t)
	idx, reqs := lateGain()
	short := re[0] + re[1] - 1
	for _, set := range []func(*resolver.Options){
		func(o *resolver.Options) { o.UpgradeMaxSteps = short; o.UpgradeStepFactor = 1000 },
		func(o *resolver.Options) {
			o.UpgradeMaxSteps = 1 << 30
			o.UpgradeStepFactor = (float64(short) + 0.5) / float64(base)
		},
	} {
		got, steps := resolveSteps(t, idx, reqs, set)
		if !reflect.DeepEqual(got, lateBase) || sum(steps[1:]) != short {
			t.Errorf("got %v using %d steps, want %v using %d", got, sum(steps[1:]), lateBase, short)
		}
	}
}

// loadClock is a clock that jumps a random amount on every read, standing in
// for a loaded machine. Each run gets its own jumps.
func loadClock(seed int64) func() time.Time {
	rng := rand.New(rand.NewSource(seed))
	var mu sync.Mutex
	at := time.Unix(0, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Duration(rng.Int63n(int64(time.Millisecond))))
		return at
	}
}

// busy keeps n goroutines spinning until stop is called.
func busy(n int) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	return func() { close(done); wg.Wait() }
}

// determinismCases are random indexes where the unbounded pass changes the
// answer, each with a step budget that cuts the pass partway.
func determinismCases(t *testing.T) []struct {
	idx    index.MetadataIndex
	reqs   []string
	budget int
} {
	t.Helper()
	var out []struct {
		idx    index.MetadataIndex
		reqs   []string
		budget int
	}
	for seed := int64(0); seed < 300 && len(out) < 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		idx, reqs := randomIndex(rng, 4+rng.Intn(4))
		opts := testOptions(t)
		if _, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts); err != nil {
			continue
		}
		_, steps := resolveSteps(t, idx, reqs, func(*resolver.Options) {})
		if len(steps) < 3 {
			continue // fewer than two re-solves: no budget cuts it partway
		}
		out = append(out, struct {
			idx    index.MetadataIndex
			reqs   []string
			budget int
		}{idx, reqs, sum(steps[1:]) / 2})
	}
	if len(out) < 5 {
		t.Fatalf("only %d cases with two or more re-solves", len(out))
	}
	return out
}

// With a step budget, the same inputs give the same pins and the same steps on
// every run, whatever the clock, GOMAXPROCS or other work on the machine. A
// time cap that the jumping clock never reaches is set too, as a caller would.
func TestUpgradeStepBudgetIsDeterministic(t *testing.T) {
	cases := determinismCases(t)
	runs := 20
	if raceEnabled {
		runs = 5
	}
	for i, c := range cases {
		var first map[string]string
		var firstSteps []int
		for _, procs := range []int{1, 8} {
			prev := runtime.GOMAXPROCS(procs)
			stop := busy(4)
			for run := range runs {
				restore := resolver.SetClock(loadClock(int64(run)))
				got, steps := resolveSteps(t, c.idx, c.reqs, func(o *resolver.Options) {
					o.UpgradeMaxSteps = c.budget
					o.UpgradeMaxTime = time.Hour
				})
				restore()
				if first == nil {
					first, firstSteps = got, steps
					continue
				}
				if !reflect.DeepEqual(got, first) || !reflect.DeepEqual(steps, firstSteps) {
					t.Errorf("case %d GOMAXPROCS %d run %d: %v steps %v, first run %v steps %v",
						i, procs, run, got, steps, first, firstSteps)
				}
			}
			stop()
			runtime.GOMAXPROCS(prev)
		}
	}
}

// The jumping clock does move a time-bounded pass, so the test above would
// see a dependence on the clock if the step budget had one.
func TestUpgradeTimeBoundMovesWithLoadClock(t *testing.T) {
	for _, c := range determinismCases(t) {
		seen := map[int]bool{}
		for run := range 20 {
			restore := resolver.SetClock(loadClock(int64(run)))
			_, steps := resolveSteps(t, c.idx, c.reqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1.5 })
			restore()
			seen[len(steps)] = true
		}
		if len(seen) > 1 {
			return
		}
	}
	t.Error("no case started a different number of re-solves across runs: the clock is not exercised")
}
