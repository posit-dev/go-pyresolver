// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// ctxBlind hides the caller's context from the index, so only the solver's
// own check can notice a cancel. onRead runs before each read.
type ctxBlind struct {
	index.MetadataIndex
	onRead func()
}

func (c ctxBlind) read() {
	if c.onRead != nil {
		c.onRead()
	}
}

func (c ctxBlind) Versions(_ context.Context, pkg index.PackageName) ([]version.Version, error) {
	c.read()
	return c.MetadataIndex.Versions(context.Background(), pkg)
}

func (c ctxBlind) Metadata(_ context.Context, pkg index.PackageName, ver version.Version) (index.PackageMetadata, error) {
	c.read()
	return c.MetadataIndex.Metadata(context.Background(), pkg, ver)
}

func (c ctxBlind) Files(_ context.Context, pkg index.PackageName, ver version.Version) ([]index.DistFile, error) {
	c.read()
	return c.MetadataIndex.Files(context.Background(), pkg, ver)
}

func TestResolveExpiredContextStopsTheSolve(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	idx := ctxBlind{MetadataIndex: upgradeFixture()}

	start := time.Now()
	res, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b"), idx, testOptions(t))
	if err == nil {
		t.Fatalf("an expired context was ignored: got %v", pins(t, res))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false for %T: %v", err, err)
	}
	if !errors.Is(err, solver.ErrInterrupted) {
		t.Errorf("errors.Is(err, solver.ErrInterrupted) = false for %T: %v", err, err)
	}
	var re *resolver.ResolutionError
	if errors.As(err, &re) {
		t.Errorf("a timeout must not be reported as a conflict: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v to stop", d)
	}
}

func TestResolveCancelMidSolveStopsTheSolve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	idx := ctxBlind{MetadataIndex: upgradeFixture(), onRead: func() {
		reads++
		if reads == 2 {
			cancel()
		}
	}}

	_, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b"), idx, testOptions(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false for %T: %v", err, err)
	}
}

// The upgrade pass's re-solves get the interrupt too: a cancel as one starts
// stops it before it decides anything past the root.
func TestUpgradeRootsCancelStopsTheReSolve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	solves := 0
	defer resolver.OnSolve(func() {
		solves++
		if solves == 2 {
			cancel()
		}
	})()
	var solvers []*solver.Solver[provider.Package, pep440set.Set]
	defer resolver.OnSolver(func(s *solver.Solver[provider.Package, pep440set.Set]) {
		solvers = append(solvers, s)
	})()

	opts := testOptions(t)
	opts.UpgradeRoots = 12
	idx := ctxBlind{MetadataIndex: upgradeFixture()}
	res, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b"), idx, opts)
	if err != nil {
		t.Fatalf("a cut pass must not fail the resolution: %v", err)
	}
	if got := pins(t, res); got["a"] != "1.0" {
		t.Errorf("got %v, want the base answer", got)
	}
	if len(solvers) != 2 {
		t.Fatalf("ran %d solves, want 2", len(solvers))
	}
	if first, second := solvers[0].Stats.Decisions, solvers[1].Stats.Decisions; second > 1 || first <= second {
		t.Errorf("re-solve made %d decisions after the cancel (base solve made %d)", second, first)
	}
}
