// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package pep440set is the PEP 440 version-set algebra the resolver hands to
// the generic PubGrub solver.
//
// # Positions, not versions
//
// A set is a union of half-open intervals over *positions* rather than over
// versions, because several of PEP 440's own rules need boundaries no version
// can name. `==1.0` matches every local variant of 1.0, and local labels are
// unbounded, so its upper bound is "above every 1.0+local". `>1.0` rejects
// 1.0.post1, and post numbers are unbounded, so its lower bound is "above
// every version in release 1.0". The edge type supplies both, plus the two
// finer positions `==1.0+a` and `<1.0` need.
//
// # Canonicalize on construction
//
// go-pubgrub requires Equal to be true for any two representations of the same
// set: it compares incompatibilities for equality, so keeping [1,2) u [2,3)
// distinct from [1,3) makes the solver fail to recognize facts it has already
// derived, and it can then loop or report a conflict that does not exist.
// Every operation returns through newSet for that reason. Note that 1.0 and
// 1.0.0 are the same version, so merging compares positions, not spellings.
//
// # Equal and IsEmpty answer about positions
//
// Because a set is an interval over positions, both predicates are exact about
// POSITIONS and only approximate about versions, always in the safe direction:
//
//   - Equal can report false for two sets that admit the same versions, when
//     they differ across a gap no version occupies. `<=1.0` unioned with
//     `>=1.0.post0.dev0` admits every version and is still two spans, not
//     All(), because the boundary between them is the position above every
//     1.0+local and the position at 1.0.post0.dev0, with nothing in between.
//   - IsEmpty can report false for a set that holds no version, for the same
//     reason: `>=1.0,!=1.0,<1.0.post0.dev0` is exactly that gap.
//
// Neither is a defect to fix. go-pubgrub compares incompatibilities for
// identity, so Equal must not merge representations it can tell apart, and a
// term that looks satisfiable but yields no candidate costs the solver a
// wasted branch rather than a wrong answer.
//
// # Matching, not selection
//
// FromSpecifiers reproduces version.Specifiers.Check, and Check is pure
// matching. PEP 440's pre-release POLICY -- the rule that an
// installer should not offer 2.0rc1 for `>=1.0` unless asked -- governs which
// candidates to offer, not which versions a specifier matches, so it is not
// applied here; the resolver's candidate layer decides what to offer, and
// applying it in the algebra would make Complement unsound.
//
// Check, not PEP 440's prose, is the specification. Where the two differ this
// package follows Check, which follows pypa/packaging 26.2 -- measured, not
// assumed. `~=` is the operator where it matters: its prefix comes from the raw
// operand TEXT, so `~=1.0c1` and `~=1.0rc1` mean different things despite
// naming the same version. See compatibleUpperBound.
//
// The operator-level pre-release and post-release GUARDS are a different rule
// and are applied, because they are matching: `<1.0` does not match 1.0rc1 and
// `>1.0` does not match 1.0.post1 under any policy.
//
// # Arbitrary equality is not representable
//
// `===` is string equality on the original text, so `===1.0` rejects 1.0.0
// while every set containing 1.0 accepts it. FromSpecifiers reports
// ErrUnrepresentable rather than approximating it. The same is reported for
// version.Specifiers' `||` OR-of-ANDs form, which has no PEP 440 spelling and
// no accessor that would let this package read the groups apart.
package pep440set
