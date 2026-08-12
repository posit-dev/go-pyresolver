// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import "github.com/posit-dev/go-python-packaging/version"

// Exactly is the set holding just v — and, as PEP 440 requires of `==v`, every
// local variant of it: `==1.0` matches `1.0+ubuntu1`.
func Exactly(v version.Version) Set {
	return newSet(span{
		lo: bound{v: v, edge: edgeAt},
		hi: bound{v: v, edge: edgeAboveLocals},
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
	if sp.lo.edge != edgeAt || sp.hi.edge != edgeAboveLocals {
		return version.Version{}, false
	}
	if cmpBound(bound{v: sp.lo.v, edge: edgeAboveLocals}, sp.hi) != 0 {
		return version.Version{}, false
	}
	return sp.lo.v, true
}
