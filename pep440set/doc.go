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
// # Matching, not selection
//
// FromSpecifiers agrees with version.Specifiers.Check on every version, and
// Check is pure matching. PEP 440's pre-release POLICY -- the rule that an
// installer should not offer 2.0rc1 for `>=1.0` unless asked -- governs which
// candidates to offer, not which versions a specifier matches, so it is not
// applied here; the resolver's candidate layer decides what to offer, and
// applying it in the algebra would make Complement unsound.
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
