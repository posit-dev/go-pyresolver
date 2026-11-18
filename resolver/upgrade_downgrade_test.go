// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// passCandidate is one answer an upgrade re-solve found.
type passCandidate struct {
	pins map[string]string
	kept bool
}

// resolveCandidates resolves with the pass on and returns the final pins and
// every candidate the pass scored.
func resolveCandidates(t *testing.T, idx index.MetadataIndex, reqs ...string) (map[string]string, []passCandidate) {
	t.Helper()
	var seen []passCandidate
	defer resolver.OnCandidate(func(got *resolver.Resolution, kept bool) {
		seen = append(seen, passCandidate{pins(t, got), kept})
	})()
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pins(t, res), seen
}

func TestUpgradeRootsRootMovingDown(t *testing.T) {
	cases := []struct {
		name string
		idx  *index.MockIndex
		reqs []string
		base map[string]string // the answer with the pass off
		cand map[string]string // a candidate the pass must score
		kept bool              // whether the pass keeps cand
	}{
		{
			// #106: n can rise a major only if a leaves its final for an
			// older pre-release.
			name: "final to an older pre-release is not kept",
			idx: index.NewMockIndex("test").
				AddVersion("a", "1.0rc1").
				AddVersion("a", "1.0", "n<2").AddVersion("a", "1.1", "n<2").
				AddVersion("n", "1.0").AddVersion("n", "1.1").
				AddVersion("n", "1.2").AddVersion("n", "2.0"),
			reqs: []string{"a==1.*", "n"},
			base: map[string]string{"a": "1.1", "n": "1.2"},
			cand: map[string]string{"a": "1.0rc1", "n": "2.0"},
			kept: false,
		},
		{
			// The pre-release is newer than the final, and two majors up.
			name: "final to a newer pre-release is not kept",
			idx: index.NewMockIndex("test").
				AddVersion("a", "1.0", "n<2").AddVersion("a", "3.0rc1").
				AddVersion("n", "1.0").AddVersion("n", "2.0"),
			reqs: []string{"a", "n"},
			base: map[string]string{"a": "1.0", "n": "1.0"},
			cand: map[string]string{"a": "3.0rc1", "n": "2.0"},
			kept: false,
		},
		{
			// A root may still give up a final for an older final when the
			// roots gain more levels than they lose.
			name: "final to an older final is kept on a net gain",
			idx: index.NewMockIndex("test").
				AddVersion("a", "1.0").
				AddVersion("a", "1.1", "n<2").
				AddVersion("n", "1.0").AddVersion("n", "1.1").AddVersion("n", "2.0"),
			reqs: []string{"a", "n"},
			base: map[string]string{"a": "1.1", "n": "1.1"},
			cand: map[string]string{"a": "1.0", "n": "2.0"},
			kept: true,
		},
		{
			name: "pre-release to a newer pre-release is kept",
			idx: index.NewMockIndex("test").
				AddVersion("p", "1.0rc1").
				AddVersion("p", "2.0rc1", "q<2").AddVersion("p", "3.0rc1", "q<2").
				AddVersion("q", "1.0").AddVersion("q", "2.0"),
			reqs: []string{"p", "q"},
			base: map[string]string{"p": "1.0rc1", "q": "2.0"},
			cand: map[string]string{"p": "3.0rc1", "q": "1.0"},
			kept: true,
		},
		{
			name: "pre-release to a newer final is kept",
			idx: index.NewMockIndex("test").
				AddVersion("p", "1.0rc1").
				AddVersion("p", "2.0", "q<2").AddVersion("p", "3.0", "q<2").
				AddVersion("q", "1.0").AddVersion("q", "2.0"),
			reqs: []string{"p", "q"},
			base: map[string]string{"p": "1.0rc1", "q": "2.0"},
			cand: map[string]string{"p": "3.0", "q": "1.0"},
			kept: true,
		},
		{
			// c is not a root, so its drop costs nothing.
			name: "a package that is not a root may move down",
			idx: index.NewMockIndex("test").
				AddVersion("a", "1.0").
				AddVersion("a", "2.0", "c<2").AddVersion("a", "3.0", "c<2").
				AddVersion("b", "1.0", "c").
				AddVersion("c", "1.0").AddVersion("c", "2.0"),
			reqs: []string{"a", "b"},
			base: map[string]string{"a": "1.0", "b": "1.0", "c": "2.0"},
			cand: map[string]string{"a": "3.0", "b": "1.0", "c": "1.0"},
			kept: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveUp(t, 0, tc.idx, tc.reqs...); !reflect.DeepEqual(got, tc.base) {
				t.Fatalf("pass off: %v, want %v", got, tc.base)
			}
			got, seen := resolveCandidates(t, tc.idx, tc.reqs...)
			found := false
			for _, c := range seen {
				if reflect.DeepEqual(c.pins, tc.cand) {
					found = true
					if c.kept != tc.kept {
						t.Errorf("candidate %v: kept = %v, want %v", c.pins, c.kept, tc.kept)
					}
				}
			}
			if !found {
				t.Fatalf("the pass never scored %v; it scored %v", tc.cand, seen)
			}
			want := tc.base
			if tc.kept {
				want = tc.cand
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("pass on: %v, want %v", got, want)
			}
		})
	}
}
