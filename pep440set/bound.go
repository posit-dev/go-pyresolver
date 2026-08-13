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
// ⚠️ DERIVE IT AT CONSTRUCTION, NOT PER COMPARISON. Every field here costs at
// least one string render: version.Version holds its release as big.Ints and
// its public spelling only as something String() builds, and Public() then
// needs a re-parse to be comparable. cmpBound runs in the innermost loop of the
// set algebra, which is itself in the solver's hot loop, so it is called orders
// of magnitude more often than a bound is built. One resolution against a
// curated-shaped index -- packages present, transitive dependencies absent --
// took 17.5 s and allocated 25 GB CUMULATIVELY with this work on the comparison
// side. Cumulatively, not concurrently: peak heap stayed under 120 MB, so what
// the churn bought was garbage collection. The cost is latency, not footprint.
type posKey struct {
	// epoch and release are the canonical (leading-zero-free, trailing-zero-
	// stripped) decimal digit runs releaseKey produces.
	epoch   string
	release []string
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
	epoch, release := releaseKey(v)
	k := &posKey{epoch: epoch, release: release, public: v.Public()}
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

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// canonDigits strips leading zeros from a digit run, keeping one digit for an
// all-zero segment, so "007" and "7" produce the same key component.
func canonDigits(s string) string {
	i := 0
	for i < len(s)-1 && s[i] == '0' {
		i++
	}
	return s[i:]
}

// releaseKey returns the (epoch, release) of b's version, with trailing zeros
// stripped so 1.0 and 1.0.0.0 share a key. BaseVersion renders "1!3.4.5" for
// an epoch, so the epoch is split off here.
//
// ⚠️ THE KEY COMPONENTS ARE DECIMAL STRINGS, NOT ints. DO NOT "SIMPLIFY" THIS
// BACK TO strconv.Atoi.
//
// PEP 440 puts no ceiling on an epoch or a release segment -- the grammar is
// `[0-9]+` -- and gpp stores both as arbitrary-precision part.BigInt, so it
// orders 1.99999999999999999999 above 1.5 correctly. The earlier key parsed
// each segment with strconv.Atoi and BROKE out of the loop on error, so a
// segment at or above 2^63 was dropped along with every segment AFTER it: the
// key became a PREFIX of the real release, and 99999999999999999999.0 keyed as
// the empty release, sorting below every version in existence. That made
// `>99999999999999999999.0` admit everything while Check admitted nothing, and
// `<1.5` admit 1.99999999999999999999. Comparing the digit runs directly
// (length first, then byte-wise) is exact at every magnitude and needs no
// math/big.
func releaseKey(v version.Version) (epoch string, release []string) {
	base := v.BaseVersion()
	epoch = "0"
	if i := strings.Index(base, "!"); i >= 0 {
		// A non-numeric epoch cannot come out of BaseVersion; if one ever did,
		// treating it as 0 keeps this a total order rather than a panic.
		if isDigits(base[:i]) {
			epoch = canonDigits(base[:i])
		}
		base = base[i+1:]
	}
	for _, part := range strings.Split(base, ".") {
		if !isDigits(part) {
			break
		}
		release = append(release, canonDigits(part))
	}
	for len(release) > 0 && release[len(release)-1] == "0" {
		release = release[:len(release)-1]
	}
	return epoch, release
}

// cmpDigits orders two canonical (leading-zero-free) digit runs by value. The
// shorter run is the smaller number, and equal-length runs compare byte-wise,
// which for ASCII digits is the same as comparing values.
func cmpDigits(a, b string) int {
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return strings.Compare(a, b)
}

// cmpSegments orders two release keys segment by segment, the shorter being
// smaller when it is a prefix of the longer. releaseKey has already stripped
// trailing zeros, so a shorter key means a genuinely shorter release.
func cmpSegments(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := cmpDigits(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// cmpBound reports whether a is before (-1), at (0) or after (+1) b.
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
	if c := cmpDigits(ak.epoch, bk.epoch); c != 0 {
		return c
	}
	if c := cmpSegments(ak.release, bk.release); c != 0 {
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
