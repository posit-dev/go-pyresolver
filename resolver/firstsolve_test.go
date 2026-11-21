// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// twoPhaseCase is one fixture with the options the pass runs under.
type twoPhaseCase struct {
	name string
	idx  index.MetadataIndex
	reqs []string
	set  func(*resolver.Options)
}

// twoPhaseCases are the upgrade fixtures the equivalence tests run.
func twoPhaseCases(t *testing.T) []twoPhaseCase {
	t.Helper()
	none := func(*resolver.Options) {}
	early, earlyReqs := earlyGain()
	late, lateReqs := lateGain()
	base, re := unbounded(t)
	// factor reaches budget steps from the first solve's steps, rounding down.
	factor := func(budget int) float64 { return (float64(budget) + 0.5) / float64(base) }
	cases := []twoPhaseCase{
		{"major behind", upgradeFixture(), []string{"a", "b"}, none},
		{"early gain", early, earlyReqs, none},
		{"late gain", late, lateReqs, none},
		{"time factor 1", late, lateReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1 }},
		{"time factor 1.5", late, lateReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1.5 }},
		{"early time factor 1", early, earlyReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1 }},
		{"max time", late, lateReqs, func(o *resolver.Options) { o.UpgradeMaxTime = solveCost }},
		{"step factor short", late, lateReqs, func(o *resolver.Options) { o.UpgradeStepFactor = factor(re[0] + re[1] - 1) }},
		{"step factor exact", late, lateReqs, func(o *resolver.Options) { o.UpgradeStepFactor = factor(re[0] + re[1]) }},
		{"max steps", late, lateReqs, func(o *resolver.Options) { o.UpgradeMaxSteps = re[0] }},
		{"budget 1", late, lateReqs, func(o *resolver.Options) { o.UpgradeRoots = 1 }},
	}
	for _, d := range rootMovingDownCases() {
		cases = append(cases, twoPhaseCase{"down: " + d.name, d.idx, d.reqs, none})
	}
	return cases
}

// rootMovingDownCases are the fixtures of TestUpgradeRootsRootMovingDown.
func rootMovingDownCases() []struct {
	name string
	idx  *index.MockIndex
	reqs []string
} {
	return []struct {
		name string
		idx  *index.MockIndex
		reqs []string
	}{
		{"final to an older pre-release", index.NewMockIndex("test").
			AddVersion("a", "1.0rc1").
			AddVersion("a", "1.0", "n<2").AddVersion("a", "1.1", "n<2").
			AddVersion("n", "1.0").AddVersion("n", "1.1").
			AddVersion("n", "1.2").AddVersion("n", "2.0"), []string{"a==1.*", "n"}},
		{"final to an older final", index.NewMockIndex("test").
			AddVersion("a", "1.0").
			AddVersion("a", "1.1", "n<2").
			AddVersion("n", "1.0").AddVersion("n", "1.1").AddVersion("n", "2.0"), []string{"a", "n"}},
		{"pre-release to a newer final", index.NewMockIndex("test").
			AddVersion("p", "1.0rc1").
			AddVersion("p", "2.0", "q<2").AddVersion("p", "3.0", "q<2").
			AddVersion("q", "1.0").AddVersion("q", "2.0"), []string{"p", "q"}},
	}
}

// clocked runs f on a fake clock where each solve takes solveCost, and
// returns the steps of each resolution f ran.
func clocked(f func()) []int {
	return clockedGap(func(func(time.Duration)) { f() })
}

// clockedGap is clocked, but f may also move the clock with advance.
func clockedGap(f func(advance func(time.Duration))) []int {
	clock := time.Unix(0, 0)
	restoreClock := resolver.SetClock(func() time.Time { return clock })
	defer restoreClock()
	defer resolver.OnSolve(func() { clock = clock.Add(solveCost) })()
	steps, restore := resolver.RecordSteps()
	defer restore()
	f(func(d time.Duration) { clock = clock.Add(d) })
	return steps()
}

func twoPhaseOptions(t *testing.T, set func(*resolver.Options)) resolver.Options {
	t.Helper()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	set(&opts)
	return opts
}

// The first solve then UpgradePass gives exactly what Resolve gives, report
// included, and re-solves at the same steps.
func TestTwoPhaseMatchesResolve(t *testing.T) {
	for _, tc := range twoPhaseCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			opts := twoPhaseOptions(t, tc.set)
			reqs := mustRequirements(t, tc.reqs...)
			var want, got *resolver.Resolution
			wantSteps := clocked(func() {
				var err error
				if want, err = resolver.Resolve(context.Background(), reqs, tc.idx, opts); err != nil {
					t.Fatal(err)
				}
			})
			gotSteps := clocked(func() {
				first, err := resolver.ResolveFirst(context.Background(), reqs, tc.idx, opts)
				if err != nil {
					t.Fatal(err)
				}
				got = first.UpgradePass(context.Background())
			})
			if want.Upgrade == nil {
				t.Fatal("Resolve gave no report with the pass on")
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("two phases gave\n%+v (report %+v)\nResolve gave\n%+v (report %+v)",
					got, got.Upgrade, want, want.Upgrade)
			}
			if !reflect.DeepEqual(gotSteps, wantSteps) {
				t.Errorf("steps per solve: two phases %v, Resolve %v", gotSteps, wantSteps)
			}
		})
	}
}

// Time between ResolveFirst and UpgradePass counts toward neither the first
// solve's duration nor the pass's time.
func TestTwoPhaseGapBetweenPhasesIsNotCounted(t *testing.T) {
	late, lateReqs := lateGain()
	early, earlyReqs := earlyGain()
	for _, tc := range []twoPhaseCase{
		{"time factor 1", late, lateReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1 }},
		{"time factor 1.5", late, lateReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1.5 }},
		{"early time factor 1", early, earlyReqs, func(o *resolver.Options) { o.UpgradeTimeFactor = 1 }},
		{"max time 1 solve", late, lateReqs, func(o *resolver.Options) { o.UpgradeMaxTime = solveCost }},
		{"max time 2 solves", late, lateReqs, func(o *resolver.Options) { o.UpgradeMaxTime = 2 * solveCost }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := twoPhaseOptions(t, tc.set)
			reqs := mustRequirements(t, tc.reqs...)
			var want, got *resolver.Resolution
			wantSteps := clocked(func() {
				var err error
				if want, err = resolver.Resolve(context.Background(), reqs, tc.idx, opts); err != nil {
					t.Fatal(err)
				}
			})
			gotSteps := clockedGap(func(advance func(time.Duration)) {
				first, err := resolver.ResolveFirst(context.Background(), reqs, tc.idx, opts)
				if err != nil {
					t.Fatal(err)
				}
				advance(time.Hour)
				got = first.UpgradePass(context.Background())
			})
			if !reflect.DeepEqual(got, want) {
				t.Errorf("after a gap, two phases gave %v (report %+v), Resolve gave %v (report %+v)",
					pins(t, got), got.Upgrade, pins(t, want), want.Upgrade)
			}
			if !reflect.DeepEqual(gotSteps, wantSteps) {
				t.Errorf("steps per solve: two phases %v, Resolve %v", gotSteps, wantSteps)
			}
		})
	}
}

// The pass uses the AllowPrerelease the first solve saw, even if the caller
// changes its slice in between.
func TestTwoPhaseKeepsAllowPrerelease(t *testing.T) {
	// a 3.1rc1 is newest; named in AllowPrerelease it outranks 3.0, and the pass
	// will not move a root from a final to a pre-release.
	idx := upgradeFixture().AddVersion("a", "3.1rc1")
	reqs := mustRequirements(t, "a", "b")
	opts := twoPhaseOptions(t, func(*resolver.Options) {})
	without, err := resolver.Resolve(context.Background(), reqs, idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.AllowPrerelease = []index.PackageName{index.NewPackageName("a")}
	want, err := resolver.Resolve(context.Background(), reqs, idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(pins(t, want), pins(t, without)) {
		t.Fatalf("fixture: AllowPrerelease changes nothing (%v)", pins(t, want))
	}
	first, err := resolver.ResolveFirst(context.Background(), reqs, idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.AllowPrerelease[0] = index.NewPackageName("unrelated")
	if got := first.UpgradePass(context.Background()); !reflect.DeepEqual(got, want) {
		t.Errorf("after changing the caller's AllowPrerelease, got %v, want %v", pins(t, got), pins(t, want))
	}
}

// The step bound is relative to the first solve's steps: a factor one step
// short of the gain stops the pass on the same re-solve, at the same step.
func TestTwoPhaseStepBoundIsRelativeToFirstSolve(t *testing.T) {
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
		opts := twoPhaseOptions(t, func(o *resolver.Options) {
			o.UpgradeStepFactor = (float64(tc.budget) + 0.5) / float64(base)
		})
		var got *resolver.Resolution
		steps := clocked(func() {
			first, err := resolver.ResolveFirst(context.Background(), mustRequirements(t, reqs...), idx, opts)
			if err != nil {
				t.Fatal(err)
			}
			got = first.UpgradePass(context.Background())
		})
		if len(steps) == 0 || steps[0] != base {
			t.Fatalf("%s: steps %v, want the first solve at %d", tc.name, steps, base)
		}
		if p := pins(t, got); !reflect.DeepEqual(p, tc.want) || sum(steps[1:]) != tc.budget {
			t.Errorf("%s: got %v using %d steps, want %v using %d", tc.name, p, sum(steps[1:]), tc.want, tc.budget)
		}
		if got.Upgrade == nil || got.Upgrade.StoppedByTime {
			t.Errorf("%s: report %+v, want one not stopped by time", tc.name, got.Upgrade)
		}
	}
}

// A ctx that ended before the pass gives the first answer, stopped by time,
// with no error and no re-solve.
func TestTwoPhaseEndedCtxGivesFirst(t *testing.T) {
	idx, reqs := earlyGain()
	want := passOff(t, idx, reqs)
	first, err := resolver.ResolveFirst(context.Background(), mustRequirements(t, reqs...), idx, twoPhaseOptions(t, func(*resolver.Options) {}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	count, restore := resolver.CountSolves()
	defer restore()
	got := first.UpgradePass(ctx)
	if n := count(); n != 0 {
		t.Errorf("%d re-solves after the ctx ended, want 0", n)
	}
	if got.Upgrade == nil || !got.Upgrade.StoppedByTime {
		t.Fatalf("report %+v, want StoppedByTime", got.Upgrade)
	}
	if !reflect.DeepEqual(got.Upgrade.First, want) {
		t.Errorf("First = %+v, want the pass-off answer %+v", got.Upgrade.First, want)
	}
	kept := *got
	kept.Upgrade = nil
	if !reflect.DeepEqual(&kept, want) {
		t.Errorf("kept %+v, want the first answer %+v", &kept, want)
	}
}

// With the pass off, UpgradePass gives the first answer with no report, and
// Resolution gives the same answer.
func TestTwoPhasePassOff(t *testing.T) {
	idx, reqs := earlyGain()
	want := passOff(t, idx, reqs)
	first, err := resolver.ResolveFirst(context.Background(), mustRequirements(t, reqs...), idx, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := first.UpgradePass(context.Background()); !reflect.DeepEqual(got, want) {
		t.Errorf("UpgradePass = %+v, want %+v", got, want)
	}
	if got := first.Resolution(); !reflect.DeepEqual(got, want) {
		t.Errorf("Resolution = %+v, want %+v", got, want)
	}
}

// ResolveFirst runs no pass even with UpgradeRoots set, and fails as Resolve does.
func TestResolveFirstRunsNoPass(t *testing.T) {
	idx, reqs := earlyGain()
	want := passOff(t, idx, reqs)
	opts := twoPhaseOptions(t, func(*resolver.Options) {})
	count, restore := resolver.CountSolves()
	defer restore()
	first, err := resolver.ResolveFirst(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("%d solves, want 1", n)
	}
	if got := first.Resolution(); !reflect.DeepEqual(got, want) {
		t.Errorf("Resolution = %+v, want %+v", got, want)
	}

	conflict := mustRequirements(t, "a==1.0", "a==2.0")
	_, wantErr := resolver.Resolve(context.Background(), conflict, idx, opts)
	got, err := resolver.ResolveFirst(context.Background(), conflict, idx, opts)
	if wantErr == nil || err == nil || err.Error() != wantErr.Error() || got != nil {
		t.Errorf("ResolveFirst = %v, %v; want nil, %v", got, err, wantErr)
	}
}

// Changing what UpgradePass or Resolution returned, or the caller's inputs,
// does not change a later UpgradePass.
func TestTwoPhaseResultsShareNothing(t *testing.T) {
	idx, reqs := earlyGain()
	opts := twoPhaseOptions(t, func(*resolver.Options) {})
	want, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	in := mustRequirements(t, reqs...)
	first, err := resolver.ResolveFirst(context.Background(), in, idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	in[0] = mustRequirements(t, "a<2")[0]
	for range 2 {
		got := first.UpgradePass(context.Background())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("UpgradePass = %+v, want %+v", got, want)
		}
		for _, r := range []*resolver.Resolution{got, got.Upgrade.First, first.Resolution()} {
			clear(r.Pinned)
			r.Order[0] = "changed"
		}
	}
}

// Many first solves run on some goroutines and their passes on others, at
// once. Run with -race.
func TestTwoPhaseAcrossGoroutines(t *testing.T) {
	type job struct {
		tc    twoPhaseCase
		opts  resolver.Options
		want  *resolver.Resolution
		first *resolver.FirstSolve
	}
	var jobs []*job
	for _, tc := range twoPhaseCases(t) {
		// No fake clock here: a real one makes the time-bound cases vary.
		probe := twoPhaseOptions(t, tc.set)
		if probe.UpgradeTimeFactor > 0 || probe.UpgradeMaxTime > 0 {
			continue
		}
		want, err := resolver.Resolve(context.Background(), mustRequirements(t, tc.reqs...), tc.idx, probe)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, &job{tc: tc, opts: probe, want: want})
	}
	firsts := make(chan *job)
	var solvers sync.WaitGroup
	for _, j := range jobs {
		solvers.Add(1)
		go func() {
			defer solvers.Done()
			first, err := resolver.ResolveFirst(context.Background(), mustRequirements(t, j.tc.reqs...), j.tc.idx, j.opts)
			if err != nil {
				t.Error(err)
				return
			}
			j.first = first
			firsts <- j
		}()
	}
	go func() { solvers.Wait(); close(firsts) }()

	var passes sync.WaitGroup
	for j := range firsts {
		for range 2 {
			passes.Add(1)
			go func() {
				defer passes.Done()
				if got := j.first.UpgradePass(context.Background()); !reflect.DeepEqual(got, j.want) {
					t.Errorf("%s: two phases gave %+v, want %+v", j.tc.name, got, j.want)
				}
			}()
		}
	}
	passes.Wait()
}
