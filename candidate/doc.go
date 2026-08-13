// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package candidate decides which versions of a package may be offered to the
// solver, and in what order it should try them.
//
// Two separable things live here, and keeping them separate is the point:
//
//   - Admission is a yes/no about a single version, decided once before
//     solving so it cannot move while the solver backtracks. Today that is the
//     pre-release rule: see PrereleaseSet.
//   - Ranking is a total order over the admissible versions, supplied by the
//     caller through Policy so that an embedder -- Package Manager, say -- can
//     demote versions it would rather not install without making them
//     unavailable. Newest is the default.
//
// # Ranking must never remove a version
//
// Provider.Candidates walks this ranking and stops at the first USABLE version,
// so a version a policy dropped is not merely deprioritized -- it is
// unreachable. A package whose only usable version the policy disliked would be
// reported as having nothing available, indistinguishable from one that does not
// exist, and the resulting failure report would describe a conflict that is not
// the real one. Filtering belongs to the index; admission here is limited to
// facts about the version itself.
//
// # Two policies deliberately do NOT live here
//
// Wheel-versus-sdist preference and compatibility-tag matching are deferred to
// a later issue, not absent by oversight. The PyPI RSF serves no file records
// -- RSFIndex returns index.ErrFilesUnavailable -- so on the primary
// standalone path there is nothing to select between, and a file-selection
// policy written now could not be exercised against real data.
//
// requires_python filtering moved to package provider, which models the target
// interpreter as an ordinary package. An incompatible release is then excluded
// by a derivation the failure report can explain, rather than by silently
// vanishing from the candidate list.
package candidate
