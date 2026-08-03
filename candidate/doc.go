// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package candidate selects which version of a package the resolver should try
// next, and which distribution file represents it.
//
// This is where Python-specific policy lives, kept out of both the generic
// solver and the storage layer: preferring wheels over sdists, honoring
// requires_python against the target interpreter, applying prerelease and
// yanked rules, and ordering versions so the solver tries the most preferred
// one first.
//
// Two policies deliberately do NOT live here:
//
//   - Platform filtering of wheels. Per RFD 0001 Section 6, Files() returns
//     all wheels and the resolver decides, so compatibility-tag matching
//     happens on this side rather than in the index.
//   - Snapshot-date, prerelease, and yanked filtering when it can be applied
//     uniformly. That is FilteredIndex's job in the index package; this
//     package handles the part that depends on the resolution state.
package candidate
