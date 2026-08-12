// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/requirement"
)

func TestEnabledPrereleasesFromSpecifiers(t *testing.T) {
	cases := []struct {
		req  string
		want bool
	}{
		{"flask>=1.0", false},
		{"flask>=1.0rc1", true},
		{"flask==2.0rc1", true},
		{"flask>=1.0,<2.0b1", true},
		{"flask~=2.2", false},
		{"flask!=1.0a1", false}, // != does not enable, matching pypa
		{"flask==1.*", false},
	}
	for _, tc := range cases {
		r, err := requirement.Parse(tc.req)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.req, err)
		}
		got := EnabledPrereleases([]requirement.Requirement{r}, nil)
		if got["flask"] != tc.want {
			t.Errorf("%s: enabled = %v, want %v", tc.req, got["flask"], tc.want)
		}
	}
}

func TestEnabledPrereleasesExplicitAllow(t *testing.T) {
	r, err := requirement.Parse("flask>=1.0")
	if err != nil {
		t.Fatal(err)
	}
	got := EnabledPrereleases([]requirement.Requirement{r}, []index.PackageName{"flask"})
	if !got["flask"] {
		t.Error("an explicitly allowed package must be enabled even when no specifier names a pre-release")
	}
}

func TestEnabledPrereleasesCanonicalizesNames(t *testing.T) {
	r, err := requirement.Parse("Flask_Login>=1.0rc1")
	if err != nil {
		t.Fatal(err)
	}
	got := EnabledPrereleases([]requirement.Requirement{r}, nil)
	if !got["flask-login"] {
		t.Errorf("name was not PEP 503 canonicalized: %v", got)
	}
}

func TestAdmits(t *testing.T) {
	s := PrereleaseSet{"flask": true}
	if !s.Admits("flask", mustV(t, "2.0rc1")) {
		t.Error("enabled package should admit a pre-release")
	}
	if s.Admits("django", mustV(t, "2.0rc1")) {
		t.Error("non-enabled package should not admit a pre-release")
	}
	if !s.Admits("django", mustV(t, "2.0")) {
		t.Error("a final release is always admitted")
	}
	// A dev release is a pre-release for this purpose.
	if s.Admits("django", mustV(t, "2.0.dev1")) {
		t.Error("non-enabled package should not admit a dev release")
	}
	// A post-release is NOT a pre-release.
	if !s.Admits("django", mustV(t, "2.0.post1")) {
		t.Error("a post-release is not a pre-release and must be admitted")
	}
}
