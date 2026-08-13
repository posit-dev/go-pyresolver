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
	// It ranks and never filters -- see candidate.Rank. A version a Policy
	// dislikes is still counted, because a count of zero is what the solver
	// reads as "nothing here at all".
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
}

// New returns a Provider for one resolution.
//
// ctx bounds the whole resolve: every index call the solver provokes is made
// with it. See Provider.ctx for why it is stored rather than passed.
func New(ctx context.Context, idx index.MetadataIndex, opts Options) *Provider {
	if opts.RootVersion.String() == "" {
		opts.RootVersion = version.MustParse("0")
	}
	return &Provider{ctx: ctx, index: idx, opts: opts, recorded: make(map[string]bool)}
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
// Because it is existence and not cardinality, this stops at the FIRST usable
// version. That is what keeps the cost proportional to the packages actually
// decided rather than to every version ever published: certifi has 130 releases
// and no dependencies at all, and answering "is one of them usable" reads one.
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
// therefore the first usable version in ranked-in-range order is the same version
// as Rank(usable-only)[0]. Filter first and rank the survivors and you get the
// same answer at full enumeration cost; rank first and stop early and you get it
// cheaply. Test usability first and rank after, and there is nothing left to stop
// early on.
//
// Discarding the versions outside allowed BEFORE ranking is separately what
// guarantees best lies inside allowed, which the solver refuses to trust.
//
// # Cost
//
// Deciding usability reads a version's metadata, and the solver asks about the
// same package repeatedly as it backtracks, so index.RSFIndex memoizes by exactly
// (package, version). What dominates now is the work projectDependencies does
// with the parsed requirements -- evaluating markers and converting specifiers to
// version sets -- which is a pure function of (requirement, environment) and so is
// the next thing worth memoizing, keyed by (package, version, extra). See
// resolver/bench_test.go.
func (p *Provider) Candidates(pkg Package, allowed pep440set.Set) (pep440set.Set, bool, int, error) {
	switch pkg.Kind {
	case KindRoot:
		return singleVersion(p.opts.RootVersion, allowed)
	case KindPython:
		return singleVersion(p.opts.PythonVersion, allowed)
	}

	all, err := p.index.Versions(p.ctx, pkg.Name)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			// Not an error: an unknown name is something the solver explains
			// through the derivation graph, and aborting the resolve over it
			// would replace a good report with a bad one.
			return pep440set.Empty(), false, 0, nil
		}
		// Anything else -- a transport failure, a corrupt snapshot -- is NOT
		// "no such version". Reporting it as unavailable would let the resolver
		// quietly settle on an older version, or blame the user's constraints
		// for an outage.
		return pep440set.Empty(), false, 0, fmt.Errorf("provider: versions of %s: %w", pkg, err)
	}

	// The cheap half of admission: range and pre-release policy, no metadata and
	// no I/O. This list is both what gets ranked and what rank counts.
	inRange := make([]version.Version, 0, len(all))
	for _, v := range all {
		if !allowed.Contains(v) {
			continue
		}
		if !p.opts.Prereleases.Admits(pkg.Name, v) {
			continue
		}
		inRange = append(inRange, v)
	}

	// ⚠️ An index failure on a LOWER-ranked version is no longer always seen.
	//
	// usable returns an error rather than false when the index cannot answer, and
	// that error aborts the resolve on purpose -- an outage must not be reported as
	// "no such version". That is unchanged for every version this walk reaches. But
	// the walk stops at the first usable version, so a broken older release is not
	// examined unless backtracking narrows the range down to it.
	//
	// So an index that is broken for one old version now resolves successfully where
	// it used to abort, and whether the failure surfaces became path-dependent. That
	// is arguably the better behaviour -- an unreadable release nobody would have
	// chosen is a poor reason to fail a resolve -- but it IS a change, and it is not
	// a weakening of the rule the error path exists for: nothing is being reported as
	// unavailable on the strength of an outage. It is simply not being looked at.
	for _, v := range candidate.Rank(pkg.Name, inRange, p.opts.Policy) {
		ok, err := p.usable(pkg, v)
		if err != nil {
			return pep440set.Empty(), false, 0, err
		}
		if ok {
			return pep440set.Exactly(v), true, len(inRange), nil
		}
	}

	// Every in-range version was rejected, so nothing is available. This is the
	// one case that still costs a full walk -- and it is unavoidable, because
	// "nothing here is usable" cannot be established without checking everything.
	// The old behaviour paid this on every package; this pays it only when the
	// answer really is "nothing".
	return pep440set.Empty(), false, 0, nil
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
