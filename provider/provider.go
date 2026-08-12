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
}

// New returns a Provider for one resolution.
//
// ctx bounds the whole resolve: every index call the solver provokes is made
// with it. See Provider.ctx for why it is stored rather than passed.
func New(ctx context.Context, idx index.MetadataIndex, opts Options) *Provider {
	if opts.RootVersion.String() == "" {
		opts.RootVersion = version.MustParse("0")
	}
	return &Provider{ctx: ctx, index: idx, opts: opts}
}

// Candidates implements solver.Provider.
//
// # count must be 0 exactly when nothing satisfies
//
// go-pubgrub treats a count of zero as "no version of this package lies within
// this range" and derives an incompatibility from it. So a version that
// genuinely cannot be used must be left out of the count, and a version that is
// merely undesirable must not be. That asymmetry is why admission
// (candidate.PrereleaseSet) and ranking (candidate.Policy) are separate types
// rather than one predicate.
//
// # best is filtered before it is ranked, not after
//
// The solver rejects a decision outside the accumulated term rather than
// trusting it, because such a decision corrupts the partial solution in a way
// that no later error points back to. Intersecting with allowed first is what
// guarantees best lies inside it; reordering those two steps reintroduces the
// bug.
func (p *Provider) Candidates(pkg Package, allowed pep440set.Set) (pep440set.Set, int, error) {
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
			return pep440set.Empty(), 0, nil
		}
		// Anything else -- a transport failure, a corrupt snapshot -- is NOT
		// "no such version". Reporting it as count 0 would let the resolver
		// quietly settle on an older version, or blame the user's constraints
		// for an outage.
		return pep440set.Empty(), 0, fmt.Errorf("provider: versions of %s: %w", pkg, err)
	}

	admissible := make([]version.Version, 0, len(all))
	for _, v := range all {
		if !allowed.Contains(v) {
			continue
		}
		if !p.opts.Prereleases.Admits(pkg.Name, v) {
			continue
		}
		ok, err := p.usable(pkg, v)
		if err != nil {
			return pep440set.Empty(), 0, err
		}
		if !ok {
			continue
		}
		admissible = append(admissible, v)
	}
	if len(admissible) == 0 {
		return pep440set.Empty(), 0, nil
	}

	ranked := candidate.Rank(pkg.Name, admissible, p.opts.Policy)
	return pep440set.Exactly(ranked[0]), len(ranked), nil
}

// singleVersion answers for a package with exactly one version and no index
// behind it: the root and the interpreter.
func singleVersion(v version.Version, allowed pep440set.Set) (pep440set.Set, int, error) {
	if !allowed.Contains(v) {
		return pep440set.Empty(), 0, nil
	}
	return pep440set.Exactly(v), 1, nil
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
		return false, err
	}
	return reason == "", nil
}
