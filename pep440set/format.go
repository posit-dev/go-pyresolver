// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// Markers for the positions PEP 440 has no operator for. A rendering carrying
// one is not a specifier and will not parse; the marker says which region the
// bare specifier in front of it would have got wrong, so the text still names
// the right set of versions to a reader.
//
// ⚠️ THESE EXIST SO THAT TWO DIFFERENT POSITIONS NEVER RENDER THE SAME TEXT.
// Dropping one back to its bare specifier does not just lose detail: it makes
// String say a set holds versions it does not (or hides ones it does), which is
// the failure mode measured in TestStringDistinguishesSetsHoldingDifferentVersions.
const (
	// markerWithPre: the anchor's own dev and pre-releases are IN, which `<V`
	// excludes by PEP 440's exclusive-comparison guard.
	markerWithPre = "[+pre]"
	// markerWithPost: the anchor's own post-releases are IN, which `<=V`
	// excludes -- the whole release group is below this bound.
	markerWithPost = "[+post]"
	// markerWithLocalAndPost: the anchor's locals and post-releases are IN,
	// which `>V` excludes.
	markerWithLocalAndPost = "[+local,+post]"
	// markerWithoutLocal: the anchor's local variants are OUT, which `<=V`
	// admits.
	markerWithoutLocal = "[-local]"
)

// String renders the set as PEP 440 specifier text: "==1.4", ">=1.0,<2.0",
// "==1.*", "!=1.0", "*" for every version and "<none>" for none.
//
// # Why this method exists at all
//
// A set reaches a user through go-pubgrub's failure report, which asks a
// report.Formatter to describe one. Nothing outside this package can: spans and
// bounds are unexported, and they stay that way -- exposing them would make the
// canonical-representation invariant that Equal depends on somebody else's
// problem. Without this method the report falls back to %v and prints the raw
// struct, which measures in the hundreds of characters per range and is
// unreadable in exactly the situation the report exists for.
//
// # It renders POSITIONS, and PEP 440 cannot spell all of them
//
// A span is an interval over positions (see the package doc), and a position is
// finer than a specifier: `>1.0` clears the whole 1.0 release group, while the
// complement of `<=1.0` starts just above 1.0's local variants and therefore
// HOLDS 1.0.post1. Those are different sets of real versions, so rendering both
// as ">1.0" would be two lies at once -- two sets with one rendering, and a
// rendering that names a set other than its own.
//
// Every bound is therefore rendered by naming a VERSION where one exists, even
// when that means printing a version nobody wrote: the position above every
// 1.0+local is rendered ">=1.0.post0.dev0", because 1.0.post0.dev0 is the least
// version above it and nothing sorts in between. Where PEP 440 has no operator
// at all -- "below 1.0 but above its pre-releases", "at or above every
// post-release of 1.0" -- the nearest specifier is printed with a bracketed
// marker (see markerWithPre and friends) naming the region it would otherwise
// misstate. Bounds anchored to a version carrying a local label are printed with
// the label; PEP 440 forbids a local label in an ordered comparison, so `<1.0+a`
// is not a specifier either, and the label is what keeps it distinct.
//
// So: a rendering that parses is exact -- parse it back and it holds the same
// versions, which TestStringRendersEveryEdgeExactly measures for every edge in
// both positions. A rendering that does not parse carries either a marker or a
// local label, which is how a reader can tell.
func (s Set) String() string {
	if len(s.spans) == 0 {
		return "<none>"
	}
	if v, ok := s.excluded(); ok {
		return "!=" + v
	}
	parts := make([]string, 0, len(s.spans))
	for _, sp := range s.spans {
		parts = append(parts, sp.text())
	}
	return strings.Join(parts, " || ")
}

// excluded recognizes the shape Complement(Exactly(v)) produces, so the very
// common `!=1.0` renders as itself rather than as "<1.0 || >1.0".
func (s Set) excluded() (string, bool) {
	if len(s.spans) != 2 {
		return "", false
	}
	lo, hi := s.spans[0], s.spans[1]
	if lo.lo.inf >= 0 || hi.hi.inf <= 0 {
		return "", false
	}
	if lo.hi.edge != edgeAt {
		return "", false
	}
	if hi.lo.edge != edgeAboveLocals && hi.lo.edge != edgeAboveExact {
		return "", false
	}
	if cmpBound(bound{v: lo.hi.v, edge: hi.lo.edge}, hi.lo) != 0 {
		return "", false
	}
	return lo.hi.v.String(), true
}

// text renders one span.
func (sp span) text() string {
	if sp.lo.inf < 0 && sp.hi.inf > 0 {
		return "*"
	}
	if v, ok := sp.exact(); ok {
		return "==" + v
	}
	if prefix, ok := sp.releasePrefix(); ok {
		return "==" + prefix + ".*"
	}

	var clauses []string
	if lo := sp.lo.lowerText(); lo != "" {
		clauses = append(clauses, lo)
	}
	if hi := sp.hi.upperText(); hi != "" {
		clauses = append(clauses, hi)
	}
	return strings.Join(clauses, ",")
}

// exact recognizes the span Exactly builds. It is Singleton's test, applied to
// one span rather than to a whole set.
func (sp span) exact() (string, bool) {
	if sp.lo.inf != 0 || sp.hi.inf != 0 || sp.lo.edge != edgeAt {
		return "", false
	}
	if sp.hi.edge != edgeAboveLocals && sp.hi.edge != edgeAboveExact {
		return "", false
	}
	if cmpBound(bound{v: sp.lo.v, edge: sp.hi.edge}, sp.hi) != 0 {
		return "", false
	}
	return sp.lo.v.String(), true
}

// releasePrefix recognizes the span releasePrefixSpan builds for `==P.*`: one
// run of release groups, from P up to but excluding the next value of P's last
// segment.
func (sp span) releasePrefix() (string, bool) {
	if sp.lo.inf != 0 || sp.hi.inf != 0 {
		return "", false
	}
	if sp.lo.edge != edgeBelowRelease || sp.hi.edge != edgeBelowRelease {
		return "", false
	}
	next, err := incrementLastSegment(sp.lo.v.BaseVersion())
	if err != nil {
		return "", false
	}
	if cmpBound(bound{v: next, edge: edgeBelowRelease}, sp.hi) != 0 {
		return "", false
	}
	return sp.lo.v.BaseVersion(), true
}

// lowerText renders an INCLUSIVE lower bound.
func (b bound) lowerText() string {
	if b.inf < 0 {
		return ""
	}
	if b.inf > 0 {
		// A span starting at +inf holds nothing and newSet drops it, so this is
		// unreachable; rendering something is still better than rendering "".
		return ">*"
	}
	switch b.edge {
	case edgeAt:
		// `>=V` admits V itself, which is this position. A local label on the
		// anchor makes the text unparseable rather than wrong; see the marker
		// block above.
		return ">=" + b.v.String()
	case edgeBelowRelease:
		// The bottom of a release group. Its least member is the group's own
		// dev0, which sorts below every pre-release of it, so naming that
		// version is exact.
		return ">=" + b.v.BaseVersion() + ".dev0"
	case edgeAboveRelease:
		// A property of the release group alone -- aboveRelease(1.0rc1) and
		// aboveRelease(1.0.post9) are the same position -- and PEP 440's `>`
		// operator is defined to clear exactly that group, so the group's own
		// spelling is the exact operand. Rendering the ANCHOR here instead is
		// what made ">1.0rc1" name a set holding 1.0.
		return ">" + b.v.BaseVersion()
	case edgeAboveLocals:
		return b.aboveLocalsLowerText()
	default: // edgeAboveExact
		if b.v.Local() != "" {
			// The only edgeAboveExact bounds that exist: Exactly builds one for
			// `==1.0+a`, and Complement carries it over to `!=1.0+a`. No
			// specifier can carry a local label, but the label makes the text
			// unambiguous.
			return ">" + b.v.String()
		}
		// Unreachable today. `>V` would exclude V's locals and post-releases,
		// which this position holds.
		return ">" + b.v.String() + markerWithLocalAndPost
	}
}

// aboveLocalsLowerText renders the position above b's version and every local
// variant of it, as an INCLUSIVE lower bound.
//
// greaterThanBound is the oracle rather than a second copy of its case analysis:
// `>V` lands on exactly this position when V is a post-release or a dev release,
// and somewhere else (the top of the release group, or the next pre-release)
// otherwise. Asking it removes the possibility of the two drifting apart.
func (b bound) aboveLocalsLowerText() string {
	if pub, err := version.Parse(b.v.Public()); err == nil {
		if lo, err := greaterThanBound(pub); err == nil && cmpBound(lo, b) == 0 {
			return ">" + b.v.Public()
		}
		// Otherwise the least version above V and its locals is V's earliest
		// post-release: 1.0.post0.dev0 for 1.0, 1.0rc1.post0.dev0 for 1.0rc1.
		// Nothing sorts between the two positions, so naming it is exact.
		if least := b.v.Public() + ".post0.dev0"; parses(least) {
			return ">=" + least
		}
	}
	// Unreachable: every anchor reaching this point is a plain release or a
	// pre-release, and both take a `.post0.dev0` suffix.
	return ">" + b.v.Public() + markerWithLocalAndPost
}

// upperText renders an EXCLUSIVE upper bound.
func (b bound) upperText() string {
	if b.inf > 0 {
		return ""
	}
	if b.inf < 0 {
		return "<*"
	}
	switch b.edge {
	case edgeAt:
		return b.atUpperText()
	case edgeBelowRelease:
		// `<BASE` stops below BASE's earliest pre-release, which is the bottom
		// of the release group: exact.
		return "<" + b.v.BaseVersion()
	case edgeAboveLocals:
		// `<=V` admits V and every local variant of it and nothing further:
		// exactly this position. The anchor's own label, if it somehow has one,
		// is not part of the position -- see cmpBound -- so Public is both
		// exact and the only spelling a specifier could take.
		return "<=" + b.v.Public()
	case edgeAboveRelease:
		// The top of the release group. `<=BASE` stops below BASE's
		// post-releases, which this position holds, and no operator names the
		// top of a group: 1.0.0.1 sorts between it and `<1.0.1`.
		return "<=" + b.v.BaseVersion() + markerWithPost
	default: // edgeAboveExact
		if b.v.Local() != "" {
			return "<=" + b.v.String()
		}
		// Unreachable today. `<=V` would admit V's locals, which sort above
		// this position.
		return "<=" + b.v.String() + markerWithoutLocal
	}
}

// atUpperText renders an EXCLUSIVE upper bound at b's version.
//
// lessThanBound is the oracle, for the reason aboveLocalsLowerText gives. It
// anchors `<V` at V.dev0 for a plain release, so the anchor is rendered back as
// the version the user wrote wherever that round-trips -- "<2.0", not
// "<2.0.dev0", which reads like a constraint the resolver invented.
func (b bound) atUpperText() string {
	if b.v.Local() == "" {
		candidates := []string{}
		if trimmed, ok := strings.CutSuffix(b.v.Public(), ".dev0"); ok {
			candidates = append(candidates, trimmed)
		}
		candidates = append(candidates, b.v.Public())
		for _, cand := range candidates {
			v, err := version.Parse(cand)
			if err != nil {
				continue
			}
			hi, err := lessThanBound(v)
			if err == nil && cmpBound(hi, b) == 0 {
				return "<" + cand
			}
		}
	}
	// No operand reproduces the position: `<V` for a plain or post release
	// stops BELOW V's own dev and pre-releases, and this bound holds them.
	// (A local label makes the text unparseable in any case.)
	return "<" + b.v.String() + markerWithPre
}

// parses reports whether s is a version PEP 440 can spell.
func parses(s string) bool {
	_, err := version.Parse(s)
	return err == nil
}
