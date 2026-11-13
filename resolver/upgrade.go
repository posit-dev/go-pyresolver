// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// level is a version's position in the coarse order the pass works in:
// the epoch, then the major (or the minor for a 0.x package).
type level struct{ epoch, n int64 }

func (a level) less(b level) bool {
	return a.epoch < b.epoch || (a.epoch == b.epoch && a.n < b.n)
}

// levelsBetween is how many levels lo is below hi: the epoch gap if the epochs
// differ, else the gap in n.
func levelsBetween(hi, lo level) int {
	if !lo.less(hi) {
		return 0
	}
	if hi.epoch != lo.epoch {
		return int(min(hi.epoch-lo.epoch, math.MaxInt32))
	}
	return int(min(hi.n-lo.n, math.MaxInt32))
}

// usability answers, once per (project, version), whether the solver could
// pick a version in this environment. It asks the provider the solver uses.
type usability struct {
	p    *provider.Provider
	memo map[usableKey]bool
}

type usableKey struct {
	name index.PackageName
	ver  string
}

// ok is false for a version the solver would skip. An index error is false and
// is not remembered, so a cut ctx cannot poison later answers.
func (u *usability) ok(name index.PackageName, v version.Version) bool {
	k := usableKey{name, v.String()}
	if got, seen := u.memo[k]; seen {
		return got
	}
	got, err := u.p.Usable(name, v)
	if err != nil {
		return false
	}
	u.memo[k] = got
	return got
}

// upRoot is one root project and the versions its own specifiers admit.
type upRoot struct {
	name  index.PackageName
	cands []version.Version // ascending
	zero  bool              // newest candidate has major 0: the minor is the level
	u     *usability
}

// parseSeg reads one release segment. A segment too large for int64 counts as
// the largest level rather than 0, so it never reads as older.
func parseSeg(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return math.MaxInt32
	}
	return n
}

// release returns the epoch, major and minor of v.
func release(v version.Version) (epoch, maj, minor int64) {
	s := v.BaseVersion()
	if i := strings.IndexByte(s, '!'); i >= 0 {
		epoch = parseSeg(s[:i])
		s = s[i+1:]
	}
	parts := strings.Split(s, ".")
	maj = parseSeg(parts[0])
	if len(parts) > 1 {
		minor = parseSeg(parts[1])
	}
	return epoch, maj, minor
}

func (r *upRoot) level(v version.Version) level {
	epoch, maj, minor := release(v)
	if r.zero {
		if maj > 0 {
			return level{epoch, 1 << 40}
		}
		return level{epoch, minor}
	}
	return level{epoch, maj}
}

// top is the newest version the solver could pick, or pin if none is newer.
func (r *upRoot) top(pin version.Version) version.Version {
	for i := len(r.cands) - 1; i >= 0 && r.cands[i].GreaterThan(pin); i-- {
		if r.u.ok(r.name, r.cands[i]) {
			return r.cands[i]
		}
	}
	return pin
}

// behind returns how many levels and usable versions pin is below the newest
// usable version.
func (r *upRoot) behind(pin version.Version) (levels, versions int) {
	top := r.top(pin)
	if !pin.LessThan(top) {
		return 0, 0
	}
	for i := len(r.cands) - 1; i >= 0 && r.cands[i].GreaterThan(pin); i-- {
		if r.u.ok(r.name, r.cands[i]) {
			versions++
		}
	}
	return levelsBetween(r.level(top), r.level(pin)), versions
}

// bounds returns, for each level above pin's that has a usable version, the
// oldest usable version of that level, ascending.
func (r *upRoot) bounds(pin version.Version) []version.Version {
	var out []version.Version
	pinLv := r.level(pin)
	for i := 0; i < len(r.cands); {
		lv := r.level(r.cands[i])
		j := i
		for j < len(r.cands) && r.level(r.cands[j]) == lv {
			j++
		}
		if pinLv.less(lv) {
			for _, c := range r.cands[i:j] {
				if c.GreaterThan(pin) && r.u.ok(r.name, c) {
					out = append(out, c)
					break
				}
			}
		}
		i = j
	}
	return out
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
	u := &usability{
		p:    provider.New(ctx, idx, providerOptions(reqs, opts)),
		memo: map[usableKey]bool{},
	}
	roots := upgradeRootSet(ctx, reqs, idx, opts, base, u)
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
	// answer is valid and strictly better. A failed solve spends budget too.
	attempt := func(r *upRoot, bound version.Version) *Resolution {
		// Public: a local label is not allowed in a >= specifier.
		floor, err := requirement.Parse(string(r.name) + ">=" + bound.Public())
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
		// The untried root furthest behind (at least one level); the first by
		// name on a tie. A root behind only in versions is not tried.
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

		bounds := pick.bounds(best.Pinned[pick.name])
		if len(bounds) == 0 {
			continue
		}
		// levelIdx is the index of the highest bound at or below v's level.
		levelIdx := func(v version.Version) int {
			at := -1
			for i, b := range bounds {
				if !pick.level(v).less(pick.level(b)) {
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

		// Cheapest test first: any level up at all. Then the top level, then
		// bisect. The bisect assumes that if a level fails, every higher one
		// does too. That can miss an upgrade, which is acceptable.
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
	u *usability,
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
		r := &upRoot{name: n, u: u}
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
		_, maj, _ := release(r.cands[len(r.cands)-1])
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
