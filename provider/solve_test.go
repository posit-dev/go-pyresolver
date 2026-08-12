// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// maxRounds bounds every solve in this file.
//
// It is a safety valve, not a tuning knob: go-pubgrub's own documentation notes
// that neither prose source proves the outer loop terminates in a bounded
// number of rounds. A design error in this package should surface as a failing
// test, not as a test binary that never returns.
const maxRounds = 10_000

// solve runs a real solve over the given index and requirements.
func solve(t *testing.T, idx index.MetadataIndex, reqs ...string) (map[string]string, *provider.Provider, error) {
	t.Helper()

	opts := testOptions(t)
	opts.Requirements = mustRequirements(t, reqs...)
	p := provider.New(context.Background(), idx, opts)

	s := solver.New(provider.Root(), pep440set.Exactly(mustVersion(t, "0")), p)
	s.MaxRounds = maxRounds

	sol, err := s.Solve()
	if err != nil {
		return nil, p, err
	}

	selected := make(map[string]string, len(sol.Selected))
	for pkg, set := range sol.Selected {
		v, ok := set.Singleton()
		if !ok {
			t.Fatalf("%s was decided as %v, which is not a single version", pkg, set)
		}
		selected[pkg.String()] = v.String()
	}
	return selected, p, nil
}

// assertSelected checks the packages a solution must contain and their
// versions. Packages not named are ignored, so a test can speak only about what
// it means to.
func assertSelected(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for pkg, ver := range want {
		if got[pkg] != ver {
			t.Errorf("%s = %q, want %q (full solution: %v)", pkg, got[pkg], ver, got)
		}
	}
}

func TestSolveTrivial(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0", "werkzeug>=2.0").
		AddVersion("flask", "3.0", "werkzeug>=3.0").
		AddVersion("werkzeug", "2.1").
		AddVersion("werkzeug", "3.0.1")

	got, _, err := solve(t, idx, "flask>=2.0")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertSelected(t, got, map[string]string{
		"flask":    "3.0",
		"werkzeug": "3.0.1",
		"python":   "3.11.4",
	})
}

// The conflict PubGrub exists for: the newest version of one package is
// incompatible with a constraint stated elsewhere, and the resolver has to back
// out of it rather than fail. A resolver that only ever tried the newest of
// everything would report this as unsolvable.
func TestSolveTransitiveMultiVersionConflict(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "1.0", "foo>=1.0").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "0.9").
		AddVersion("foo", "1.0")

	got, _, err := solve(t, idx, "foo>=1.0", "bar")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertSelected(t, got, map[string]string{
		"bar": "1.0",
		"foo": "1.0",
	})
}

// The same shape with no version satisfying both. This must be *Unsolvable --
// a proof carrying a derivation graph -- and not some other error, because
// everything the report package does depends on getting that graph.
func TestSolveUnsolvableCarriesADerivation(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "0.9").
		AddVersion("foo", "1.0")

	_, _, err := solve(t, idx, "foo>=1.0", "bar")

	var unsolvable *solver.Unsolvable[provider.Package, pep440set.Set]
	if !errors.As(err, &unsolvable) {
		t.Fatalf("err = %v, want *solver.Unsolvable", err)
	}
	if unsolvable.RootCause == nil {
		t.Fatal("Unsolvable carries no root cause")
	}
	if !causeMentions(unsolvable.RootCause, func(pkg provider.Package) bool {
		return pkg == provider.Project("foo")
	}) {
		t.Error("the derivation does not mention foo, which is what the conflict is about")
	}
}

// THE payoff for modeling the interpreter as a package. A version whose
// Requires-Python excludes the target could have been filtered out of the
// candidate list instead, which would have made this fail as "no versions of
// flask" -- true in a useless way, and impossible to act on. Because the
// interpreter is a package, the derivation says which Python was demanded.
func TestSolveRequiresPythonMismatchNamesTheInterpreter(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresPython:    mustSpecifiers(t, ">=3.12"),
			RequiresPythonRaw: ">=3.12",
		})

	_, _, err := solve(t, idx, "flask")

	var unsolvable *solver.Unsolvable[provider.Package, pep440set.Set]
	if !errors.As(err, &unsolvable) {
		t.Fatalf("err = %v, want *solver.Unsolvable", err)
	}
	if !causeMentions(unsolvable.RootCause, func(pkg provider.Package) bool {
		return pkg == provider.Python()
	}) {
		t.Error("the derivation does not mention the interpreter, so the report cannot explain " +
			"that the target Python is what ruled flask out")
	}
}

// An extra is only worth modeling if its own requirements reach the solution.
func TestSolveActivatesAnExtra(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, "werkzeug>=3.0", `asgiref>=3.2; extra == "async"`),
			ProvidesExtra: []string{"async"},
		}).
		AddVersion("werkzeug", "3.0.1").
		AddVersion("asgiref", "3.7")

	got, _, err := solve(t, idx, "flask[async]")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertSelected(t, got, map[string]string{
		"flask":        "3.0",
		"flask[async]": "3.0",
		"werkzeug":     "3.0.1",
		"asgiref":      "3.7",
	})
}

// Without the extra, its requirement must stay out of the solution -- the
// symmetric half of the test above, and the one that would catch an
// implementation that activated every extra a package declares.
func TestSolveWithoutTheExtraLeavesItsRequirementOut(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, "werkzeug>=3.0", `asgiref>=3.2; extra == "async"`),
			ProvidesExtra: []string{"async"},
		}).
		AddVersion("werkzeug", "3.0.1").
		AddVersion("asgiref", "3.7")

	got, _, err := solve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if _, ok := got["asgiref"]; ok {
		t.Errorf("asgiref is in the solution without the extra that requires it: %v", got)
	}
	if _, ok := got["flask[async]"]; ok {
		t.Errorf("the extra package is in the solution without being asked for: %v", got)
	}
}

// A misspelled extra must fail the resolve rather than install nothing and
// report success. This is the end-to-end form of the ProvidesExtra check.
func TestSolveMisspelledExtraFails(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{ProvidesExtra: []string{"async"}})

	_, _, err := solve(t, idx, "flask[asynk]")

	var unsolvable *solver.Unsolvable[provider.Package, pep440set.Set]
	if !errors.As(err, &unsolvable) {
		t.Fatalf("err = %v, want *solver.Unsolvable", err)
	}
}

// An sdist-only newest version must not sink the resolve: the solver falls back
// to the newest version it can actually read, and the provider keeps the reason
// so a later report can explain what it skipped.
func TestSolveSkipsAnSdistOnlyVersionAndRecordsWhy(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	got, p, err := solve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	assertSelected(t, got, map[string]string{"flask": "2.0"})

	if len(p.Unusable()) != 1 {
		t.Fatalf("recorded %v, want exactly one reason", p.Unusable())
	}
}

// causeMentions walks the derivation graph from inc, reporting whether any
// incompatibility in it names a package matching want.
//
// The root cause itself is §7.4's terminal incompatibility -- a lone term about
// the root package -- so the packages that actually explain a failure are found
// by following its causes.
func causeMentions(inc *solver.Incompatibility[provider.Package, pep440set.Set], want func(provider.Package) bool) bool {
	if inc == nil {
		return false
	}
	for _, pkg := range inc.Packages() {
		if want(pkg) {
			return true
		}
	}
	a, b, derived := inc.Causes()
	if !derived {
		return false
	}
	return causeMentions(a, want) || causeMentions(b, want)
}
