// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
)

// go-pubgrub's type parameter P is `comparable`, so a Package that cannot key a
// map does not compile against the solver at all. Asserting it here means the
// failure is a readable test rather than an inscrutable instantiation error in
// whatever file first tries to build a Solver.
func TestPackageIsUsableAsAMapKey(t *testing.T) {
	counts := map[provider.Package]int{}

	counts[provider.Root()]++
	counts[provider.Python()]++
	counts[provider.Project("flask")]++
	counts[provider.Project("flask")]++
	counts[provider.WithExtra("flask", "async")]++

	if got := counts[provider.Project("flask")]; got != 2 {
		t.Errorf("two identical Project keys collapsed to %d entries, want 2 hits on one", got)
	}
	if len(counts) != 4 {
		t.Errorf("distinct packages = %d, want 4: %v", len(counts), counts)
	}
}

// The three kinds must not collide. KindPython exists precisely because
// "python" is a real PyPI project name, so Python() and Project("python") being
// equal would silently merge the interpreter with a package that depends on it.
func TestPackageIdentitiesAreDistinct(t *testing.T) {
	cases := []struct {
		name string
		a, b provider.Package
	}{
		{"root vs python", provider.Root(), provider.Python()},
		{"root vs project", provider.Root(), provider.Project("flask")},
		{"python vs the real PyPI project named python", provider.Python(), provider.Project("python")},
		{"base vs extra", provider.Project("flask"), provider.WithExtra("flask", "async")},
		{"two different extras", provider.WithExtra("flask", "async"), provider.WithExtra("flask", "dotenv")},
		{"same extra of different projects", provider.WithExtra("flask", "async"), provider.WithExtra("django", "async")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.a == tc.b {
				t.Errorf("%v and %v compare equal", tc.a, tc.b)
			}
		})
	}
}

// Names arrive from PEP 508 requirement text, where gpp leaves them exactly as
// parsed. Two spellings of one project must reach the solver as one identity or
// it builds two nodes for one package.
func TestProjectNormalizesItsName(t *testing.T) {
	if provider.Project("Flask_Login") != provider.Project("flask-login") {
		t.Error("Project does not normalize its name per PEP 503")
	}
	if got := provider.Project("Flask_Login").Name; got != index.NewPackageName("Flask_Login") {
		t.Errorf("Name = %q, want %q", got, index.NewPackageName("Flask_Login"))
	}
}

// PEP 685 normalization, on both halves of the key.
func TestWithExtraNormalizesBothHalves(t *testing.T) {
	if provider.WithExtra("flask", "Async_IO") != provider.WithExtra("flask", "async-io") {
		t.Error("WithExtra does not normalize its extra per PEP 685")
	}
	if provider.WithExtra("Flask", "async") != provider.WithExtra("flask", "async") {
		t.Error("WithExtra does not normalize its name per PEP 503")
	}
	if got := provider.WithExtra("flask", "Async_IO").Extra; got != index.NormalizeExtra("Async_IO") {
		t.Errorf("Extra = %q, want %q", got, index.NormalizeExtra("Async_IO"))
	}
}

// String is what the failure report shows a user, so it has to render in the
// form they typed rather than as a struct.
func TestPackageString(t *testing.T) {
	cases := []struct {
		pkg  provider.Package
		want string
	}{
		{provider.Root(), "<root>"},
		{provider.Python(), "python"},
		{provider.Project("Flask"), "flask"},
		{provider.WithExtra("Flask", "Async_IO"), "flask[async-io]"},
	}
	for _, tc := range cases {
		if got := tc.pkg.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

// An empty extra means the base package, not an extra spelled "".
func TestWithExtraOfEmptyStringIsTheBasePackage(t *testing.T) {
	if provider.WithExtra("flask", "") != provider.Project("flask") {
		t.Error("WithExtra with an empty extra must be the base package")
	}
	if got := provider.WithExtra("flask", "").String(); got != "flask" {
		t.Errorf("String() = %q, want %q", got, "flask")
	}
}
