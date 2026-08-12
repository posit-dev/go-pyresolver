// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import "strings"

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
// A span is an interval over positions (see the package doc), and several
// positions have no specifier that names them: "above every 1.0+local but below
// 1.0.post0.dev0" is a real bound and `<=1.0` is only the nearest spelling of
// it. Those are rendered as the nearest spelling, so a rendering is exact for
// every set a specifier produced -- which is every set a user is likely to see
// named in a report -- and approximate only across gaps that no version
// occupies. String is therefore for reading, not for round-tripping: do not
// parse it back and assume the same set.
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
		return ">=" + b.v.String()
	case edgeBelowRelease:
		// The bottom of a release group. Its least member is the group's own
		// dev0, which sorts below every pre-release of it.
		return ">=" + b.v.BaseVersion() + ".dev0"
	default:
		// edgeAboveExact, edgeAboveLocals and edgeAboveRelease all sit just
		// above the version they name; which of them it is decides whether the
		// locals and post-releases of that version are in, and no specifier
		// distinguishes those cases.
		return ">" + b.v.String()
	}
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
		// `<V` stops below V's earliest pre-release, so lessThanBound anchors
		// this bound at V.dev0. Rendering the anchor verbatim would print
		// "<2.0.dev0" for the "<2.0" the user wrote, which reads like a
		// constraint the resolver invented. The two denote the same set.
		if trimmed, ok := strings.CutSuffix(b.v.Public(), ".dev0"); ok && b.v.Local() == "" {
			return "<" + trimmed
		}
		return "<" + b.v.String()
	case edgeBelowRelease:
		return "<" + b.v.BaseVersion()
	default:
		return "<=" + b.v.String()
	}
}
