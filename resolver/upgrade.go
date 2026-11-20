// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

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

// upRoot is one root project and its published versions.
type upRoot struct {
	name  index.PackageName
	cands []version.Version // every published version, ascending
	zero  bool              // newest candidate has major 0: the minor is the level
	u     *usability

	// reqs are the root's own requirements, and pre is whether its pre-releases
	// count: only if the base answer pinned one. admit remembers per index of
	// cands whether they admit it (1) or not (2). It is filled on demand: the
	// specifier test is costly, and only versions above a pin are ever asked
	// about.
	reqs  []requirement.Requirement
	pre   bool
	admit []int8
}

// onSpecTest, when set by a test, is called before each root specifier test.
var onSpecTest func()

// onCandidate, when set by a test, is called with each answer a re-solve
// found and whether the pass kept it.
var onCandidate func(got *Resolution, kept bool)

// admitted reports whether the root's own specifiers admit cands[i].
func (r *upRoot) admitted(i int) bool {
	if r.admit[i] == 0 {
		v := r.cands[i]
		r.admit[i] = 2
		if onSpecTest != nil {
			onSpecTest()
		}
		if (r.pre || !v.IsPreRelease()) && admitsAll(r.reqs, v) {
			r.admit[i] = 1
		}
	}
	return r.admit[i] == 1
}

// can reports whether cands[i] is admitted and could be picked by the solver.
func (r *upRoot) can(i int) bool {
	return r.admitted(i) && r.u.ok(r.name, r.cands[i])
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
		if r.can(i) {
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
		if r.can(i) {
			versions++
		}
	}
	return levelsBetween(r.level(top), r.level(pin)), versions
}

// levelsBehind is the first result of behind. It needs only the newest usable
// version, so it does not test every version above pin.
func (r *upRoot) levelsBehind(pin version.Version) int {
	top := r.top(pin)
	if !pin.LessThan(top) {
		return 0
	}
	return levelsBetween(r.level(top), r.level(pin))
}

// bounds returns, for each level above pin's that has a usable version, the
// oldest usable version of that level, ascending.
func (r *upRoot) bounds(pin version.Version) []version.Version {
	var out []version.Version
	pinLv := r.level(pin)
	// Only a version above pin can be a bound; they are the tail of cands.
	i := len(r.cands)
	for i > 0 && r.cands[i-1].GreaterThan(pin) {
		i--
	}
	// A level is a run of admitted versions; found is whether lv has its bound.
	var lv level
	inRun, found := false, false
	for ; i < len(r.cands); i++ {
		if !r.admitted(i) {
			continue
		}
		if l := r.level(r.cands[i]); !inRun || l != lv {
			lv, inRun, found = l, true, false
		}
		if found || !pinLv.less(lv) {
			continue
		}
		if r.u.ok(r.name, r.cands[i]) {
			out = append(out, r.cands[i])
			found = true
		}
	}
	return out
}

// upgradeTimeLimit is how long the pass may run before it stops starting
// re-solves, from the options and the first solve's duration. ok is false when
// neither bound is set.
func upgradeTimeLimit(opts Options, baseDur time.Duration) (limit time.Duration, ok bool) {
	if opts.UpgradeMaxTime > 0 {
		limit, ok = opts.UpgradeMaxTime, true
	}
	if opts.UpgradeTimeFactor > 0 {
		// A huge factor is no bound; it must not overflow the Duration.
		if f := float64(baseDur) * opts.UpgradeTimeFactor; f < math.MaxInt64 {
			if d := time.Duration(f); !ok || d < limit {
				limit, ok = d, true
			}
		}
	}
	return limit, ok
}

// upgradeStepLimit is how many solver steps the pass's re-solves may take in
// all, from the options and the first solve's steps. ok is false when neither
// bound is set.
func upgradeStepLimit(opts Options, baseSteps int) (limit int, ok bool) {
	if opts.UpgradeMaxSteps > 0 {
		limit, ok = opts.UpgradeMaxSteps, true
	}
	if opts.UpgradeStepFactor > 0 {
		// A huge factor is no bound; it must not overflow the int.
		if f := float64(baseSteps) * opts.UpgradeStepFactor; f < math.MaxInt {
			if n := int(f); !ok || n < limit {
				limit, ok = n, true
			}
		}
	}
	return limit, ok
}

// upgradeRoots is the pass behind Options.UpgradeRoots. base is a valid answer
// to reqs, and baseDur and baseSteps are what it took; the result is base's
// answer or a valid one that scores strictly better, with an UpgradeReport.
func upgradeRoots(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
	base *Resolution,
	baseDur time.Duration,
	baseSteps int,
) *Resolution {
	best, byTime := upgradePass(ctx, reqs, idx, opts, base, baseDur, baseSteps)
	if best == base {
		// First must not alias the answer the caller gets.
		best = cloneResolution(base)
	}
	// ctx errors are sticky, so any way a ctx end reached the pass shows here.
	best.Upgrade = &UpgradeReport{First: base, StoppedByTime: byTime || ctx.Err() != nil}
	return best
}

// upgradePass runs the pass. byTime is whether a time bound ended it.
func upgradePass(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
	base *Resolution,
	baseDur time.Duration,
	baseSteps int,
) (_ *Resolution, byTime bool) {
	passStart := now()
	limit, timed := upgradeTimeLimit(opts, baseDur)
	stepLimit, stepped := upgradeStepLimit(opts, baseSteps)
	spent := 0 // solver steps the re-solves have used
	// cut is set once a re-solve is stopped by the time bound; the pass then
	// ends. A re-solve stopped by the step bound leaves spent at stepLimit.
	cut := false
	clockUp := false // the time bound was reached between re-solves
	timeUp := func() bool {
		if cut || (stepped && spent >= stepLimit) {
			return true
		}
		clockUp = timed && now().Sub(passStart) >= limit
		return clockUp
	}
	u := &usability{
		p:    provider.New(ctx, idx, providerOptions(reqs, opts)),
		memo: map[usableKey]bool{},
	}
	roots := upgradeRootSet(ctx, reqs, idx, opts, base, u)
	if len(roots) == 0 {
		return base, false
	}
	best := base
	// best is scored only once a re-solve has an answer to compare it with:
	// scoring tests every version above each root's pin. After a ctx cancel or
	// an index error mid-pass, scoring may differ; the answer stays valid.
	var bestScore score
	scored := false
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
		// Callers check timeUp first, so this cannot fire; it keeps a zero
		// meter limit, which means no limit, from ever reaching resolveOnce.
		if stepped && spent >= stepLimit {
			return nil
		}
		budget--
		all := append(slices.Clone(reqs), floor)
		solveCtx := ctx
		if timed {
			// A re-solve may run only for the time the pass has left.
			c, cancel := context.WithTimeout(ctx, limit-now().Sub(passStart))
			defer cancel()
			solveCtx = c
		}
		steps := &stepMeter{}
		if stepped {
			steps.limit = stepLimit - spent
		}
		got, err := resolveOnce(solveCtx, all, idx, inner, steps)
		spent += steps.used
		if err != nil {
			// Stopped by the time bound, not the caller: no gain, and the pass ends.
			if ctx.Err() == nil && solveCtx.Err() != nil {
				cut = true
			}
			return nil
		}
		if !scored {
			bestScore = objective(roots, best)
			scored = true
		}
		kept := objective(roots, got).less(bestScore)
		if onCandidate != nil {
			onCandidate(got, kept)
		}
		if !kept {
			return nil
		}
		return got
	}

	for budget > 0 && ctx.Err() == nil && !timeUp() {
		// The untried root furthest behind (at least one level); the first by
		// name on a tie. A root behind only in versions is not tried.
		var pick *upRoot
		pickLv := 0
		for _, r := range roots {
			if done[r.name] {
				continue
			}
			if lv := r.levelsBehind(best.Pinned[r.name]); lv > pickLv {
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
			bestScore = objective(roots, best)
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
		for hi-lo > 1 && budget > 0 && ctx.Err() == nil && !timeUp() {
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
	return best, cut || clockUp
}

// cloneResolution copies r's maps and slices, so the copy shares none with r.
func cloneResolution(r *Resolution) *Resolution {
	c := *r
	c.Pinned = maps.Clone(r.Pinned)
	c.Order = slices.Clone(r.Order)
	if r.Extras != nil {
		c.Extras = make(map[index.PackageName][]string, len(r.Extras))
		for k, v := range r.Extras {
			c.Extras[k] = slices.Clone(v)
		}
	}
	c.Unusable = slices.Clone(r.Unusable)
	c.MissingExtras = slices.Clone(r.MissingExtras)
	c.YankedPins = slices.Clone(r.YankedPins)
	return &c
}

// upgradeRootSet lists, sorted by name, each root project that base pinned,
// with its published versions. Roots that are URL requirements or whose marker
// is false here are left out. A root's pre-releases count only if base pinned
// one. See upRoot.admitted for the specifier test.
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
		if len(vs) == 0 {
			continue
		}
		cmp := func(a, b version.Version) int { return a.Compare(b) }
		if !slices.IsSortedFunc(vs, cmp) {
			// A copy: the list belongs to the index.
			vs = slices.Clone(vs)
			slices.SortStableFunc(vs, cmp)
		}
		r := &upRoot{
			name: n, u: u, cands: vs,
			reqs: by[n], pre: pin.IsPreRelease(), admit: make([]int8, len(vs)),
		}
		// 0.x is decided by the newest version the solver could pick.
		_, maj, _ := release(r.top(pin))
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

// score is how far an answer's roots are from where the pass wants them.
// Lower is better; the fields compare in order.
type score struct {
	pre      int // roots moved from a final release to a pre-release
	levels   int // sum of levels behind
	versions int // sum of versions behind
}

func (a score) less(b score) bool {
	if a.pre != b.pre {
		return a.pre < b.pre
	}
	if a.levels != b.levels {
		return a.levels < b.levels
	}
	return a.versions < b.versions
}

// objective scores res over roots. A root the base answer pinned at a final
// release counts in pre when res pins a pre-release of it, older or newer.
func objective(roots []*upRoot, res *Resolution) score {
	var s score
	for _, r := range roots {
		pin, ok := res.Pinned[r.name]
		if !ok {
			continue
		}
		if !r.pre && pin.IsPreRelease() {
			s.pre++
		}
		l, v := r.behind(pin)
		s.levels += l
		s.versions += v
	}
	return s
}
