// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// verPos is the position at(v), held on the caller's stack for the duration of
// one containment test instead of being materialized as a bound.
//
// A bound's posKey is built eagerly because it is long-lived and SHARED: it
// rides along every copy of the bound through the set algebra, so deriving it
// lazily would be a data race. A verPos is neither -- Contains builds one,
// probes each span with it, and drops it -- so it can defer the expensive part
// of the derivation, the public-spelling render (and, for a version carrying a
// local label, the re-parse), until a comparison actually descends into a
// release group. Cross-group comparisons, the common case when probing a span
// whose bounds anchor other releases, never pay it.
//
// ⚠️ DO NOT COPY a verPos after init and DO NOT SHARE ONE ACROSS GOROUTINES.
// ensurePub mutates it in place; that is safe precisely because exactly one
// containment test owns it.
type verPos struct {
	v       version.Version
	epoch   string
	release []string
	// public, pub and pubOK mirror posKey's fields, filled by ensurePub.
	// pubDone marks them filled, so an empty public spelling (the zero
	// Version) is not re-derived per comparison.
	public  string
	pub     version.Version
	pubOK   bool
	pubDone bool
}

// init derives the group key, which every comparison needs first. The public
// spelling waits for ensurePub.
//
// It clears the whole struct before deriving, so re-initializing a used verPos
// cannot carry the previous version's public spelling into the next
// comparison. No current caller re-inits, but the name promises it works, and
// the hoist-one-out-of-the-loop micro-optimization that would start re-initing
// is exactly the kind of change that skips re-reading this file.
func (p *verPos) init(v version.Version) {
	*p = verPos{v: v}
	p.epoch, p.release = releaseKey(v)
}

// ensurePub fills the public-version fields, exactly as newPosKey does.
func (p *verPos) ensurePub() {
	if p.pubDone {
		return
	}
	p.pubDone = true
	p.public = p.v.Public()
	if p.public == "" {
		// An uninitialized Version: the public comparison stays out, exactly
		// as it does for a posKey.
		return
	}
	if p.v.Local() == "" {
		p.pub, p.pubOK = p.v, true
		return
	}
	if pub, err := version.Parse(p.public); err == nil {
		p.pub, p.pubOK = pub, true
	}
}

// cmpVerBound is cmpBound(atBound(p.v), b) without building the bound: the
// same decision ladder with the left side specialized to edgeAt (tier 1, never
// edgeAboveLocals). TestCmpVerBoundAgreesWithCmpBound holds the two together
// over the full ordering grid; a change to either ladder must keep that green.
func cmpVerBound(p *verPos, b bound) int {
	// at(v) sits above -inf and below +inf.
	if b.inf != 0 {
		return -int(b.inf)
	}

	bk := b.pos()
	if c := cmpDigits(p.epoch, bk.epoch); c != 0 {
		return c
	}
	if c := cmpSegments(p.release, bk.release); c != 0 {
		return c
	}

	// at(v) is tier 1: above b when b is the group's floor, below it when b is
	// the group's ceiling.
	switch b.tier() {
	case 0:
		return 1
	case 2:
		return -1
	}

	// Both inside the group; see cmpBound for the ladder this mirrors.
	p.ensurePub()
	if p.public != bk.public && p.pubOK && bk.pubOK {
		if c := p.pub.Compare(bk.pub); c != 0 {
			return c
		}
	}

	// at(v) is never edgeAboveLocals, so only b can be, and it wins.
	if b.edge == edgeAboveLocals {
		return -1
	}

	if c := strings.Compare(p.v.Local(), b.v.Local()); c != 0 {
		return c
	}
	switch {
	case edgeAt < b.edge:
		return -1
	case edgeAt > b.edge:
		return 1
	}
	return 0
}
