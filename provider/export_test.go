// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

// This file exposes internals to the external provider_test package. It is a test
// file, so nothing here is part of the module's API.

import (
	"errors"
	"sort"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/version"
)

// rankBySortRef is what candidate.Rank did before the monotonicity fast path:
// always sort.SliceStable, never inspect the input's shape.
//
// ⚠️ ExactCandidates must not call candidate.Rank, or the differential stops
// being one. Rank is now part of the code under test twice over — the fast path
// AND the ranked-list memo depend on it — so a reference that called it would
// share the very thing the comparison exists to check. This is a verbatim copy of
// the pre-fast-path implementation, using the SAME Policy.Less, so the only
// difference between the two sides is the mechanism.
func rankBySortRef(pkg index.PackageName, versions []version.Version, p candidate.Policy) []version.Version {
	if p == nil {
		p = candidate.Newest{}
	}
	out := make([]version.Version, len(versions))
	copy(out, versions)
	sort.SliceStable(out, func(i, j int) bool {
		return p.Less(pkg, out[i], out[j])
	})
	return out
}

// ExactCandidates is what Candidates did before the found/rank split: test EVERY
// in-range version for usability, then rank the survivors and report the exact
// count of them.
//
// It exists to be differentiated against, never to be selected at runtime, and it
// lives in this package for one specific reason: it calls the same p.usable the
// real path calls. The old cost note warned that "computing usability a second,
// cheaper way is how the two drift apart", and a reference implementation with its
// own notion of usable would be testing two guesses against each other rather than
// testing the change under review.
//
// It reads p.index directly and ranks with rankBySortRef, so it shares neither the
// ranked-list memo nor the fast path with the implementation it is checking.
func (p *Provider) ExactCandidates(pkg Package, allowed pep440set.Set) (pep440set.Set, bool, int, error) {
	switch pkg.Kind {
	case KindRoot, KindPython:
		// ⚠️ Delegating to the code under test, which is a free pass, and it is
		// bounded rather than justified: these two kinds have no index behind them
		// and never reach the ranking, the memo or the fast path, so there is
		// genuinely nothing here for a differential to compare. singleVersion is
		// the whole implementation for both.
		//
		// It is inert today because no differential feeds these kinds. If one ever
		// does, this arm must grow a real reference instead — a comparison that
		// calls the implementation to produce the expected value proves nothing,
		// and that failure mode is silent.
		return p.Candidates(pkg, allowed)
	}

	all, err := p.index.Versions(p.ctx, pkg.Name)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			return pep440set.Empty(), false, 0, nil
		}
		return pep440set.Empty(), false, 0, err
	}

	admissible := make([]version.Version, 0, len(all))
	for _, v := range all {
		if !allowed.Contains(v) || !p.opts.Prereleases.Admits(pkg.Name, v) {
			continue
		}
		ok, err := p.usable(pkg, v)
		if err != nil {
			return pep440set.Empty(), false, 0, err
		}
		if ok {
			admissible = append(admissible, v)
		}
	}
	if len(admissible) == 0 {
		return pep440set.Empty(), false, 0, nil
	}

	ranked := rankBySortRef(pkg.Name, admissible, p.opts.Policy)
	return pep440set.Exactly(ranked[0]), true, len(ranked), nil
}

// InRangeRanked is the ranked in-range version list Candidates walks, before any
// usability test. Exposed so the differential can tell whether the walk actually
// had to SKIP anything to reach best — which is the only case where the two
// implementations could have disagreed.
func (p *Provider) InRangeRanked(pkg Package, allowed pep440set.Set) ([]version.Version, error) {
	all, err := p.index.Versions(p.ctx, pkg.Name)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			return nil, nil
		}
		return nil, err
	}

	inRange := make([]version.Version, 0, len(all))
	for _, v := range all {
		if !allowed.Contains(v) || !p.opts.Prereleases.Admits(pkg.Name, v) {
			continue
		}
		inRange = append(inRange, v)
	}
	return rankBySortRef(pkg.Name, inRange, p.opts.Policy), nil
}
