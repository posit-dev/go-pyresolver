// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
)

func TestPrereleaseIfNoFinal(t *testing.T) {
	mixed := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1")
	onlyPre := index.NewMockIndex("test").
		AddVersion("beta", "1.0b1").
		AddVersion("beta", "1.0b2")

	cases := []struct {
		name    string
		idx     *index.MockIndex
		pkg     index.PackageName
		enabled candidate.PrereleaseSet
		from    string // range start, above every final of the name
		flag    bool
		wantVer string // "" means not found
	}{
		{"finals exist, not enabled: pre-release is not a candidate", mixed, "flask", nil, "1.5", true, ""},
		{"same, option off: in-range fallback offers it", mixed, "flask", nil, "1.5", false, "2.0rc1"},
		{"a requirement names a pre-release: admitted", mixed, "flask", candidate.PrereleaseSet{"flask": true}, "1.5", true, "2.0rc1"},
		{"only pre-releases: still resolvable", onlyPre, "beta", nil, "1.0b1", true, "1.0b2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Prereleases = tc.enabled
			opts.PrereleaseIfNoFinal = tc.flag
			p := provider.New(context.Background(), tc.idx, opts)

			best, found, _, err := p.Candidates(provider.Project(tc.pkg), atLeast(t, tc.from))
			if err != nil {
				t.Fatalf("Candidates: %v", err)
			}
			if tc.wantVer == "" {
				if found {
					t.Fatalf("found %s, want nothing", bestVersion(t, best))
				}
				return
			}
			if !found {
				t.Fatalf("found = false, want %s", tc.wantVer)
			}
			if got := bestVersion(t, best); got.String() != tc.wantVer {
				t.Errorf("best = %s, want %s", got, tc.wantVer)
			}
		})
	}
}
