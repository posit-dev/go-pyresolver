// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"strconv"
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// edge names a position relative to a version, rather than a version itself.
//
// PEP 440 needs this: local labels and post numbers are unbounded above, so
// neither "the least version above every 1.0+local" nor "the least version
// above every 1.0.postN" exists, yet `==1.0` needs the first as its upper
// bound and `>1.0` needs the second as its lower bound.
type edge uint8

const (
	// edgeBelowRelease is below every version sharing this version's
	// (epoch, release), including its dev and pre-releases.
	edgeBelowRelease edge = iota
	// edgeAt is exactly this version's position in the total order.
	edgeAt
	// edgeAboveLocals is above this version and every local variant of it,
	// and below the next public version (e.g. below 1.0.post0.dev0).
	edgeAboveLocals
	// edgeAboveRelease is above every version sharing this version's
	// (epoch, release), including every post-release of it.
	edgeAboveRelease
)

// bound is a position in the version order. inf is -1, 0 or +1.
type bound struct {
	inf  int8
	v    version.Version
	edge edge
}

func negInf() bound { return bound{inf: -1} }
func posInf() bound { return bound{inf: 1} }

func atBound(v version.Version) bound { return bound{v: v, edge: edgeAt} }

// tier collapses edge into the three-way grouping cmpBound sorts on after the
// release group: below the group, inside it, above it.
func (b bound) tier() int {
	switch b.edge {
	case edgeBelowRelease:
		return 0
	case edgeAboveRelease:
		return 2
	default:
		return 1
	}
}

// releaseKey returns the (epoch, release) of b's version, with trailing zeros
// stripped so 1.0 and 1.0.0.0 share a key. BaseVersion renders "1!3.4.5" for
// an epoch, so the epoch is split off here.
func releaseKey(v version.Version) (epoch int, release []int) {
	base := v.BaseVersion()
	if i := strings.Index(base, "!"); i >= 0 {
		epoch, _ = strconv.Atoi(base[:i])
		base = base[i+1:]
	}
	for _, part := range strings.Split(base, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		release = append(release, n)
	}
	for len(release) > 0 && release[len(release)-1] == 0 {
		release = release[:len(release)-1]
	}
	return epoch, release
}

func cmpInts(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// cmpBound reports whether a is before (-1), at (0) or after (+1) b.
func cmpBound(a, b bound) int {
	if a.inf != 0 || b.inf != 0 {
		switch {
		case a.inf < b.inf:
			return -1
		case a.inf > b.inf:
			return 1
		}
		return 0
	}

	aEpoch, aRel := releaseKey(a.v)
	bEpoch, bRel := releaseKey(b.v)
	switch {
	case aEpoch < bEpoch:
		return -1
	case aEpoch > bEpoch:
		return 1
	}
	if c := cmpInts(aRel, bRel); c != 0 {
		return c
	}

	switch at, bt := a.tier(), b.tier(); {
	case at < bt:
		return -1
	case at > bt:
		return 1
	case at != 1:
		// Both below the same group, or both above it: same position.
		return 0
	}

	// Both inside the group. Compare the public version first, so that
	// aboveLocals(1.0) lands below at(1.0.post0.dev0) ...
	aPub, aErr := version.Parse(a.v.Public())
	bPub, bErr := version.Parse(b.v.Public())
	if aErr == nil && bErr == nil {
		if c := aPub.Compare(bPub); c != 0 {
			return c
		}
	}

	// ... and the edge second, so that aboveLocals(1.0) lands above every
	// at(1.0+local) no matter how the label sorts.
	switch {
	case a.edge < b.edge:
		return -1
	case a.edge > b.edge:
		return 1
	}

	if a.edge == edgeAt {
		return strings.Compare(a.v.Local(), b.v.Local())
	}
	return 0
}
