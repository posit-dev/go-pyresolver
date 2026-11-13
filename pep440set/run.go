// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import "github.com/posit-dev/go-python-packaging/version"

// Run is the set of versions from lo up to, but not including, hi, by position
// in the version order. A nil bound leaves that end open.
//
// It is what go-pubgrub's Dependency.Depender wants: a run of adjacent
// published versions, named by the first version in it and the first one after
// it. Unlike `>=lo,<hi` it applies no pre-release guard to hi, so hi's own
// pre-releases are inside the run when they sort below it.
func Run(lo, hi *version.Version) Set {
	l, h := negInf(), posInf()
	if lo != nil {
		l = newBound(*lo, edgeAt)
	}
	if hi != nil {
		h = newBound(*hi, edgeAt)
	}
	if cmpBound(l, h) >= 0 {
		return Empty()
	}
	return newSet(span{lo: l, hi: h})
}
