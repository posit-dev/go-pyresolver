// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// Options configures one resolution.
type Options struct {
	// Environment is the single concrete marker environment every PEP 508
	// marker is evaluated in. RFD 0001 defers universal (environment-
	// independent) resolution, so this is one target, not a set.
	//
	// Build it with marker.EnvironmentFromTarget rather than as a struct
	// literal: a literal zero-fills the fields it omits, which turns
	// python_version into "" and silently flips the answer of any marker that
	// mentions it.
	Environment marker.Environment

	// PythonVersion is the interpreter the Python() package is pinned to.
	//
	// Carried separately from Environment because the interpreter-as-package
	// model needs a parsed version.Version and Environment holds marker
	// variables as strings. It must agree with Environment's
	// python_full_version; checking that is the caller's job (see the resolver
	// package), since Options alone cannot tell a deliberate override from a
	// mistake.
	PythonVersion version.Version

	// Policy orders the admissible versions of a package. Nil means
	// candidate.Newest.
	//
	// It ranks and never filters -- see candidate.Rank. Candidates walks the
	// ranking and stops at the first usable version, so a version a Policy
	// dropped would be UNREACHABLE rather than merely last, and a package whose
	// only usable version the Policy disliked would be reported as having
	// nothing available at all.
	Policy candidate.Policy

	// Prereleases names the packages whose pre-release versions may be
	// offered. Build it with candidate.EnabledPrereleases, ONCE, before
	// solving: the solver caches derivations on the assumption that the facts
	// behind them do not move.
	Prereleases candidate.PrereleaseSet

	// Requirements are the caller's own requirements: what the resolution is
	// being asked for. They become the root package's dependencies.
	//
	// Names may be passed exactly as parsed; they are canonicalized here.
	Requirements []requirement.Requirement

	// RootVersion is the synthetic version of the root package. The zero value
	// means version 0.
	//
	// Exposed only because a caller embedding this in a larger graph may want
	// the root to carry a meaningful version; nothing in resolution depends on
	// what it is.
	RootVersion version.Version
}

// Provider implements solver.Provider[Package, pep440set.Set].
//
// One Provider serves one resolution. It is not safe for concurrent use, which
// matches go-pubgrub's Solver.
type Provider struct {
	// ctx is stored, which is normally a smell, and is forced here rather than
	// chosen: solver.Provider's methods take no context.Context, and
	// index.MetadataIndex requires one on every call. The alternatives are
	// worse -- a background context would make a resolution uncancellable, and
	// widening the solver interface would push a Python-specific concern into
	// a deliberately language-agnostic package. A Provider is created per
	// resolve and never outlives it, so the usual hazard (a context captured in
	// a long-lived struct outliving the request it belongs to) does not arise.
	ctx context.Context

	index index.MetadataIndex
	opts  Options

	// unusable holds the reasons versions could not be used, in first-seen
	// order, and recorded is the dedupe key set behind it. See Provider.record.
	unusable []Unusable
	recorded map[string]bool

	// ranked memoizes candidate.Rank over a package's FULL version list, so the
	// sort is paid once per package per resolution rather than once per
	// Candidates call. See rankedVersions.
	ranked map[index.PackageName][]version.Version
}

// New returns a Provider for one resolution.
//
// ctx bounds the whole resolve: every index call the solver provokes is made
// with it. See Provider.ctx for why it is stored rather than passed.
func New(ctx context.Context, idx index.MetadataIndex, opts Options) *Provider {
	if opts.RootVersion.String() == "" {
		opts.RootVersion = version.MustParse("0")
	}
	return &Provider{
		ctx:      ctx,
		index:    idx,
		opts:     opts,
		recorded: make(map[string]bool),
		ranked:   make(map[index.PackageName][]version.Version),
	}
}

// Candidates implements solver.Provider.
//
// # found must be true exactly when something usable is in range
//
// go-pubgrub treats found == false as "no version of this package lies within
// this range" and derives an incompatibility from it. So a version that
// genuinely cannot be used must not make found true, and a version that is
// merely undesirable must. That asymmetry is why admission
// (candidate.PrereleaseSet) and ranking (candidate.Policy) are separate types
// rather than one predicate.
//
// Because it is existence and not cardinality, this stops TESTING at the first
// usable version. That is what keeps the METADATA cost proportional to the
// packages actually decided rather than to every version in range: certifi has 65
// published versions and no dependencies at all, and an exact count read every one
// of them on each of the two rounds the solver asked about it, 131 metadata reads
// in all. Answering existence instead takes 3.
//
// ⚠️ The WALK is a different matter and is not short-circuited: rank is the
// in-range count, so every version is still tested against allowed even after best
// is settled. That is O(all versions) per call by construction, and it is why
// pep440set.Set.Contains is 28.6% of this call in the current profile. An earlier
// draft of this paragraph said the cost was proportional to packages decided
// without that qualification, which was true only of the metadata reads.
//
// # rank is the in-range count, taken BEFORE usability is tested
//
// rank only orders which package the solver works on next. go-pubgrub documents it
// as a hint it only ever COMPARES, and is explicit that nothing requires it to be a
// count, an upper bound, or non-negative.
//
// ⚠️ Being an upper bound is therefore THIS provider's own choice, not an upstream
// obligation, and the difference matters when reading the differential: the
// under-count check there enforces a rule we impose on ourselves. We choose it
// because under-counting is the one direction with a cost — it would make the
// solver prefer this package over one that genuinely has fewer candidates, and the
// heuristic exists to do the opposite. The in-range count before usability
// filtering satisfies it for free, because that list has to be built anyway.
//
// Do not be tempted to make it exact. An exact rank means testing every version
// in range, which is the entire cost this design exists to avoid, in service of a
// heuristic that go-pubgrub and both prose sources agree is not
// correctness-bearing.
//
// ⚠️ Nor should it be a constant. Reporting a flat 1 is legal and disables the
// heuristic, and that is not free: measured against a production snapshot it
// silently changed which of several legal resolutions was found. The in-range
// count reproduces the exact-count search order on everything measured.
//
// # ⚠️ Rank BEFORE the usability walk, not after
//
// The version this hands back must be the same one an exact implementation would
// have chosen -- the highest-ranked USABLE version -- and the order of these two
// steps is what makes that true. candidate.Rank is a stable sort over a pairwise
// Less, so it orders a subset consistently with the superset it came from;
// therefore the first usable version in ranked order is the same version as
// Rank(usable-only)[0]. Filter first and rank the survivors and you get the same
// answer at full enumeration cost; rank first and stop early and you get it
// cheaply. Test usability first and rank after, and there is nothing left to stop
// early on.
//
// ⚠️ The FULL version list is what gets ranked, not the in-range one, which
// applies that same identity one level up: the in-range members of Rank(all), in
// that order, are Rank(in-range). Ranking the superset is what makes the order
// memoizable across calls at all -- the in-range list is a function of the
// caller's allowed set and would key a memo on caller-supplied input. See
// rankedVersions.
//
// Discarding the versions outside allowed while walking that order is separately
// what guarantees best lies inside allowed, which the solver refuses to trust.
//
// # Cost
//
// Ranking, not metadata, is what this call costs. Measured warm against a
// production snapshot with the found/rank walk in place, candidate.Rank was 83%
// of Candidates on the benchmark's app-set entry and 85% on wide-versions, while
// the usability walk the found/rank change had just optimized was 1.7% and 0.6%.
// The solver re-asks about a package on every round it reconsiders it -- app-set
// provokes 87 Versions() calls over 18 distinct packages -- and each of those
// re-sorted from scratch.
//
// Two things fixed that, and they compose: the ranked list is memoized per
// package for the life of this Provider, and candidate.Rank detects an input
// already in (or exactly counter to) Policy order in one linear pass instead of
// sorting it. The second matters because index.RSFIndex returns versions
// ASCENDING and the default Newest policy wants them descending, which is
// sort.SliceStable's worst case. Together, warm resolution of the benchmark
// corpus fell 1.4x to 11.8x. See resolver/bench_test.go, which carries the
// per-entry table and is the single place those numbers are maintained.
func (p *Provider) Candidates(pkg Package, allowed pep440set.Set) (pep440set.Set, bool, int, error) {
	switch pkg.Kind {
	case KindRoot:
		return singleVersion(p.opts.RootVersion, allowed)
	case KindPython:
		return singleVersion(p.opts.PythonVersion, allowed)
	}

	ranked, err := p.rankedVersions(pkg)
	if err != nil {
		return pep440set.Empty(), false, 0, err
	}

	// ONE walk of the pre-ranked list does both jobs: it counts the in-range
	// versions (rank) and finds the first usable one (best). No in-range slice is
	// materialized, and nothing is ordered HERE -- rankedVersions did that once
	// for this package, on whichever call reached it first.
	//
	// ⚠️ An index failure on a LOWER-ranked version is no longer always seen.
	//
	// usable returns an error rather than false when the index cannot answer, and
	// that error aborts the resolve on purpose -- an outage must not be reported as
	// "no such version". That is unchanged for every version this walk reaches. But
	// the walk stops testing at the first usable version, so a broken older release
	// is not examined unless backtracking narrows the range down to it.
	//
	// So an index that is broken for one old version now resolves successfully where
	// it used to abort, and whether the failure surfaces became path-dependent. That
	// is arguably the better behaviour -- an unreadable release nobody would have
	// chosen is a poor reason to fail a resolve -- but it IS a change, and it is not
	// a weakening of the rule the error path exists for: nothing is being reported as
	// unavailable on the strength of an outage. It is simply not being looked at.
	var (
		best    version.Version
		found   bool
		inRange int
	)
	for _, v := range ranked {
		// The cheap half of admission: range and pre-release policy, no metadata
		// and no I/O. What passes both is what rank counts.
		if !allowed.Contains(v) {
			continue
		}
		if !p.opts.Prereleases.Admits(pkg.Name, v) {
			continue
		}
		inRange++
		if found {
			// best is settled; the rest of the walk only counts, which costs no
			// metadata read.
			continue
		}
		ok, err := p.usable(pkg, v)
		if err != nil {
			return pep440set.Empty(), false, 0, err
		}
		if ok {
			best, found = v, true
		}
	}
	if !found {
		// Every in-range version was rejected, so nothing is available. This is the
		// one case that still costs a full usability walk -- and it is unavoidable,
		// because "nothing here is usable" cannot be established without checking
		// everything.
		return pep440set.Empty(), false, 0, nil
	}
	return pep440set.Exactly(best), true, inRange, nil
}

// rankedVersions returns pkg's full published version list in Policy order,
// memoized for the life of this Provider.
//
// # Why the memo is here rather than in the index
//
// The solver asks about the same package on every round it reconsiders it --
// app-set provokes 87 Versions() calls across 18 distinct packages, a reuse
// factor of 4.8 -- and every one of those calls used to re-sort. The distinct
// count is not an estimate: after this memo, `versions/op` on the benchmark IS
// the number of distinct names, because each one triggers exactly one call, and
// TestCandidatesAgreeAcrossRepeatedCallsWithDifferentRanges asserts that equality
// through a counting index rather than deriving it.
//
// Sorting is where the time went: measured warm against a production snapshot,
// candidate.Rank was 83% of Candidates on app-set and 85% on wide-versions,
// against 1.7% and 0.6% for the usability walk the found/rank change had just
// optimized. See resolver/bench_test.go.
//
// Ranking the FULL list rather than the in-range one is what makes it memoizable
// at all: the in-range list is a function of the caller's allowed set, and a memo
// keyed by that would be keyed on caller-supplied input and grow without bound.
// This one is keyed by package name and is bounded by the closure of a single
// resolution.
//
// # ⚠️ Why this memo is on the PROVIDER and not on the index
//
// ⚠️ HISTORICAL as of go-python-packaging v0.6.0. This used to read: a
// version.Version must not be shared between goroutines even for reads, because
// Version.Compare padded a release segment with append into spare capacity that a
// by-value copy still shared -- so a memo of parsed versions had to sit on a
// Provider, which serves one resolution and is documented as unsafe for
// concurrent use, and moving it onto a shared index would reintroduce the race in
// full.
//
// THAT IS NO LONGER TRUE. v0.6.0 pads into a fresh slice, index.RSFIndex now
// memoizes parsed versions on the strength of it, and this memo COULD move onto
// the index, where the ranked list would survive across resolutions rather than
// only within one. It is deliberately not part of the parsed-version memo's
// change, because a second effect would confound that one's measurement.
//
// candidate.Rank is the only work moving the memo up would save, and only ACROSS
// resolutions, since within one this memo already runs it once per package. On
// the tree that took the parsed-version memo, warm wide-versions against the
// production snapshot, rankedVersions is 27.5% of resolver.Resolve's cumulative
// cost and candidate.Rank inside it is 17.4% -- so that is the shape of the
// opportunity and 17.4% is its ceiling.
//
// ⚠️ Treat that as a pointer, not a promise, and RE-PROFILE before acting on it.
// An earlier draft of this comment quoted a residual profile naming a pep440set
// frame that c006d47 then deleted, so it advertised a cost that no longer existed
// in a function that no longer existed. A profile is a fact about one tree.
//
// ⚠️ Whoever takes it owns the retention question, and it is bigger here than for
// the parsed list: a ranked list is per package too, and an index is long-lived
// where a Provider is discarded.
//
// Until then the memo stays here, and being per-resolution it is bounded twice
// over: one entry per package the resolution reaches, and the whole Provider is
// discarded when the resolve ends.
//
// # ⚠️ Three things this narrows, none of them free
//
// Memoizing a call means the call stops happening, and three behaviours rode on
// it. None is a correctness defect; all three are changes, and an undocumented
// change is the one that surprises someone later.
//
//   - RETAINED MEMORY. This holds the parsed version list of every package the
//     closure reaches for the whole resolution, where those were previously
//     transient garbage -- so the obvious worry is that it trades allocation
//     churn for a higher high-water mark, and total allocation (B/op) cannot
//     answer that because it is cumulative rather than peak.
//
//     ⚠️ MEASURED, and it goes the OTHER way: peak heap FELL, by 3.0x to 7.3x.
//     app-set's peak-over-baseline went 53.9 MB to 7.4 MB and wide-versions'
//     56.2 MB to 18.6 MB, reproducible across three runs a side. An earlier
//     draft of this note asserted the trade-off as though it were established;
//     it was a prediction, and it was wrong.
//
//     The reason is that the churn it removes was never short-lived. The old
//     path allocated a fresh in-range slice AND a fresh Rank copy on every one
//     of app-set's 87 calls, and those pile up within a GC cycle; the memo holds
//     one list per package -- 18 of them -- and the in-range slice is gone
//     entirely. Fewer live bytes, not more. See resolver.TestPeakHeapDuringOneResolve,
//     which is a SAMPLED maximum and therefore a floor on the true peak.
//
//   - CANCELLATION. index.Versions checks ctx.Err(), so before this every
//     Candidates call had a cancellation checkpoint even when nothing was in
//     range. Now a memoized call for a package with no in-range versions reaches
//     no index method at all. Any call that gets as far as usable still checks,
//     and the solver's loop is bounded by MaxRounds, so a cancelled resolve still
//     terminates -- but it can now take longer to notice.
//
//   - A TRANSIENT Versions() FAILURE after a first success is unobservable for
//     that package. This is a sibling of the caveat on Candidates about index
//     failures on lower-ranked versions, and a different one: that caveat is
//     about versions never examined, this is about a package never re-read.
func (p *Provider) rankedVersions(pkg Package) ([]version.Version, error) {
	if r, ok := p.ranked[pkg.Name]; ok {
		return r, nil
	}

	all, err := p.index.Versions(p.ctx, pkg.Name)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			// Not an error: an unknown name is something the solver explains
			// through the derivation graph, and aborting the resolve over it
			// would replace a good report with a bad one. Memoized as empty so a
			// name the solver asks about repeatedly is looked up once.
			p.ranked[pkg.Name] = nil
			return nil, nil
		}
		// Anything else -- a transport failure, a corrupt snapshot -- is NOT
		// "no such version". Reporting it as unavailable would let the resolver
		// quietly settle on an older version, or blame the user's constraints
		// for an outage. NOT memoized: a failure is not an answer.
		return nil, fmt.Errorf("provider: versions of %s: %w", pkg, err)
	}

	// ⚠️ Ranking the superset and filtering after is the same identity the walk
	// already rested on, applied one level up. candidate.Rank is a stable sort over
	// a pairwise Less, so it orders a subset consistently with the superset it came
	// from; therefore the in-range members of Rank(all), in that order, are exactly
	// Rank(in-range). Composed with the existing argument -- the first usable
	// version of a ranked list is the same version as Rank(usable-only)[0] -- best
	// is unchanged. provider/differential_test.go checks that against the exact
	// reference rather than leaving it as an argument.
	r := candidate.Rank(pkg.Name, all, p.opts.Policy)
	p.ranked[pkg.Name] = r
	return r, nil
}

// singleVersion answers for a package with exactly one version and no index
// behind it: the root and the interpreter.
func singleVersion(v version.Version, allowed pep440set.Set) (pep440set.Set, bool, int, error) {
	if !allowed.Contains(v) {
		return pep440set.Empty(), false, 0, nil
	}
	return pep440set.Exactly(v), true, 1, nil
}

// usable reports whether pkg at v can be offered to the solver.
//
// It answers by doing exactly the work Dependencies would do and discarding the
// result. That is deliberate: every version this admits is one Dependencies
// must then succeed on, and computing usability a second, cheaper way is how
// the two drift apart -- leaving the solver holding a decision whose
// dependencies then fail, which surfaces as an aborted resolve rather than as
// the conflict it really is.
func (p *Provider) usable(pkg Package, v version.Version) (bool, error) {
	_, reason, err := p.projectDependencies(pkg, v)
	if err != nil {
		// An index that could not answer is NOT a version that cannot be used.
		// Recording it here would put an outage in the failure report as
		// though it were a fact about the package.
		return false, err
	}
	if reason != "" {
		p.record(pkg, v, reason, false)
		return false, nil
	}
	return true, nil
}
