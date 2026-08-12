// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"sort"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/version"
)

// Policy orders the admissible versions of a package, deciding which one the
// solver tries first.
//
// It is an interface so an embedder can express preferences this module has no
// business knowing about -- Package Manager demoting versions that are blocked
// by an administrator or carry a known vulnerability, for instance. What it
// cannot do is make a version unavailable: see Rank.
type Policy interface {
	// Less reports whether a should be TRIED BEFORE b. Both are admissible
	// versions of pkg.
	//
	// It must be a strict weak ordering: irreflexive (Less(p, v, v) is
	// false) and consistent under swapping (Less(p, a, b) and Less(p, b, a)
	// are never both true). Rank sorts with it, and an inconsistent
	// ordering yields an arbitrary result rather than an error.
	Less(pkg index.PackageName, a, b version.Version) bool
}

// Newest is the default Policy: highest version first, which is what a Python
// user expects an installer to do absent any other instruction.
type Newest struct{}

// Less reports whether a is a higher version than b, ignoring pkg -- Newest
// treats every package the same way.
func (Newest) Less(_ index.PackageName, a, b version.Version) bool {
	return a.Compare(b) > 0
}

// Rank returns the versions of pkg in the order p wants them tried, most
// preferred first. A nil p means Newest.
//
// The result is a new slice; the input is left untouched, because the caller's
// slice usually comes straight from a MetadataIndex and may be shared or
// cached. Sorting is stable, so versions a Policy considers equivalent stay in
// the index's order rather than an arbitrary one -- that keeps a resolution
// reproducible when the policy expresses no preference.
//
// Rank returns exactly as many versions as it is given. It is a reordering and
// never a filter: Provider.Candidates reports a count the solver reads as
// whether anything satisfies a requirement at all, so a dropped version would
// be indistinguishable from a version that does not exist, and the failure
// report would then describe a conflict that is not the real one. A caller who
// wants a version gone must keep it out of the index, not out of the ranking.
func Rank(pkg index.PackageName, versions []version.Version, p Policy) []version.Version {
	if p == nil {
		p = Newest{}
	}
	out := make([]version.Version, len(versions))
	copy(out, versions)
	sort.SliceStable(out, func(i, j int) bool {
		return p.Less(pkg, out[i], out[j])
	})
	return out
}
