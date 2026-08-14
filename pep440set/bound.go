// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// edge names a position relative to a version, rather than a version itself.
//
// PEP 440 needs this: local labels and post numbers are unbounded above, so
// neither "the least version above every 1.0+local" nor "the least version
// above every 1.0.postN" exists, yet `==1.0` needs the first as its upper
// bound and `>1.0` needs the second as its lower bound.
type edge uint8

const (
	// edgeBelowRelease is below every version sharing this version's
	// (epoch, release), including its dev and pre-releases.
	edgeBelowRelease edge = iota
	// edgeAt is exactly this version's position in the total order.
	edgeAt
	// edgeAboveExact is immediately above this version and nothing else: above
	// 1.0+a but below 1.0+b. `==1.0+a` needs it, because that specifier matches
	// the one local it names and no other (version.Specifiers keeps the local
	// segment when the operand carries one).
	edgeAboveExact
	// edgeAboveLocals is above this version and every local variant of it,
	// and below the next public version (e.g. below 1.0.post0.dev0).
	edgeAboveLocals
	// edgeAboveRelease is above every version sharing this version's
	// (epoch, release), including every post-release of it.
	edgeAboveRelease
)

// bound is a position in the version order. inf is -1, 0 or +1.
//
// key is the part of the position that depends on v alone, derived once when
// the bound is built. See posKey: deriving it per comparison instead is what
// made a failing resolution allocate tens of gigabytes
// (rstudio/package-manager#19713).
type bound struct {
	inf  int8
	v    version.Version
	edge edge
	key  *posKey
}

// posKey is everything cmpBound needs from a bound's version.
//
// ⚠️ DERIVE IT AT CONSTRUCTION, NOT PER COMPARISON. version.Version holds its
// public spelling only as something String() builds, and Public() then needs a
// re-parse to be comparable, so the public fields below cost a render each.
// cmpBound runs in the innermost loop of the set algebra, which is itself in
// the solver's hot loop, so it is called orders of magnitude more often than a
// bound is built. One resolution against a curated-shaped index -- packages
// present, transitive dependencies absent -- took 17.5 s and allocated 25 GB
// CUMULATIVELY with this work on the comparison side. Cumulatively, not
// concurrently: peak heap stayed under 120 MB, so what the churn bought was
// garbage collection. The cost is latency, not footprint.
type posKey struct {
	// rel is v's release group: its epoch and its trailing-zero-stripped
	// release segments, ignoring the pre/post/dev/local suffix. gpp derives it
	// from the parsed fields, so unlike the rest of this struct it costs no
	// render -- see the note above cmpBound.
	rel version.ReleaseKey
	// public is v.Public(), and pub is that spelling parsed, which is v with
	// any local label removed. pubOK is false when the spelling does not parse,
	// which leaves the public comparison out entirely, exactly as it was when
	// the parse happened inline.
	public string
	pub    version.Version
	pubOK  bool
}

// newPosKey derives the key for v.
func newPosKey(v version.Version) *posKey {
	k := &posKey{rel: v.ReleaseKey(), public: v.Public()}
	if k.public == "" {
		// An uninitialized Version. Parsing its (empty) public spelling failed
		// when cmpBound did it inline, so it stays out of the comparison.
		return k
	}
	if v.Local() == "" {
		// Public is v's whole spelling, so v already IS its public version and
		// re-parsing it would only rebuild the same fields.
		k.pub, k.pubOK = v, true
		return k
	}
	if pub, err := version.Parse(k.public); err == nil {
		k.pub, k.pubOK = pub, true
	}
	return k
}

// pos returns b's key, deriving it on demand for a bound built as a literal
// rather than through newBound. Correct either way; the derived-on-demand path
// is the slow one, and TestBoundKeyAgreesWithLiteral holds the two together.
func (b bound) pos() *posKey {
	if b.key != nil {
		return b.key
	}
	return newPosKey(b.v)
}

// newBound is the constructor every bound with a version goes through.
func newBound(v version.Version, e edge) bound {
	return bound{v: v, edge: e, key: newPosKey(v)}
}

// withEdge is b's version at a different edge. The key depends on the version
// alone, so it carries over.
func (b bound) withEdge(e edge) bound {
	return bound{inf: b.inf, v: b.v, edge: e, key: b.key}
}

func negInf() bound { return bound{inf: -1} }
func posInf() bound { return bound{inf: 1} }

func atBound(v version.Version) bound { return newBound(v, edgeAt) }

// tier collapses edge into the three-way grouping cmpBound sorts on after the
// release group: below the group, inside it, above it.
func (b bound) tier() int {
	switch b.edge {
	case edgeBelowRelease:
		return 0
	case edgeAboveRelease:
		return 2
	default:
		return 1
	}
}

// cmpBound reports whether a is before (-1), at (0) or after (+1) b.
//
// # Where the release group key comes from, and why it is not derived here
//
// The (epoch, release) group key is version.ReleaseKey, derived by gpp from the
// parsed Version's own epoch and release fields.
//
// ⚠️ DO NOT REDERIVE IT FROM A RENDERED STRING. This package used to: it called
// BaseVersion(), which renders "1!3.4.5" through a bytes.Buffer with one
// math/big decimal conversion PER SEGMENT, and then split the result back into
// digit runs -- once per candidate version per Contains call.
//
// That derivation was 82% of Set.Contains on the warm app-set resolution and
// 74% on wide-versions, measured with pprof against the production snapshot.
//
// ⚠️ Measured as a share of resolver.Resolve, NOT as a share of total samples.
// The denominator has to be something the change does not move: removing this
// allocation shrinks the profile's garbage-collection share too, so "% of
// samples" would credit this change with that as well. Against Resolve,
// Contains falls from 41% to 24% on app-set and from 33% to 14% on
// wide-versions -- 3.1x and 3.8x fewer samples in absolute terms.
//
// End to end that is 1.39x warm on app-set and 1.20x on wide-versions, with
// 2.27x fewer allocations. BenchmarkContains/cross-group -- the common case,
// probing a span that anchors some other release -- went from 304 ns and 8
// allocations to 104 ns and none.
//
// ⚠️ strings.Split leaves the profile entirely, but writeRelease and
// math/big.nat.itoa DO NOT: ensurePub still renders the public spelling, which
// this change deliberately does not touch. They drop from 2.3% and 1.6% of
// samples under Contains to below the sampling floor, which is not the same as
// gone. What is left inside Contains is mostly verPos.init, and most of that is
// the two Version copies it makes rather than the key.
//
// ⚠️ AND DO NOT REPLACE IT WITH ints. An even earlier key parsed each segment
// with strconv.Atoi and BROKE out of the loop on error, so a segment at or
// above 2^63 was dropped along with every segment AFTER it: the key became a
// PREFIX of the real release, and 99999999999999999999.0 keyed as the empty
// release, sorting below every version in existence. That made
// `>99999999999999999999.0` admit everything while Check admitted nothing, and
// `<1.5` admit 1.99999999999999999999. PEP 440 puts no ceiling on an epoch or a
// release segment -- the grammar is `[0-9]+` -- and ReleaseKey is exact at
// every magnitude, comparing arbitrary-precision integers for the keys that do
// not fit its packed fast path.
func cmpBound(a, b bound) int {
	if a.inf != 0 || b.inf != 0 {
		switch {
		case a.inf < b.inf:
			return -1
		case a.inf > b.inf:
			return 1
		}
		return 0
	}

	ak, bk := a.pos(), b.pos()
	if c := ak.rel.Compare(bk.rel); c != 0 {
		return c
	}

	switch at, bt := a.tier(), b.tier(); {
	case at < bt:
		return -1
	case at > bt:
		return 1
	case at != 1:
		// Both below the same group, or both above it: same position.
		return 0
	}

	// Both inside the group. Compare the public version first, so that
	// aboveLocals(1.0) lands below at(1.0.post0.dev0) ...
	//
	// Identical public spellings are identical PUBLIC versions -- both are the
	// normalized rendering -- so the compare is skipped rather than run to
	// reach 0. It says nothing about the bounds' own versions, which can still
	// differ in the local label the public spelling drops; the two blocks below
	// are what order those. Skipping is the common case here: the edges around
	// one version all share its public spelling, and Version.Compare's own fast
	// path renders both sides to a string before it can say so.
	if ak.public != bk.public && ak.pubOK && bk.pubOK {
		if c := ak.pub.Compare(bk.pub); c != 0 {
			return c
		}
	}

	// ... edgeAboveLocals second, so that it lands above every at(1.0+local) no
	// matter how the label sorts. It is the only edge that ignores the local
	// segment of the version it is anchored to: it is a property of the public
	// version alone.
	aTop, bTop := a.edge == edgeAboveLocals, b.edge == edgeAboveLocals
	switch {
	case aTop && bTop:
		return 0
	case aTop:
		return 1
	case bTop:
		return -1
	}

	// ... and, among the two edges that name one local variant, the label
	// before the edge: aboveExact(1.0+a) sits below at(1.0+b), which is what
	// makes `==1.0+a` exclude 1.0+b while `<=1.0` still admits both.
	if c := strings.Compare(a.v.Local(), b.v.Local()); c != 0 {
		return c
	}
	switch {
	case a.edge < b.edge:
		return -1
	case a.edge > b.edge:
		return 1
	}
	return 0
}
