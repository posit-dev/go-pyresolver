// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

func TestPrereleaseIfNoFinalResolve(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1").
		AddVersion("beta", "1.0b1")

	cases := []struct {
		name    string
		req     string
		flag    bool
		want    string // "" means resolution must fail
		wantPkg string
	}{
		// ">1.0" leaves only the pre-release in range.
		{"option off: in-range pre-release is the fallback", "flask>1.0", false, "2.0rc1", "flask"},
		{"option on: the pre-release is not a candidate", "flask>1.0", true, "", "flask"},
		{"option on, a requirement names a pre-release", "flask>=2.0rc1", true, "2.0rc1", "flask"},
		{"option on, a pre-release-only package still resolves", "beta", true, "1.0b1", "beta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.PrereleaseIfNoFinal = tc.flag
			res, err := resolver.Resolve(context.Background(), mustRequirements(t, tc.req), idx, opts)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("resolved %v, want a failure", res.Pinned)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Pinned[index.PackageName(tc.wantPkg)].String(); got != tc.want {
				t.Errorf("%s = %s, want %s", tc.wantPkg, got, tc.want)
			}
		})
	}
}
