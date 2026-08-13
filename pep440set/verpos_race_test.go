// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"sync"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// TestContainsConcurrent probes ONE shared Set from several goroutines, with
// versions chosen to reach the pub.Compare arm of cmpVerBound against the
// set's shared posKeys.
//
// Run under -race. The hazard it watches for is upstream: rstudio/go-version
// v0.0.2's Parts.Normalize reslices a release's trailing zeros off, leaving
// len < cap, and Parts.Padding appends into that spare capacity IN PLACE, so
// two goroutines comparing against copies that share the backing array can
// race. Every version below therefore ends in ".0" (the spare-capacity shape,
// same reasoning as resolver/concurrency_test.go), and the probes share the
// bounds' release group so the comparison actually descends to pub.Compare
// rather than stopping at the group key.
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
				if cmpDigits(p.epoch, bk.epoch) != 0 ||
					cmpSegments(p.release, bk.release) != 0 ||
					b.tier() != 1 {
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
