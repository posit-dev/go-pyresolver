// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// Exactly is the set `==v` matches.
//
// When v carries no local label that is v and every local variant of it, as
// PEP 440 requires: `==1.0` matches `1.0+ubuntu1`. When v carries one it is v
// alone, because a specifier whose operand has a local label compares the
// local label too: `==1.0+a` does not match `1.0+b`, and does not match `1.0`.
func Exactly(v version.Version) Set {
	hi := edgeAboveLocals
	if v.Local() != "" {
		hi = edgeAboveExact
	}
	lo := newBound(v, edgeAt)
	return newSet(span{lo: lo, hi: lo.withEdge(hi)})
}

// Contains reports whether v is in the set.
//
// It probes the spans with a stack-held verPos rather than materializing
// atBound(v): a bound's posKey costs a public-spelling render (and a re-parse
// when v carries a local label) plus a heap allocation, purely to test the
// membership of a version the caller already holds. The resolver calls this
// once per candidate version per Candidates call, which made that derivation a
// measurable slice of resolution time. containsBound remains the reference
// path, and TestContainsAgreesWithContainsBound holds the two together.
func (s Set) Contains(v version.Version) bool {
	if len(s.spans) == 0 {
		return false
	}
	var p verPos
	p.init(v)
	for _, sp := range s.spans {
		if cmpVerBound(&p, sp.lo) >= 0 && cmpVerBound(&p, sp.hi) < 0 {
			return true
		}
	}
	return false
}

// Singleton returns the one version in the set, when it holds exactly one.
//
// go-pubgrub hands the version chosen by Candidates back into Dependencies as
// a set, so the adapter needs a way to get it out again.
func (s Set) Singleton() (version.Version, bool) {
	if len(s.spans) != 1 {
		return version.Version{}, false
	}
	sp := s.spans[0]
	if sp.lo.inf != 0 || sp.hi.inf != 0 {
		return version.Version{}, false
	}
	if sp.lo.edge != edgeAt {
		return version.Version{}, false
	}
	if sp.hi.edge != edgeAboveLocals && sp.hi.edge != edgeAboveExact {
		return version.Version{}, false
	}
	if cmpBound(sp.lo.withEdge(sp.hi.edge), sp.hi) != 0 {
		return version.Version{}, false
	}
	return sp.lo.v, true
}

// ErrUnrepresentable reports a specifier with no version-set equivalent.
//
// `===` arbitrary equality is the main case: PEP 440 defines it as string
// equality on the original text, so `===1.0` rejects `1.0.0` while every
// version set containing 1.0 accepts it. A caller should treat the affected
// release as unusable rather than approximating.
var ErrUnrepresentable = errors.New("pep440set: specifier has no version-set equivalent")

// FromSpecifiers converts a PEP 440 specifier set into a version set.
//
// The result is the intersection of one span set per specifier. Where a set
// comes back, Contains and version.Specifiers.Check are built to answer alike
// for every version: Check is pure MATCHING, and so is this. Pre-release
// exclusion is deliberately NOT applied on top of it, because that is a
// selection rule -- which candidates an installer offers -- and applying it
// here would make Complement unsound. The resolver's candidate layer decides
// what to offer.
//
// ⚠️ AGREEMENT IS A TESTED PROPERTY, NOT A THEOREM. Nothing derives these
// spans from Check; construct.go reimplements each operator, so the two can
// only be held together by measurement. They are, over a generated grid, a
// production PyPI snapshot and a fuzzer, and each of those has already caught a
// case the others missed. Treat a disagreement as a bug here until Check has
// been checked against pypa/packaging, which is the reference for both.
//
// ⚠️ The operator-level pre-release and post-release guards ARE applied,
// because those are matching: `<1.0` does not match `1.0rc1` and `>1.0` does
// not match `1.0.post1` under any pre-release policy.
func FromSpecifiers(ss version.Specifiers) (Set, error) {
	// ⚠️ List flattens OR-groups, so intersecting it is only correct for a set
	// that has one group. `>=1||<2` admits every version, while the
	// intersection of its members admits none, and silently returning the
	// second for the first is a wrong answer rather than a narrow one. The
	// OR-of-ANDs shape is version.Specifiers' R-only `||` extension with no
	// PEP 440 spelling, and no exported accessor reaches the groups, so it is
	// refused rather than mis-read.
	if strings.Contains(ss.String(), "||") {
		return Set{}, fmt.Errorf("%w: %s", ErrUnrepresentable, ss.String())
	}

	out := All()
	for _, sp := range ss.List() {
		one, err := fromSpecifier(sp)
		if err != nil {
			return Set{}, err
		}
		out = out.Intersect(one)
	}
	return out, nil
}

func fromSpecifier(sp version.Specifier) (Set, error) {
	op, operand := sp.Operator(), strings.TrimSpace(sp.Version())

	if op == "===" {
		return Set{}, fmt.Errorf("%w: %s%s", ErrUnrepresentable, op, operand)
	}

	if strings.HasSuffix(operand, ".*") {
		switch op {
		case "==", "=", "!=", "":
			prefix := strings.TrimSuffix(operand, ".*")
			lo, hi, err := releasePrefixSpan(prefix)
			if err != nil {
				return Set{}, err
			}
			s := newSet(span{lo, hi})
			if op == "!=" {
				return s.Complement(), nil
			}
			return s, nil
		default:
			return Set{}, fmt.Errorf("%w: %s%s", ErrUnrepresentable, op, operand)
		}
	}

	v, err := version.Parse(operand)
	if err != nil {
		return Set{}, fmt.Errorf("pep440set: operand %q: %w", operand, err)
	}

	switch op {
	case ">=":
		// The operand cannot carry a local label here (the grammar rejects it),
		// and the prospective version's label is ignored, so the lowest
		// matching position is the operand itself.
		return newSet(span{newBound(v, edgeAt), posInf()}), nil
	case ">":
		lo, err := greaterThanBound(v)
		if err != nil {
			return Set{}, err
		}
		return newSet(span{lo, posInf()}), nil
	case "<=":
		// A local label on the prospective version is ignored, so every local
		// variant of the operand still matches.
		return newSet(span{negInf(), newBound(v, edgeAboveLocals)}), nil
	case "<":
		hi, err := lessThanBound(v)
		if err != nil {
			return Set{}, err
		}
		return newSet(span{negInf(), hi}), nil
	case "==", "=", "":
		return Exactly(v), nil
	case "!=":
		return Exactly(v).Complement(), nil
	case "~=":
		// ~=X.Y is >=X.Y combined with ==X.*, and the prefix comes from the RAW
		// operand text -- see compatibleUpperBound.
		hi, ok, err := compatibleUpperBound(operand)
		if err != nil {
			return Set{}, err
		}
		if !ok {
			// The `==prefix.*` half matches nothing, so the conjunction is
			// empty however permissive the `>=` half is.
			return Empty(), nil
		}
		return newSet(span{newBound(v, edgeAt), hi}), nil
	default:
		return Set{}, fmt.Errorf("%w: unknown operator %q", ErrUnrepresentable, op)
	}
}

// preSuffixRegexp matches the pre-release segment at the end of a normalized
// public version: version renders it as a letter (a, b or rc) plus a number,
// with no separator.
var preSuffixRegexp = regexp.MustCompile(`^(.*)(a|b|rc)([0-9]+)$`)

// lessThanBound is the upper bound of `<v`.
//
// PEP 440: "<V MUST NOT allow a pre-release of the specified version unless the
// specified version is itself a pre-release." That is a MATCHING rule, not
// candidate selection -- no pre-release policy turns it off -- so `<1.0` stops
// below 1.0.dev0 rather than at 1.0, and `<1.0.post1` stops below
// 1.0.post1.dev0. When the operand is itself a pre-release the guard is off and
// the bound is the operand's own position.
func lessThanBound(v version.Version) (bound, error) {
	if v.IsPreRelease() {
		return newBound(v, edgeAt), nil
	}
	// The earliest pre-release of v: the same version with dev set to 0. The
	// operand cannot carry a local label, so Public is its whole spelling.
	earliest, err := version.Parse(v.Public() + ".dev0")
	if err != nil {
		return bound{}, fmt.Errorf(
			"pep440set: earliest pre-release of %q: %w", v.Public(), err)
	}
	return newBound(earliest, edgeAt), nil
}

// greaterThanBound is the lower bound of `>v`.
//
// PEP 440: ">V MUST NOT allow a post-release of the specified version unless
// the specified version is itself a post-release", and ">V MUST NOT match a
// local version of V". Both are matching rules, so the bound has to clear
// whichever of those regions exists:
//
//   - a plain release excludes every post-release and local of itself, and
//     nothing else in its release group sorts above it, so the bound is above
//     the whole group;
//   - a post-release excludes only its own locals, since the post-release guard
//     is off;
//   - a dev release likewise, since a post-release of a dev release cannot be
//     spelled (dev is the innermost segment);
//   - a pre-release excludes its locals and its own post-releases, which are
//     unbounded, so the bound is the least version carrying the next
//     pre-release number -- 1.0rc1.post9 sorts below 1.0rc2.dev0 for every 9.
func greaterThanBound(v version.Version) (bound, error) {
	switch {
	case v.IsPostRelease():
		return newBound(v, edgeAboveLocals), nil
	case v.IsPreRelease():
		m := preSuffixRegexp.FindStringSubmatch(v.Public())
		if m == nil {
			// A dev release: no post-release of it can exist.
			return newBound(v, edgeAboveLocals), nil
		}
		// String arithmetic for the same reason incrementLastSegment uses it: a
		// pre-release number is `[0-9]*` with no ceiling.
		next, err := version.Parse(m[1] + m[2] + incDigits(m[3]) + ".dev0")
		if err != nil {
			return bound{}, fmt.Errorf(
				"pep440set: next pre-release after %q: %w", v.Public(), err)
		}
		return newBound(next, edgeAt), nil
	default:
		return newBound(v, edgeAboveRelease), nil
	}
}

// releasePrefixSpan returns the span covering every version whose release
// starts with prefix, e.g. "1" -> [belowRelease(1), belowRelease(2)).
//
// The grammar allows a wildcard only against a bare release segment -- no
// epoch-less pre, post, dev or local part -- so the whole matched region is one
// run of release groups.
func releasePrefixSpan(prefix string) (lo, hi bound, err error) {
	loV, err := version.Parse(prefix)
	if err != nil {
		return lo, hi, fmt.Errorf("pep440set: prefix %q: %w", prefix, err)
	}
	nextV, err := incrementLastSegment(loV.BaseVersion())
	if err != nil {
		return lo, hi, err
	}
	return newBound(loV, edgeBelowRelease),
		newBound(nextV, edgeBelowRelease), nil
}

// compatSplitRegexp mirrors pypa/packaging 26.2's `_prefix_regex`, which
// version.versionSplit also uses: it puts an implicit dot between a release
// segment and a pre-release marker written without a separator, so "0rc1"
// splits into "0" and "rc1".
//
// ⚠️ It lists `c` while compatIsReleaseSegment below does NOT. That gap is
// upstream's, it is what makes `~=1.0c1` narrower than `~=1.0rc1`, and it is
// reproduced here on purpose.
var compatSplitRegexp = regexp.MustCompile(`^([0-9]+)((?:a|b|c|rc)[0-9]+)$`)

// compatVersionSplit reproduces pypa/packaging 26.2's `_version_split`, which
// gpp ports as versionSplit: the epoch (or "0") first, then the dot-separated
// pieces of the rest, with compatSplitRegexp applied to each.
//
// ⚠️ IT OPERATES ON RAW OPERAND TEXT, NOT ON A PARSED VERSION. That is the
// whole point -- see compatibleUpperBound.
func compatVersionSplit(s string) []string {
	epoch, rest := "0", s
	if i := strings.LastIndex(s, "!"); i >= 0 {
		if s[:i] != "" {
			epoch = s[:i]
		}
		rest = s[i+1:]
	}
	out := []string{epoch}
	for _, item := range strings.Split(rest, ".") {
		if m := compatSplitRegexp.FindStringSubmatch(item); m != nil {
			out = append(out, m[1:]...)
		} else {
			out = append(out, item)
		}
	}
	return out
}

// compatIsReleaseSegment reproduces pypa/packaging 26.2's `_is_not_suffix`: a
// piece counts as part of the release unless it STARTS WITH one of five
// literal, lower-case markers.
//
// ⚠️ THE LIST IS EXACTLY THESE FIVE AND THE TEST IS CASE-SENSITIVE. Do not
// "complete" it with the PEP 440 aliases and do not fold case. `c`, `pre`,
// `preview`, `r` and `rev` are all legal spellings of a pre- or post-release,
// and every one of them is treated as RELEASE here, which is why `~=1.0.pre1`
// derives the prefix 1.0.* while `~=1.0rc1` derives 1.*. Case-sensitivity is
// why `~=0.0.posT` -- accepted by the case-insensitive specifier grammar, and
// parsed as 0.0.post0 -- derives 0.0.* while `~=0.0.post0` derives 0.*.
//
// Those look like defects, and adding the aliases or folding case looks like
// the fix. It is not: pypa/packaging 26.2 and version.Specifiers.Check both
// answer exactly as this list dictates, measured on every case above. Matching
// the reference is the requirement; "more consistent than the reference" is a
// different, wrong answer.
func compatIsReleaseSegment(seg string) bool {
	for _, marker := range []string{"dev", "a", "b", "rc", "post"} {
		if strings.HasPrefix(seg, marker) {
			return false
		}
	}
	return true
}

// compatibleUpperBound derives the exclusive upper bound of `~=operand`.
//
// `~=X` is `>=X` conjoined with `==P.*`, where P is the operand's leading run
// of release pieces MINUS its last one. This function returns the top of the
// `==P.*` region; fromSpecifier supplies the `>=X` bottom.
//
// ⚠️ P COMES FROM THE RAW OPERAND TEXT, NOT FROM THE PARSED VERSION.
//
// Deriving it from v.BaseVersion() -- the obvious, structural reading of PEP
// 440, and what this function used to do -- is measurably wrong. Upstream
// splits the operand as written, keeps pieces while compatIsReleaseSegment
// holds, and drops the last one kept; normalization happens only afterwards,
// when the derived `==P.*` is evaluated. Because the suffix test misses the
// alias spellings, a pre-release written `1.0c1`, `1.0.c1`, `1.0.pre1`,
// `1.0.preview1`, `1.0.r1` or `1.0.rev1` stays INSIDE the release run and
// consumes the segment that would otherwise have been dropped: `~=1.0c1` is
// `>=1.0c1,==1.0.*` and rejects 1.1, while `~=1.0rc1` is `>=1.0rc1,==1.*` and
// accepts it. The structural reading accepted 1.1 for all seven spellings.
//
// ok=false means the derived P does not parse as a version, so `==P.*` matches
// nothing at all and the whole specifier is empty. Upstream reaches the same
// answer by a different route (its equality comparison returns false when the
// operand will not parse), and `~=v1.0` is the reachable case: P is "0!v1",
// which is not a version, because PEP 440 puts the optional `v` before the
// epoch and not after it.
func compatibleUpperBound(operand string) (hi bound, ok bool, err error) {
	var kept []string
	for _, seg := range compatVersionSplit(operand) {
		if !compatIsReleaseSegment(seg) {
			break
		}
		kept = append(kept, seg)
	}
	// kept[0] is the epoch, so fewer than two pieces leaves nothing to drop.
	if len(kept) < 2 {
		return hi, false, nil
	}
	prefix := kept[0] + "!" + strings.Join(kept[1:len(kept)-1], ".")

	prefixV, perr := version.Parse(prefix)
	if perr != nil {
		return hi, false, nil
	}

	switch {
	case !prefixV.IsPreRelease() && !prefixV.IsPostRelease() && prefixV.Local() == "":
		// The ordinary case: P is a plain release, so `==P.*` is the run of
		// release groups starting at P.
		nextV, err := incrementLastSegment(prefixV.BaseVersion())
		if err != nil {
			return hi, false, err
		}
		return newBound(nextV, edgeBelowRelease), true, nil

	case preSuffixRegexp.MatchString(prefixV.Public()) && prefixV.Local() == "":
		// P is a pre-release, reachable when the operand spells BOTH its pre-
		// and its post-release with an alias the suffix test misses, as in
		// `~=1.0.pre1.r1` (>=1.0rc1.post1, ==1.0rc1.*). `==P.*` is then every
		// version carrying that exact pre-release marker, with any post, dev or
		// local part -- a contiguous band whose top is the first version of the
		// NEXT pre-release, which is exactly the bound `>P` needs.
		b, err := greaterThanBound(prefixV)
		if err != nil {
			return hi, false, err
		}
		return b, true, nil

	default:
		// P carries a post, dev or local part. The specifier grammar admits no
		// such operand (nothing may follow a post but a dev, and a dev ends the
		// release run), so this is unreachable rather than approximated.
		return hi, false, fmt.Errorf(
			"%w: ~=%s derives the prefix %q", ErrUnrepresentable, operand, prefix)
	}
}

// incrementLastSegment turns "1.2" into version 1.3, preserving any epoch.
//
// ⚠️ The arithmetic is done on the DIGIT STRING, not through strconv. A
// release segment has no upper bound in PEP 440 and gpp holds it as a
// part.BigInt, so `==99999999999999999999.*` is a specifier a real index can
// carry; strconv.Atoi refused it and turned a representable set into an error.
func incrementLastSegment(s string) (version.Version, error) {
	epochPrefix := ""
	rest := s
	if i := strings.Index(s, "!"); i >= 0 {
		epochPrefix, rest = s[:i+1], s[i+1:]
	}
	parts := strings.Split(rest, ".")
	last := parts[len(parts)-1]
	if !isDigits(last) {
		return version.Version{}, fmt.Errorf(
			"pep440set: release segment %q is not numeric", last)
	}
	parts[len(parts)-1] = incDigits(last)
	return version.Parse(epochPrefix + strings.Join(parts, "."))
}

// incDigits adds one to a decimal digit string of any length.
func incDigits(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}
