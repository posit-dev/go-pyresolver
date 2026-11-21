// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"slices"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/requirement"
)

// FirstSolve holds a first answer so the UpgradeRoots pass can run later
// without solving it again. Make one with ResolveFirst. It is safe for concurrent use.
type FirstSolve struct {
	reqs []requirement.Requirement
	idx  index.MetadataIndex
	opts Options
	res  *Resolution

	// dur and steps are what the first solve took. The pass's time and step
	// bounds are relative to them.
	dur   time.Duration
	steps int
}

// ResolveFirst runs the first solve of Resolve, with no UpgradeRoots pass. It
// fails as Resolve does. FirstSolve.UpgradePass runs the pass later.
// It keeps idx, RankMemo, WheelTags and Policy by reference: do not change them before UpgradePass.
func ResolveFirst(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
) (*FirstSolve, error) {
	opts.AllowPrerelease = slices.Clone(opts.AllowPrerelease)
	return resolveFirst(ctx, slices.Clone(reqs), idx, opts)
}

// resolveFirst is ResolveFirst without the copies; it keeps reqs and opts.
func resolveFirst(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
) (*FirstSolve, error) {
	start := now()
	steps := &stepMeter{}
	res, err := resolveOnce(ctx, reqs, idx, opts, steps)
	if err != nil {
		return nil, err
	}
	return &FirstSolve{
		reqs: reqs, idx: idx, opts: opts, res: res,
		dur: now().Sub(start), steps: steps.used,
	}, nil
}

// Resolution returns the first answer, the one Resolve returns with the pass
// off. Each call returns a new copy.
func (f *FirstSolve) Resolution() *Resolution {
	return cloneResolution(f.res)
}

// UpgradePass runs the UpgradeRoots pass from the first answer and returns what
// Resolve returns for the same inputs. It never fails: with UpgradeRoots at zero
// or ctx already ended, it returns the first answer.
func (f *FirstSolve) UpgradePass(ctx context.Context) *Resolution {
	return f.pass(ctx, cloneResolution(f.res))
}

// pass runs the pass from base, an answer equal to f.res that the result may
// hold. With the pass off it returns base.
func (f *FirstSolve) pass(ctx context.Context, base *Resolution) *Resolution {
	if f.opts.UpgradeRoots <= 0 {
		return base
	}
	return upgradeRoots(ctx, f.reqs, f.idx, f.opts, base, f.dur, f.steps)
}
