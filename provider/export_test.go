// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

// This file exposes internals to the external provider_test package. It is a test
// file, so nothing here is part of the module's API.

import (
	"errors"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/version"
)

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
func (p *Provider) ExactCandidates(pkg Package, allowed pep440set.Set) (pep440set.Set, bool, int, error) {
	switch pkg.Kind {
	case KindRoot, KindPython:
		// No index behind these, so there is nothing to enumerate differently.
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

	ranked := candidate.Rank(pkg.Name, admissible, p.opts.Policy)
	return pep440set.Exactly(ranked[0]), true, len(ranked), nil
}
