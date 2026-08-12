// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"strings"
	"testing"

	"github.com/posit-dev/go-pubgrub/report"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// The formatter is what go-pubgrub asks for; if it stops satisfying the
// interface, the failure should be here and not at the one call site.
var _ report.Formatter[provider.Package, pep440set.Set] = pythonFormatter{}

// ⚠️ THE INTERPRETER AND A PROJECT LITERALLY NAMED "python" MUST NOT RENDER
// ALIKE.
//
// "python" is a real project on PyPI, so the obvious implementation -- render
// every package with its own String method -- makes provider.Python() and
// provider.Project("python") both come out as "python". go-pubgrub documents
// that two distinct packages formatting to the same name are ordered
// ARBITRARILY within a sentence, because the ordering that makes a report
// deterministic is over the FORMATTED name. A resolution involving that project
// would then produce a report whose clauses appear in a different order from one
// run to the next.
func TestPackageIsInjectiveForTheInterpreter(t *testing.T) {
	f := pythonFormatter{}
	if got, want := f.Package(provider.Python()), f.Package(provider.Project("python")); got == want {
		t.Fatalf("the interpreter and the PyPI project both render as %q", got)
	}
	if got := f.Package(provider.Python()); got != "Python" {
		t.Errorf("Package(Python()) = %q, want %q", got, "Python")
	}
}

// The whole mapping, not just the one pair the interpreter forces.
func TestPackageIsInjective(t *testing.T) {
	f := pythonFormatter{}
	packages := []provider.Package{
		provider.Root(),
		provider.Python(),
		provider.Project("python"),
		provider.Project("flask"),
		provider.Project("flask-login"),
		provider.WithExtra("flask", "async"),
		provider.WithExtra("flask", "dotenv"),
		provider.WithExtra("flask-login", "async"),
	}
	seen := map[string]provider.Package{}
	for _, pkg := range packages {
		got := f.Package(pkg)
		if prior, dup := seen[got]; dup {
			t.Errorf("%#v and %#v both render as %q", prior, pkg, got)
		}
		seen[got] = pkg
	}
}

func TestPackageRendersExtrasTheWayAUserWritesThem(t *testing.T) {
	f := pythonFormatter{}
	if got, want := f.Package(provider.WithExtra("flask", "async")), "flask[async]"; got != want {
		t.Errorf("Package = %q, want %q", got, want)
	}
	if got, want := f.Package(provider.Project("flask")), "flask"; got != want {
		t.Errorf("Package = %q, want %q", got, want)
	}
}

// The root is synthetic: nothing the user typed corresponds to it, so it must
// not look like a package they could go and pin.
func TestPackageRendersTheRootAsSomethingThatIsNotAPackageName(t *testing.T) {
	got := pythonFormatter{}.Package(provider.Root())
	if got == "" {
		t.Fatal("the root renders as the empty string, which will read as a missing word")
	}
	// A PEP 503-canonical name is lowercase letters, digits, "-", "." and "_".
	// Anything holding a space cannot be mistaken for one.
	if !strings.Contains(got, " ") {
		t.Errorf("the root renders as %q, which a user could mistake for a package name", got)
	}
}

func TestSetRendersTheWayAPythonUserWritesIt(t *testing.T) {
	f := pythonFormatter{}
	for _, tc := range []struct {
		name string
		set  pep440set.Set
		want string
	}{
		{"any", pep440set.All(), "any version"},
		{"none", pep440set.Empty(), "no version"},
		{"exact", pep440set.Exactly(version.MustParse("1.4")), "1.4"},
		{"range", mustSet(t, ">=1.0,<2.0"), ">=1.0,<2.0"},
		{"lower", mustSet(t, ">=1.0"), ">=1.0"},
		{"excluded", mustSet(t, "!=1.0"), "!=1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Set(tc.set); got != tc.want {
				t.Errorf("Set = %q, want %q", got, tc.want)
			}
		})
	}
}

// A set holding one version is what a decision looks like, and it is by far the
// most common thing a report names. "flask 3.0 depends on werkzeug >=3.0" is
// how a Python user says it; "flask ==3.0 depends on ..." is how a struct says
// it.
func TestSetRendersASingleVersionBare(t *testing.T) {
	got := pythonFormatter{}.Set(pep440set.Exactly(version.MustParse("3.11.4")))
	if got != "3.11.4" {
		t.Errorf("Set(Exactly(3.11.4)) = %q, want %q", got, "3.11.4")
	}
}

func mustSet(t *testing.T, spec string) pep440set.Set {
	t.Helper()
	specs, err := version.NewSpecifiers(spec)
	if err != nil {
		t.Fatalf("parse specifiers %q: %v", spec, err)
	}
	s, err := pep440set.FromSpecifiers(specs)
	if err != nil {
		t.Fatalf("convert specifiers %q: %v", spec, err)
	}
	return s
}
