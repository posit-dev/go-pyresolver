// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// upRoot is one root project and the versions its own specifiers admit.
type upRoot struct {
	name  index.PackageName
	cands []version.Version // ascending
	zero  bool              // newest candidate has major 0: the minor is the level
}

// release returns the major and minor of v's release segment.
func release(v version.Version) (maj, minor int64) {
	s := v.BaseVersion()
	if i := strings.IndexByte(s, '!'); i >= 0 {
		s = s[i+1:]
	}
	parts := strings.Split(s, ".")
	maj, _ = strconv.ParseInt(parts[0], 10, 64)
	if len(parts) > 1 {
		minor, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	return maj, minor
}

func (r *upRoot) level(v version.Version) int64 {
	maj, minor := release(v)
	if r.zero {
		if maj > 0 {
			return 1 << 40
		}
		return minor
	}
	return maj
}

// behind returns how many levels and versions pin is below the newest candidate.
func (r *upRoot) behind(pin version.Version) (levels, versions int) {
	top := r.cands[len(r.cands)-1]
	if !pin.LessThan(top) {
		return 0, 0
	}
	for i := len(r.cands) - 1; i >= 0 && r.cands[i].GreaterThan(pin); i-- {
		versions++
	}
	return int(max(r.level(top)-r.level(pin), 0)), versions
}

// upgradeRoots is the pass behind Options.UpgradeRoots. base is a valid answer
// to reqs; the result is base or a valid answer that scores strictly better.
func upgradeRoots(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
	base *Resolution,
) *Resolution {
	roots := upgradeRootSet(ctx, reqs, idx, opts, base)
	if len(roots) == 0 {
		return base
	}
	best := base
	bestLv, bestVer := objective(roots, best)
	budget := opts.UpgradeRoots
	inner := opts
	inner.UpgradeRoots = 0
	done := make(map[index.PackageName]bool, len(roots))

	// attempt re-solves with r floored at bound; it returns nil unless the
	// answer is valid and strictly better.
	attempt := func(r *upRoot, bound version.Version) *Resolution {
		floor, err := requirement.Parse(string(r.name) + ">=" + bound.String())
		if err != nil {
			return nil
		}
		budget--
		all := append(slices.Clone(reqs), floor)
		got, err := resolveOnce(ctx, all, idx, inner)
		if err != nil {
			return nil
		}
		lv, ver := objective(roots, got)
		if lv > bestLv || (lv == bestLv && ver >= bestVer) {
			return nil
		}
		return got
	}

	for budget > 0 && ctx.Err() == nil {
		// The untried root furthest behind; the first by name on a tie.
		var pick *upRoot
		pickLv := 0
		for _, r := range roots {
			if done[r.name] {
				continue
			}
			if lv, _ := r.behind(best.Pinned[r.name]); lv > pickLv {
				pick, pickLv = r, lv
			}
		}
		if pick == nil {
			break
		}
		done[pick.name] = true
		pin := best.Pinned[pick.name]

		// bounds[i] is the oldest candidate of the i-th level above the pin's.
		var bounds []version.Version
		last := pick.level(pin)
		for _, c := range pick.cands {
			if lv := pick.level(c); lv > last && c.GreaterThan(pin) {
				bounds = append(bounds, c)
				last = lv
			}
		}
		if len(bounds) == 0 {
			continue
		}
		// levelIdx is the index of the highest bound at or below v's level.
		levelIdx := func(v version.Version) int {
			at := -1
			for i, b := range bounds {
				if pick.level(b) <= pick.level(v) {
					at = i
				}
			}
			return at
		}
		accept := func(got *Resolution) int {
			best = got
			bestLv, bestVer = objective(roots, best)
			return levelIdx(best.Pinned[pick.name])
		}

		// Cheapest test first: any level up at all. Then the top level, then bisect.
		got := attempt(pick, bounds[0])
		if got == nil {
			continue
		}
		lo := accept(got) // highest level index known to work
		hi := len(bounds) // lowest level index known to fail
		triedTop := false
		for hi-lo > 1 && budget > 0 && ctx.Err() == nil {
			mid := hi - 1
			if triedTop {
				mid = (lo + hi) / 2
			}
			triedTop = true
			if got := attempt(pick, bounds[mid]); got != nil {
				lo = max(accept(got), mid)
			} else {
				hi = mid
			}
		}
	}
	return best
}

// upgradeRootSet lists, sorted by name, each root project that base pinned,
// with the versions its own specifiers admit in this environment. Roots that
// are URL requirements or whose marker is false here are left out, and so are
// pre-releases unless base pinned one.
func upgradeRootSet(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
	base *Resolution,
) []*upRoot {
	by := map[index.PackageName][]requirement.Requirement{}
	var names []index.PackageName
	for _, q := range reqs {
		if q.URL != "" || !q.Marker.Evaluate(opts.Environment, nil) {
			continue
		}
		n := index.NewPackageName(q.Name)
		if _, ok := by[n]; !ok {
			names = append(names, n)
		}
		by[n] = append(by[n], q)
	}
	slices.Sort(names)
	var out []*upRoot
	for _, n := range names {
		pin, ok := base.Pinned[n]
		if !ok {
			continue
		}
		vs, err := idx.Versions(ctx, n)
		if err != nil {
			continue
		}
		r := &upRoot{name: n}
		for _, v := range vs {
			if v.IsPreRelease() && !pin.IsPreRelease() {
				continue
			}
			if !admitsAll(by[n], v) {
				continue
			}
			r.cands = append(r.cands, v)
		}
		if len(r.cands) == 0 {
			continue
		}
		slices.SortFunc(r.cands, func(a, b version.Version) int { return a.Compare(b) })
		maj, _ := release(r.cands[len(r.cands)-1])
		r.zero = maj == 0
		out = append(out, r)
	}
	return out
}

func admitsAll(reqs []requirement.Requirement, v version.Version) bool {
	for _, q := range reqs {
		if q.Specifiers.Len() > 0 && !q.Specifiers.ContainsVersion(v) {
			return false
		}
	}
	return true
}

// objective is the sum over roots of levels behind, then of versions behind.
func objective(roots []*upRoot, res *Resolution) (levels, versions int) {
	for _, r := range roots {
		pin, ok := res.Pinned[r.name]
		if !ok {
			continue
		}
		l, v := r.behind(pin)
		levels += l
		versions += v
	}
	return levels, versions
}
