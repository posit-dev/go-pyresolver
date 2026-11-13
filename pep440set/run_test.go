// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

var runVersionPool = []string{
	"0.1", "1.0.dev1", "1.0a1", "1.0rc1", "1.0", "1.0+local", "1.0.post1", "1.1",
	"1.1.dev1", "2.0", "2.0+a", "2.0+b", "3.0rc1", "3.0",
}

func vp(s string) *version.Version {
	v := version.MustParse(s)
	return &v
}

// Run must agree with plain version order for every bound pair, local
// versions and pre-releases included, and with no pre-release guard on hi.
func TestRunAgreesWithVersionOrder(t *testing.T) {
	bounds := append([]*version.Version{nil}, func() []*version.Version {
		var out []*version.Version
		for _, s := range runVersionPool {
			out = append(out, vp(s))
		}
		return out
	}()...)
	for _, lo := range bounds {
		for _, hi := range bounds {
			run := Run(lo, hi)
			for _, s := range runVersionPool {
				w := version.MustParse(s)
				want := (lo == nil || lo.Compare(w) <= 0) && (hi == nil || w.Compare(*hi) < 0)
				if got := run.Contains(w); got != want {
					t.Errorf("Run(%v, %v).Contains(%s) = %v, want %v", lo, hi, s, got, want)
				}
			}
		}
	}
}

func TestRunEmptyWhenBoundsMeetOrCross(t *testing.T) {
	for _, c := range [][2]string{{"1.0", "1.0"}, {"2.0", "1.0"}, {"1.0+local", "1.0"}} {
		if !Run(vp(c[0]), vp(c[1])).IsEmpty() {
			t.Errorf("Run(%s, %s) should be empty", c[0], c[1])
		}
	}
}

func TestRunOpenEnds(t *testing.T) {
	if !Run(nil, nil).Contains(version.MustParse("0.0.1")) || !Run(nil, nil).Contains(version.MustParse("99")) {
		t.Error("Run(nil, nil) should hold every version")
	}
	if Run(vp("1.0"), nil).Contains(version.MustParse("0.9")) || !Run(vp("1.0"), nil).Contains(version.MustParse("1.0")) {
		t.Error("Run(lo, nil) should start at lo")
	}
	if !Run(nil, vp("1.0")).Contains(version.MustParse("0.9")) || Run(nil, vp("1.0")).Contains(version.MustParse("1.0")) {
		t.Error("Run(nil, hi) should end before hi")
	}
}
