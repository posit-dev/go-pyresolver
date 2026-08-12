// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// PrereleaseSet records which packages may have pre-release versions offered.
//
// Keys are PEP 503-canonical names. A package absent from the set, or present
// with a false value, gets final releases only.
type PrereleaseSet map[index.PackageName]bool

// EnabledPrereleases derives the set from the requirements a resolution starts
// with, plus any packages the caller names explicitly.
//
// A package is enabled when some requirement's specifier for it names a
// pre-release -- ">=1.0rc1" asks for pre-releases, ">=1.0" does not -- or when
// the caller listed it in allow. Requirement names arrive as written and are
// canonicalized here; allow is assumed to hold values already built with
// index.NewPackageName.
//
// The set is computed ONCE, before solving, and must not be recomputed as the
// solver narrows a package's allowed range. That is what makes pre-release
// admission a fact about a version rather than a fact about the current search
// state, and go-pubgrub caches derivations on the assumption that the facts
// behind them do not move.
//
// ⚠️ Detection uses Specifiers.PreReleases, NOT Specifiers.FilterVersions, and
// the difference is not stylistic. FilterVersions implements pip's fuller rule,
// which includes "fall back to pre-releases when nothing final satisfies" --
// its answer therefore depends on which candidates happen to be on offer. The
// solver narrows the allowed set as it backtracks, so the same package would
// admit a pre-release under one range and reject it under a wider one, and a
// cached incompatibility derived from the earlier answer would then be wrong.
// Do not call FilterVersions here, and do not call it from Candidates either.
//
// Note that "!=1.0a1" does not enable pre-releases even though it names one,
// and neither does "==1.*"; both match pypa/packaging's own derivation, which
// Specifiers.PreReleases implements.
func EnabledPrereleases(reqs []requirement.Requirement, allow []index.PackageName) PrereleaseSet {
	set := make(PrereleaseSet, len(reqs)+len(allow))
	for _, r := range reqs {
		if r.Specifiers.PreReleases() == version.PreReleasesInclude {
			set[index.NewPackageName(r.Name)] = true
		}
	}
	for _, p := range allow {
		set[p] = true
	}
	return set
}

// Admits reports whether v may be offered for pkg.
//
// Every final release is admitted; a pre-release is admitted only for a
// package the set enables. "Pre-release" here is version.IsPreRelease, so a
// development release (2.0.dev1) counts and a post-release (2.0.post1) does
// not -- confirmed against go-python-packaging v0.5.0 rather than assumed.
//
// This is admission, not ranking: a version this method rejects is one the
// resolution genuinely may not use, and the reason does not change while the
// solver runs. Anything that merely makes a version less desirable belongs in
// a Policy, where it cannot make the version look nonexistent.
func (s PrereleaseSet) Admits(pkg index.PackageName, v version.Version) bool {
	if !v.IsPreRelease() {
		return true
	}
	return s[pkg]
}
