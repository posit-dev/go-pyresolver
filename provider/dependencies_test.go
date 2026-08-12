// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"strings"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// mustRequirements parses PEP 508 requirement strings for test setup.
func mustRequirements(t *testing.T, raw ...string) []requirement.Requirement {
	t.Helper()
	out := make([]requirement.Requirement, 0, len(raw))
	for _, s := range raw {
		r, err := requirement.Parse(s)
		if err != nil {
			t.Fatalf("parse requirement %q: %v", s, err)
		}
		out = append(out, r)
	}
	return out
}

// mustSpecifierSet converts a specifier string such as ">=3.0" into the set a
// dependency on it should carry.
func mustSpecifierSet(t *testing.T, spec string) pep440set.Set {
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

// depsByPackage indexes a dependency list for assertion, failing on a duplicate
// -- two dependencies on one package would be a bug this helper must not hide.
func depsByPackage(t *testing.T, deps []solver.Dependency[provider.Package, pep440set.Set]) map[provider.Package]pep440set.Set {
	t.Helper()
	out := make(map[provider.Package]pep440set.Set, len(deps))
	for _, d := range deps {
		if _, dup := out[d.Package]; dup {
			t.Fatalf("duplicate dependency on %s", d.Package)
		}
		out[d.Package] = d.Allowed
	}
	return out
}

// dependenciesOf is the common call: Dependencies for one concrete version.
func dependenciesOf(t *testing.T, p *provider.Provider, pkg provider.Package, ver string) []solver.Dependency[provider.Package, pep440set.Set] {
	t.Helper()
	deps, err := p.Dependencies(pkg, pep440set.Exactly(mustVersion(t, ver)))
	if err != nil {
		t.Fatalf("Dependencies(%s, %s): %v", pkg, ver, err)
	}
	return deps
}

func TestDependenciesEmitsRequirementsWithTheirAllowedSets(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "3.0.0", "werkzeug>=3.0", "jinja2")

	p := provider.New(context.Background(), idx, testOptions(t))
	deps := dependenciesOf(t, p, provider.Project("flask"), "3.0.0")

	byPkg := depsByPackage(t, deps)
	if len(byPkg) != 2 {
		t.Fatalf("got %d dependencies, want 2: %v", len(byPkg), byPkg)
	}
	if got, ok := byPkg[provider.Project("werkzeug")]; !ok {
		t.Error("no dependency on werkzeug")
	} else if !got.Equal(mustSpecifierSet(t, ">=3.0")) {
		t.Errorf("werkzeug allowed = %v, want >=3.0", got)
	}
	// A requirement with no specifier allows every version.
	if got, ok := byPkg[provider.Project("jinja2")]; !ok {
		t.Error("no dependency on jinja2")
	} else if !got.Equal(pep440set.All()) {
		t.Errorf("jinja2 allowed = %v, want every version", got)
	}
}

// Requirement.Name is documented as "exactly as parsed" -- gpp does not
// canonicalize it. An un-normalized name reaching the solver builds a second
// node for a project that already has one.
func TestDependenciesCanonicalizesRequirementNames(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "Flask_Login>=0.6")

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("app"), "1.0"))

	if _, ok := byPkg[provider.Project("flask-login")]; !ok {
		t.Errorf("no dependency on the canonical name flask-login: %v", byPkg)
	}
}

func TestDependenciesEvaluatesMarkers(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0",
			`colorama; sys_platform == "win32"`,
			`tomli; python_version < "3.11"`,
			`typing-extensions; python_version >= "3.8"`,
		)

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("app"), "1.0"))

	if _, ok := byPkg[provider.Project("colorama")]; ok {
		t.Error("colorama is gated on win32 and the environment is linux")
	}
	if _, ok := byPkg[provider.Project("tomli")]; ok {
		t.Error("tomli is gated on python_version < 3.11 and the environment is 3.11.4")
	}
	if _, ok := byPkg[provider.Project("typing-extensions")]; !ok {
		t.Error("typing-extensions is gated on python_version >= 3.8, which holds")
	}
}

// A marker mentioning `extra` must be FALSE for the base package: with no
// extras active, extra == "x" is unsatisfied. Getting this wrong pulls every
// optional dependency into every install.
func TestDependenciesOfTheBasePackageExcludesExtraGatedRequirements(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, "werkzeug>=3.0", `asgiref; extra == "async"`),
			ProvidesExtra: []string{"async"},
		})

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("flask"), "3.0.0"))

	if _, ok := byPkg[provider.Project("asgiref")]; ok {
		t.Error("an extra-gated requirement must not be a dependency of the base package")
	}
	if _, ok := byPkg[provider.Project("werkzeug")]; !ok {
		t.Error("the unconditional requirement is missing")
	}
}

func TestDependenciesTranslatesRequiresPython(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			RequiresPython:    mustSpecifiers(t, ">=3.8"),
			RequiresPythonRaw: ">=3.8",
		})

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("flask"), "3.0.0"))

	got, ok := byPkg[provider.Python()]
	if !ok {
		t.Fatalf("no dependency on the interpreter: %v", byPkg)
	}
	if !got.Equal(mustSpecifierSet(t, ">=3.8")) {
		t.Errorf("python allowed = %v, want >=3.8", got)
	}
}

// An absent Requires-Python is over two million versions in a production PyPI
// snapshot. Emitting an unconstrained dependency on the interpreter for every
// one of them is noise in both the graph and the report.
func TestDependenciesOmitsAnAbsentRequiresPython(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "3.0.0")

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("flask"), "3.0.0"))

	if _, ok := byPkg[provider.Python()]; ok {
		t.Error("an absent Requires-Python must not produce an interpreter dependency")
	}
}

// go-pubgrub documents an empty Depender as "only the version being considered",
// which is always correct and only ever costs extra incompatibilities. Anything
// else needs a benchmark first.
func TestDependenciesLeavesDependerEmpty(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "3.0.0", "werkzeug>=3.0")

	p := provider.New(context.Background(), idx, testOptions(t))
	for _, d := range dependenciesOf(t, p, provider.Project("flask"), "3.0.0") {
		if !d.Depender.IsEmpty() {
			t.Errorf("dependency on %s carries a Depender range %v", d.Package, d.Depender)
		}
	}
}

func TestDependenciesOfPythonAreNone(t *testing.T) {
	p := provider.New(context.Background(), index.NewMockIndex("test"), testOptions(t))

	deps, err := p.Dependencies(provider.Python(), pep440set.Exactly(mustVersion(t, "3.11.4")))
	if err != nil {
		t.Fatalf("Dependencies(python): %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("the interpreter has %d dependencies, want 0: %v", len(deps), deps)
	}
}

// The root's dependencies are the user's own requirements, plus the pin that
// makes the interpreter a fact of the resolution rather than a filter applied
// behind the solver's back.
func TestDependenciesOfRootAreTheCallersRequirements(t *testing.T) {
	opts := testOptions(t)
	opts.Requirements = mustRequirements(t, "Flask_Login>=0.6", "requests")
	p := provider.New(context.Background(), index.NewMockIndex("test"), opts)

	deps, err := p.Dependencies(provider.Root(), pep440set.Exactly(mustVersion(t, "0")))
	if err != nil {
		t.Fatalf("Dependencies(root): %v", err)
	}
	byPkg := depsByPackage(t, deps)

	if got, ok := byPkg[provider.Project("flask-login")]; !ok {
		t.Errorf("no dependency on flask-login: %v", byPkg)
	} else if !got.Equal(mustSpecifierSet(t, ">=0.6")) {
		t.Errorf("flask-login allowed = %v, want >=0.6", got)
	}
	if _, ok := byPkg[provider.Project("requests")]; !ok {
		t.Error("no dependency on requests")
	}
	if got, ok := byPkg[provider.Python()]; !ok {
		t.Error("the root does not pin the interpreter")
	} else if !got.Equal(pep440set.Exactly(mustVersion(t, "3.11.4"))) {
		t.Errorf("python pin = %v, want exactly 3.11.4", got)
	}
}

// go-pubgrub only ever asks about a single version. Anything else is a contract
// violation, and guessing which version was meant would produce a resolution
// that silently answers a question nobody asked.
func TestDependenciesRejectsANonSingletonVersion(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "3.0.0")
	p := provider.New(context.Background(), idx, testOptions(t))

	for _, tc := range []struct {
		name string
		set  pep440set.Set
	}{
		{"every version", pep440set.All()},
		{"no version", pep440set.Empty()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Dependencies(provider.Project("flask"), tc.set)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "flask") {
				t.Errorf("error %q does not name the package", err)
			}
		})
	}
}

// A direct-reference requirement (foo @ https://...) carries no version
// specifier, so treating it as "any version" would let the resolver pick an
// index version the publisher never asked for -- a different artifact,
// silently. The depending version is treated as unusable instead; Candidates
// never offers it, so reaching Dependencies with one means the caller went
// around the solver.
func TestDependenciesRefusesADirectReferenceRequirement(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "foo @ https://example.com/foo-1.0-py3-none-any.whl")

	p := provider.New(context.Background(), idx, testOptions(t))

	_, err := p.Dependencies(provider.Project("app"), pep440set.Exactly(mustVersion(t, "1.0")))
	if err == nil {
		t.Fatal("want an error for a direct-reference requirement")
	}
	if !strings.Contains(err.Error(), "https://example.com/foo-1.0-py3-none-any.whl") {
		t.Errorf("error %q does not name the URL", err)
	}
}

// mustSpecifiers parses a specifier string for test setup.
func mustSpecifiers(t *testing.T, s string) version.Specifiers {
	t.Helper()
	specs, err := version.NewSpecifiers(s)
	if err != nil {
		t.Fatalf("parse specifiers %q: %v", s, err)
	}
	return specs
}
