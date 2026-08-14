// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"sync"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// TestContainsConcurrent probes ONE shared Set from several goroutines, with
// versions chosen to reach the pub.Compare arm of cmpVerBound against the
// set's shared posKeys. Run under -race.
//
// ⚠️ WHAT A GREEN RUN HERE DOES AND DOES NOT PROVE. This test demonstrates
// that concurrent Contains calls on a shared Set are race-free ON THIS PATH.
// It does NOT cover the historical upstream hazard -- rstudio/go-version
// v0.0.2's Parts.Padding appending into shared spare capacity in place
// (go-version PR #5, unmerged). Two independent reasons, and the first now
// dominates:
//
//   - gpp no longer calls Parts.Padding AT ALL. compareVersions pads through
//     padParts, into fresh slices. Parts.Padding is still defined in
//     go-version v0.0.2 and has no caller anywhere in the graph, so there is
//     nothing left in this module's dependencies to race on.
//   - On this path it was unreachable even before that: reaching pub.Compare
//     requires the two release keys to tie, and version.ReleaseKey strips a
//     trailing segment exactly when Parts.Normalize does (both drop a segment
//     whose big.Int is zero), so a tie means equal segment counts and Padding
//     would have seen a zero-length difference.
//
// Do not read a green run here as evidence about that hazard either way. The
// ".0" probe shapes below are kept because they are what makes this test
// descend to pub.Compare at all, which is the path it does cover.
//
// The probes still end in ".0" (the spare-capacity shape) and share the
// bounds' release group so the ladder genuinely descends to pub.Compare --
// asserted below, not assumed -- which keeps this test honest about the path
// it exercises and lets it catch any FUTURE change that makes padding
// reachable from here.
//
// The exposure is NOT new to the verPos path: containsBound reached the same
// Compare through cmpBound with the same shared posKey on the bound side. Both
// paths are exercised so a race in either is attributed correctly.
func TestContainsConcurrent(t *testing.T) {
	ss, err := version.NewSpecifiers(">=1.0.0,!=1.2.0,<2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := FromSpecifiers(ss)
	if err != nil {
		t.Fatal(err)
	}

	probes := make([]version.Version, 0, 4)
	for _, spelling := range []string{
		// Same release group as the 1.0.0 and 2.0.0 anchors after
		// trailing-zero stripping, so the group key ties and the ladder
		// descends; the post/dev parts make the public spellings differ,
		// which is what routes it into pub.Compare.
		"1.0.0.post1", "1.0.0.dev0", "2.0.0.dev0", "1.2.0.post1",
	} {
		probes = append(probes, mustV(t, spelling))
	}

	// The test's value depends on at least one probe actually descending to
	// the pub.Compare arm -- group key tied, tier 1, public spellings
	// differing, both parseable. Assert that rather than trusting the comment
	// above: a change to the bound shapes FromSpecifiers builds could
	// otherwise leave every probe stopping at the group key, and this test
	// covering nothing while staying green.
	reached := false
	for _, v := range probes {
		var p verPos
		p.init(v)
		p.ensurePub()
		for _, sp := range s.spans {
			for _, b := range []bound{sp.lo, sp.hi} {
				if b.inf != 0 {
					continue
				}
				bk := b.pos()
				if p.rel.Compare(bk.rel) != 0 || b.tier() != 1 {
					continue
				}
				if p.public != bk.public && p.pubOK && bk.pubOK {
					reached = true
				}
			}
		}
	}
	if !reached {
		t.Fatal("no (probe, bound) pair reaches the pub.Compare arm; " +
			"the fixture has drifted from the shape this test exists for")
	}

	want := make([]bool, len(probes))
	for i, v := range probes {
		want[i] = s.containsBound(atBound(v))
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; iter < 500; iter++ {
				for i, v := range probes {
					if got := s.Contains(v); got != want[i] {
						t.Errorf("Contains(%s) = %v, want %v", v.String(), got, want[i])
						return
					}
					if got := s.containsBound(atBound(v)); got != want[i] {
						t.Errorf("containsBound(at(%s)) = %v, want %v", v.String(), got, want[i])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}
