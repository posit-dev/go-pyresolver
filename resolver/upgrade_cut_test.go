// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// stallFailSafe ends a stalled read whose ctx never ends.
const stallFailSafe = 5 * time.Second

// stallIndex, once stall is set, blocks each read whose ctx has a deadline
// until that ctx ends. onRead sees each read's ctx.
type stallIndex struct {
	index.MetadataIndex
	stall  *atomic.Bool
	onRead func(context.Context)
}

func (s stallIndex) read(ctx context.Context) error {
	if s.onRead != nil {
		s.onRead(ctx)
	}
	if _, has := ctx.Deadline(); !has || !s.stall.Load() {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(stallFailSafe):
		return nil
	}
}

func (s stallIndex) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	if err := s.read(ctx); err != nil {
		return nil, err
	}
	return s.MetadataIndex.Versions(ctx, pkg)
}

func (s stallIndex) Metadata(ctx context.Context, pkg index.PackageName, ver version.Version) (index.PackageMetadata, error) {
	if err := s.read(ctx); err != nil {
		return index.PackageMetadata{}, err
	}
	return s.MetadataIndex.Metadata(ctx, pkg, ver)
}

func (s stallIndex) Files(ctx context.Context, pkg index.PackageName, ver version.Version) ([]index.DistFile, error) {
	if err := s.read(ctx); err != nil {
		return nil, err
	}
	return s.MetadataIndex.Files(ctx, pkg, ver)
}

// resolveStalled resolves reqs on the real clock; solves from stallFrom on
// (base is 1) stall until their ctx ends. It returns pins, re-solves, time.
func resolveStalled(t *testing.T, ctx context.Context, m *index.MockIndex, reqs []string, stallFrom int, mutate func(*resolver.Options)) (map[string]string, int, time.Duration) {
	t.Helper()
	stall := &atomic.Bool{}
	solves := 0
	defer resolver.OnSolve(func() {
		solves++
		stall.Store(solves >= stallFrom)
	})()
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	mutate(&opts)
	start := time.Now()
	res, err := resolver.Resolve(ctx, mustRequirements(t, reqs...), stallIndex{MetadataIndex: m, stall: stall}, opts)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("a cut pass must not fail the resolution: %v", err)
	}
	return pins(t, res), solves - 1, took
}

const cutBound = 100 * time.Millisecond

// A re-solve still running when the bound passes is cut there: the base
// answer comes back, and no further re-solve starts.
func TestUpgradeTimeBoundCutsARunningReSolve(t *testing.T) {
	idx, reqs := lateGain()
	got, attempts, took := resolveStalled(t, context.Background(), idx, reqs, 2,
		func(o *resolver.Options) { o.UpgradeMaxTime = cutBound })
	if !reflect.DeepEqual(got, lateBase) || attempts != 1 {
		t.Errorf("got %v after %d attempts, want %v after 1", got, attempts, lateBase)
	}
	if took >= stallFailSafe {
		t.Errorf("Resolve took %v: the re-solve was not cut at the bound", took)
	}
}

// A cut after a gain keeps the gain.
func TestUpgradeTimeBoundCutKeepsBestSoFar(t *testing.T) {
	idx, reqs := earlyGain()
	got, attempts, took := resolveStalled(t, context.Background(), idx, reqs, 3,
		func(o *resolver.Options) { o.UpgradeMaxTime = cutBound })
	if !reflect.DeepEqual(got, earlyA) || attempts != 2 {
		t.Errorf("got %v after %d attempts, want %v after 2", got, attempts, earlyA)
	}
	if took >= stallFailSafe {
		t.Errorf("Resolve took %v: the re-solve was not cut at the bound", took)
	}
}

// The parent's own deadline still ends the pass as before: the base answer and
// no error, with or without a bound.
func TestUpgradeParentDeadlineUnchangedByBound(t *testing.T) {
	for _, maxTime := range []time.Duration{0, time.Hour} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		idx, reqs := earlyGain()
		var stalled atomic.Bool
		solves := 0
		restore := resolver.OnSolve(func() {
			solves++
			if solves == 3 {
				stalled.Store(true)
				cancel() // the parent ends as the second re-solve starts
			}
		})
		opts := testOptions(t)
		opts.UpgradeRoots = 40
		opts.UpgradeMaxTime = maxTime
		res, err := resolver.Resolve(ctx, mustRequirements(t, reqs...), stallIndex{MetadataIndex: idx, stall: &stalled}, opts)
		restore()
		cancel()
		if err != nil {
			t.Fatalf("max %v: %v", maxTime, err)
		}
		if got := pins(t, res); !reflect.DeepEqual(got, earlyA) || solves != 3 {
			t.Errorf("max %v: got %v after %d solves, want %v after 3", maxTime, got, solves, earlyA)
		}
	}
}

// Each re-solve's deadline is what the pass has left. Each solve takes 10s of
// fake time and the bound is 2.5 solves, so re-solves get 25s, 15s and 5s.
func TestUpgradeReSolveDeadlineIsPassRemaining(t *testing.T) {
	idx, reqs := lateGain()
	clock := time.Unix(0, 0)
	defer resolver.SetClock(func() time.Time { return clock })()
	var left []time.Duration
	solves := 0
	defer resolver.OnSolve(func() { solves++; clock = clock.Add(solveCost) })()
	seen := 0
	onRead := func(ctx context.Context) {
		if solves < 2 || seen == solves {
			return
		}
		seen = solves
		if d, ok := ctx.Deadline(); ok {
			left = append(left, time.Until(d))
		} else {
			left = append(left, -1)
		}
	}
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	opts.UpgradeTimeFactor = 2.5
	_, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...),
		stallIndex{MetadataIndex: idx, stall: &atomic.Bool{}, onRead: onRead}, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{25 * time.Second, 15 * time.Second, 5 * time.Second}
	if len(left) != len(want) {
		t.Fatalf("re-solve deadlines %v, want about %v", left, want)
	}
	for i := range want {
		if left[i] > want[i] || left[i] < want[i]-time.Second {
			t.Errorf("re-solve %d had %v left, want about %v", i+1, left[i], want[i])
		}
	}
}

// With no bound, a re-solve runs under the caller's ctx unchanged.
func TestUpgradeNoBoundAddsNoDeadline(t *testing.T) {
	idx, reqs := lateGain()
	deadlines := 0
	onRead := func(ctx context.Context) {
		if _, ok := ctx.Deadline(); ok {
			deadlines++
		}
	}
	opts := testOptions(t)
	opts.UpgradeRoots = 40
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...),
		stallIndex{MetadataIndex: idx, stall: &atomic.Bool{}, onRead: onRead}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := pins(t, res); !reflect.DeepEqual(got, lateZ) || deadlines != 0 {
		t.Errorf("got %v with %d reads under a deadline, want %v with none", got, deadlines, lateZ)
	}
}

func TestUpgradeTimeBoundCutIsDeterministic(t *testing.T) {
	for range 3 {
		idx, reqs := earlyGain()
		got, attempts, _ := resolveStalled(t, context.Background(), idx, reqs, 3,
			func(o *resolver.Options) { o.UpgradeMaxTime = cutBound })
		if !reflect.DeepEqual(got, earlyA) || attempts != 2 {
			t.Fatalf("got %v after %d attempts, want %v after 2", got, attempts, earlyA)
		}
	}
}
