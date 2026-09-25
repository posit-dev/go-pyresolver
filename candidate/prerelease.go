// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// PrereleaseSet records which packages rank their pre-releases alongside
// their final releases, rather than after all of them.
//
// Keys are PEP 503-canonical names. A package absent from the set, or present
// with a false value, still offers its pre-releases -- only after its finals.
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
// For an enabled package, its pre-releases are ranked right alongside its
// final releases by Policy. For a package that is not enabled, Admits still
// says every version exists (see below); the caller ranks its pre-releases
// after every final release instead, so a pre-release is only ever chosen
// there when nothing final is usable. That in-range fallback matches pip and
// current uv.
//
// The set is computed ONCE, before solving, and must not be recomputed as the
// solver narrows a package's allowed range. That is what keeps a version's
// pre-release status a fact about the version rather than a fact about the
// current search state, and go-pubgrub caches derivations on the assumption
// that the facts behind them do not move.
//
// ⚠️ Detection uses Specifiers.PreReleases, NOT Specifiers.FilterVersions, and
// the difference is not stylistic. FilterVersions implements pip's fuller rule,
// which includes "fall back to pre-releases when nothing final satisfies" --
// its answer therefore depends on which candidates happen to be on offer. The
// solver narrows the allowed set as it backtracks, so the same package would
// admit a pre-release under one range and reject it under a wider one, and a
// cached incompatibility derived from the earlier answer would then be wrong.
// Do not call FilterVersions here, and do not call it from Candidates either.
// The pip/uv in-range fallback described above is implemented as ranking
// instead, precisely so it can react to the range without moving admission.
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
		// Re-normalize rather than trusting the caller. index.PackageName is a
		// plain string type, so index.PackageName("Flask_Login") is
		// constructible without ever passing through NewPackageName, and an
		// unnormalized entry here would simply never match a lookup -- the
		// caller would have asked for pre-releases and silently not got them.
		set[index.NewPackageName(string(p))] = true
	}
	return set
}

// Admits now decides ORDERING, not admission: every version of pkg is
// admissible for existence, whatever this returns. What it decides is
// whether v should be ranked no worse than pkg's final releases. A final
// release always is; a pre-release is only for a package the set enables --
// everyone else's pre-releases get ranked after every final release, so one
// is picked only when nothing final is usable (see EnabledPrereleases).
// "Pre-release" here is version.IsPreRelease, so a development release
// (2.0.dev1) counts and a post-release (2.0.post1) does not -- confirmed
// against go-python-packaging v0.5.0 rather than assumed.
func (s PrereleaseSet) Admits(pkg index.PackageName, v version.Version) bool {
	if !v.IsPreRelease() {
		return true
	}
	return s[pkg]
}
