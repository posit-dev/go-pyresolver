// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
	return newSet(span{
		lo: bound{v: v, edge: edgeAt},
		hi: bound{v: v, edge: hi},
	})
}

// Contains reports whether v is in the set.
func (s Set) Contains(v version.Version) bool {
	return s.containsBound(atBound(v))
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
	if cmpBound(bound{v: sp.lo.v, edge: sp.hi.edge}, sp.hi) != 0 {
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
// The result is the intersection of one span set per specifier, and it agrees
// with version.Specifiers.Check on every version: Check is pure MATCHING, and
// so is this. Pre-release exclusion is deliberately NOT applied on top of it,
// because that is a selection rule -- which candidates an installer offers --
// and applying it here would make Complement unsound. The resolver's candidate
// layer decides what to offer.
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
		return newSet(span{bound{v: v, edge: edgeAt}, posInf()}), nil
	case ">":
		lo, err := greaterThanBound(v)
		if err != nil {
			return Set{}, err
		}
		return newSet(span{lo, posInf()}), nil
	case "<=":
		// A local label on the prospective version is ignored, so every local
		// variant of the operand still matches.
		return newSet(span{negInf(), bound{v: v, edge: edgeAboveLocals}}), nil
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
		// ~=X.Y is >=X.Y combined with ==X.*: drop the last RELEASE segment of
		// the operand and increment what remains. Only the release segments
		// take part, so ~=1.0.post1 is >=1.0.post1 with ==1.*, not ==1.0.*.
		hi, err := compatibleUpperBound(v)
		if err != nil {
			return Set{}, err
		}
		return newSet(span{bound{v: v, edge: edgeAt}, hi}), nil
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
		return bound{v: v, edge: edgeAt}, nil
	}
	// The earliest pre-release of v: the same version with dev set to 0. The
	// operand cannot carry a local label, so Public is its whole spelling.
	earliest, err := version.Parse(v.Public() + ".dev0")
	if err != nil {
		return bound{}, fmt.Errorf(
			"pep440set: earliest pre-release of %q: %w", v.Public(), err)
	}
	return bound{v: earliest, edge: edgeAt}, nil
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
		return bound{v: v, edge: edgeAboveLocals}, nil
	case v.IsPreRelease():
		m := preSuffixRegexp.FindStringSubmatch(v.Public())
		if m == nil {
			// A dev release: no post-release of it can exist.
			return bound{v: v, edge: edgeAboveLocals}, nil
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return bound{}, fmt.Errorf(
				"pep440set: pre-release number %q: %w", m[3], err)
		}
		next, err := version.Parse(m[1] + m[2] + strconv.Itoa(n+1) + ".dev0")
		if err != nil {
			return bound{}, fmt.Errorf(
				"pep440set: next pre-release after %q: %w", v.Public(), err)
		}
		return bound{v: next, edge: edgeAt}, nil
	default:
		return bound{v: v, edge: edgeAboveRelease}, nil
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
	return bound{v: loV, edge: edgeBelowRelease},
		bound{v: nextV, edge: edgeBelowRelease}, nil
}

// compatibleUpperBound implements ~=: drop the final RELEASE segment and
// increment the one before it. ~=2.2 -> below release 3; ~=2.2.3 -> below 2.3;
// ~=1.0.post1 -> below release 2, because post1 is not a release segment.
func compatibleUpperBound(v version.Version) (hi bound, err error) {
	base := v.BaseVersion()
	epochPrefix := ""
	rest := base
	if i := strings.Index(base, "!"); i >= 0 {
		epochPrefix, rest = base[:i+1], base[i+1:]
	}
	parts := strings.Split(rest, ".")
	if len(parts) < 2 {
		return hi, fmt.Errorf(
			"pep440set: ~=%s needs at least two release segments", v.String())
	}
	nextV, err := incrementLastSegment(
		epochPrefix + strings.Join(parts[:len(parts)-1], "."))
	if err != nil {
		return hi, err
	}
	return bound{v: nextV, edge: edgeBelowRelease}, nil
}

// incrementLastSegment turns "1.2" into version 1.3, preserving any epoch.
func incrementLastSegment(s string) (version.Version, error) {
	epochPrefix := ""
	rest := s
	if i := strings.Index(s, "!"); i >= 0 {
		epochPrefix, rest = s[:i+1], s[i+1:]
	}
	parts := strings.Split(rest, ".")
	last := parts[len(parts)-1]
	n, err := strconv.Atoi(last)
	if err != nil {
		return version.Version{}, fmt.Errorf(
			"pep440set: release segment %q is not numeric: %w", last, err)
	}
	parts[len(parts)-1] = strconv.Itoa(n + 1)
	return version.Parse(epochPrefix + strings.Join(parts, "."))
}
