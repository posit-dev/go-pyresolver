// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"sort"

	"github.com/posit-dev/go-python-packaging/version"
)

// selectHighest returns the version PEP 440 says to prefer from an
// unconstrained candidate set, and false if the set is empty.
//
// # Pre-releases are excluded unless nothing else is available
//
// PEP 440 excludes pre-releases from selection by default: "pre-releases of any
// kind ... should not be automatically selected", and installers should only
// consider them when no final release satisfies the request. Taking the highest
// version outright therefore picks a release candidate over a stable version,
// which is not what pip would install.
//
// The fallback in the second half of that rule is part of it, not an edge case:
// a package whose only published versions are pre-releases is still installable,
// so an empty result after filtering means keep everything.
//
// # What counts as a pre-release
//
// Alpha, beta, release-candidate AND dev releases. A post-release does not,
// so 1.1.post1 stays selectable, while 1.0.post1.dev0 does not because of the
// dev segment. version.IsPreRelease already draws the line in that place.
//
// # ⚠️ This is selection policy, and it does NOT belong in the CLI long-term
//
// Constraint checking must NOT be changed to match this. A pre-release satisfies
// a plain ">=" constraint — measured against pypa/packaging 26.2, where
// SpecifierSet(">=1.0").contains(2.0.0rc1) is True. The exclusion lives in that
// library's filter() rather than in contains(), which is to say it is a property
// of choosing a candidate, not of testing one. Conflating the two would make
// perfectly valid constraints unsatisfiable.
//
// The durable home for this is candidate selection in the resolver, where it can
// also honour the per-constraint opt-in: a specifier that itself names a
// pre-release admits them for that requirement. This function cannot see the
// constraint, so it implements only the unconstrained default.
func selectHighest(vers []version.Version) (version.Version, bool) {
	if len(vers) == 0 {
		return version.Version{}, false
	}

	eligible := make([]version.Version, 0, len(vers))
	for _, v := range vers {
		if !v.IsPreRelease() {
			eligible = append(eligible, v)
		}
	}
	if len(eligible) == 0 {
		// Every candidate is a pre-release, so the default does not apply and
		// the whole set is back in play.
		eligible = append(eligible, vers...)
	}

	sort.Sort(version.SortedVersions(eligible))
	return eligible[len(eligible)-1], true
}
