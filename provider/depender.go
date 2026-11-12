// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"slices"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/version"
)

// # Depender runs
//
// go-pubgrub lets a dependency name the whole run of the depending package's
// versions that share it, so one learned conflict rules out the run instead of
// one version per round. widenDepender fills that field.
//
// A run is only ever made of versions that were READ and found to carry the
// same dependency (same package, equal allowed set): the claim "every version
// in Depender depends on X" is then true of each published member. The run
// stops at the first neighbour that differs, cannot be read, has a reason it
// would not be offered, or is rejected for wheel tags. Stopping, not skipping,
// is deliberate: a skipped version is only irrelevant while it stays
// un-offered, and the pre-release bar and yank pins can change during a solve.
// Bounds are published versions, so unpublished versions between members are in
// the range, which is harmless because Candidates never offers them.
//
// Cost control: the first Dependencies call for a package is left narrow, so a
// solve that never revisits a package pays nothing. Later calls (the solver is
// backtracking over that package) read up to a budget of not-yet-seen
// neighbours, doubling per call, and every read is memoized.

const (
	runBudgetStart = 32
	runBudgetMax   = 32 << 8
)

// neighbour is the memoized pure dependency list of one version, or ok=false
// when the version cannot take part in a run.
type neighbour struct {
	deps []dependency
	ok   bool
}

// widenDepender sets Depender on each dependency of (pkg, v) whose run of
// identical neighbours is longer than one.
func (p *Provider) widenDepender(pkg Package, v version.Version, deps []dependency) {
	if p.opts.NoDependerRuns || pkg.Kind != KindProject || len(deps) == 0 {
		return
	}
	p.depCalls[pkg]++
	n := p.depCalls[pkg]
	if n < 2 {
		return
	}
	asc := p.ascending(pkg.Name)
	i, found := slices.BinarySearchFunc(asc, v, func(a, b version.Version) int { return a.Compare(b) })
	if !found {
		return
	}
	budget := runBudgetStart << min(n-2, 8)

	type extent struct {
		lo, hi          int // inclusive member indexes
		leftOn, rightOn bool
	}
	ext := make([]extent, len(deps))
	live := 0
	for k, d := range deps {
		ext[k] = extent{lo: i, hi: i}
		if d.Package.Kind == KindProject || d.Package.Kind == KindPython {
			ext[k].leftOn, ext[k].rightOn = true, true
			live++
		}
	}
	has := func(w version.Version, d dependency) bool {
		nb := p.neighbourDeps(pkg, w, &budget)
		if !nb.ok {
			return false
		}
		return slices.ContainsFunc(nb.deps, func(x dependency) bool {
			return x.Package == d.Package && x.Allowed.Equal(d.Allowed)
		})
	}
	for step := 1; step <= len(asc) && budget > 0; step++ {
		any := false
		for k, d := range deps {
			e := &ext[k]
			if e.leftOn {
				if j := i - step; j >= 0 && has(asc[j], d) {
					e.lo, any = j, true
				} else {
					e.leftOn = false
				}
			}
			if e.rightOn {
				if j := i + step; j < len(asc) && has(asc[j], d) {
					e.hi, any = j, true
				} else {
					e.rightOn = false
				}
			}
		}
		if !any {
			break
		}
	}
	cur := pep440set.Exactly(v)
	for k := range deps {
		e := ext[k]
		if e.lo == e.hi {
			continue
		}
		var lo, hi *version.Version
		if e.lo > 0 {
			lo = &asc[e.lo]
		}
		if e.hi < len(asc)-1 {
			hi = &asc[e.hi+1]
		}
		run := pep440set.Run(lo, hi)
		// The solver refuses a Depender that leaves out the version it asked
		// about, which Run can do when a local variant of v is the next version.
		if cur.IsSubsetOf(run) {
			deps[k].Depender = run
		}
	}
}

// ascending is pkg's full published list in version order, memoized.
func (p *Provider) ascending(name index.PackageName) []version.Version {
	if a, ok := p.asc[name]; ok {
		return a
	}
	a := slices.Clone(p.all[name])
	slices.SortStableFunc(a, func(x, y version.Version) int { return x.Compare(y) })
	p.asc[name] = a
	return a
}

// neighbourDeps reads w's dependencies without recording anything. A fresh read
// costs one unit of *budget; when none is left the answer is "cannot join".
func (p *Provider) neighbourDeps(pkg Package, w version.Version, budget *int) neighbour {
	key := pkg.String() + "\x00" + w.String()
	if nb, ok := p.nbrs[key]; ok {
		return nb
	}
	if *budget <= 0 {
		return neighbour{}
	}
	*budget--
	var nb neighbour
	if meta, reason, _, err := p.metadata(pkg, w); err == nil && reason == "" {
		if _, rejected := p.rejectedForWheelTags(meta); !rejected {
			if deps, _, reason, err := p.buildDeps(pkg, w, meta); err == nil && reason == "" {
				nb = neighbour{deps: deps, ok: true}
			}
		}
	}
	p.nbrs[key] = nb
	return nb
}
