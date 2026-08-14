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
	// false), consistent under swapping (Less(p, a, b) and Less(p, b, a)
	// are never both true), and TRANSITIVE — if a sorts before b and b
	// before c, then a must sort before c. Rank sorts with it, and an
	// inconsistent ordering yields an arbitrary result rather than an error.
	//
	// ⚠️ Transitivity is not decoration, and it is the property a hand-written
	// Less is most likely to break. Provider.Candidates ranks the in-range
	// versions and then stops at the first USABLE one, which yields the same
	// version as ranking the usable ones alone only because a stable sort with a
	// transitive comparator orders a subset consistently with the superset it
	// came from. Break transitivity and which version gets chosen starts to
	// depend on which other versions happened to be in range — still a legal
	// version, so nothing fails loudly, but no longer reproducible.
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
// never a filter, and the reason is now stronger than it was: Provider.Candidates
// walks this ranking and stops at the first usable version, so a version a Policy
// dropped is not merely uncounted — it is UNREACHABLE. A package whose only usable
// version the Policy disliked would report found == false, indistinguishable from
// a package with nothing published, and the failure report would then describe a
// conflict that is not the real one. A caller who wants a version gone must keep
// it out of the index, not out of the ranking.
func Rank(pkg index.PackageName, versions []version.Version, p Policy) []version.Version {
	if p == nil {
		p = Newest{}
	}
	out := make([]version.Version, len(versions))
	copy(out, versions)

	switch monotonicity(pkg, out, p) {
	case ordered:
		// Already in Policy order. A stable sort of a sorted sequence is the
		// identity, so there is nothing to do.
		return out
	case reversed:
		// Strictly descending, so the answer is the exact reverse and no two
		// elements are equivalent -- which is what makes stability vacuous here
		// rather than something this branch has to preserve.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}

	sort.SliceStable(out, func(i, j int) bool {
		return p.Less(pkg, out[i], out[j])
	})
	return out
}

// shape is what monotonicity found.
type shape int

const (
	// unordered means neither fast path applies and the sort has to run.
	unordered shape = iota
	// ordered means no element is strictly before its predecessor.
	ordered
	// reversed means every element is strictly before its predecessor.
	reversed
)

// monotonicity classifies vs against p in ONE pass of at most n-1 Less calls --
// exactly one per adjacent pair -- stopping as soon as neither shape can hold.
//
// # Why this is worth a pass
//
// It is not a micro-optimization aimed at a hypothetical caller. index.RSFIndex
// returns a package's versions sorted ASCENDING and deduped, and the default
// Policy is Newest, which wants them descending -- so the real, overwhelmingly
// common input to this function is an exactly reversed sequence, which is the
// worst case for sort.SliceStable's insertion phase. On an ascending input this
// replaces about 9.8 Less calls per element with one: 13,999 against 137,387 at
// n=14,000, from BenchmarkPolicyLessCalls.
//
// The wasted work when neither shape holds is bounded by the break: it costs one
// Less call on the pair that settles the second shape, so two pairs minimum, and
// for an unsorted list that is normally the first two. Measured end to end on a
// shuffled list of 14,000, the whole pass costs 3 extra comparisons against the
// sort alone -- 243,629 against 243,626.
//
// # ⚠️ This LEANS ON transitivity where sort.SliceStable merely benefits from it
//
// Adjacent comparisons imply a global order only if the relation is transitive.
// Policy already requires that in as many words, so this is not a new demand on
// an embedder -- but it is a new place where breaking it goes unnoticed. An
// intransitive Less fed to sort.SliceStable yields an arbitrary order; fed to
// this, it yields the input order or its reverse, which is a DIFFERENT arbitrary
// order. Neither is detectable at runtime and neither is worth detecting; the
// point is only that a reviewer must not read this fast path as free.
//
// TestRankFastPathAgreesWithTheSort pins the agreement on real corpus data,
// where the sole Policy in the module is Newest and Newest is transitive because
// PEP 440 ordering is total.
func monotonicity(pkg index.PackageName, vs []version.Version, p Policy) shape {
	if len(vs) < 2 {
		// One element or none is trivially both; report ordered, which returns
		// it untouched.
		return ordered
	}
	asc, desc := true, true
	for i := 1; i < len(vs); i++ {
		if before := p.Less(pkg, vs[i], vs[i-1]); before {
			asc = false
		} else {
			desc = false
		}
		if !asc && !desc {
			return unordered
		}
	}
	if asc {
		return ordered
	}
	return reversed
}
