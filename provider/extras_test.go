// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// A requirement with extras becomes a dependency on the base package AND one on
// each extra, all over the same allowed set. Depending on the base as well is
// what keeps the base package's own requirements in the graph: an extra ADDS
// requirements, it does not replace them.
func TestRequirementWithExtrasEmitsBaseAndExtraDependencies(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "flask[async,dotenv]>=2.0")

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("app"), "1.0"))

	want := []provider.Package{
		provider.Project("flask"),
		provider.WithExtra("flask", "async"),
		provider.WithExtra("flask", "dotenv"),
	}
	if len(byPkg) != len(want) {
		t.Fatalf("got %d dependencies, want %d: %v", len(byPkg), len(want), byPkg)
	}
	for _, pkg := range want {
		got, ok := byPkg[pkg]
		if !ok {
			t.Errorf("no dependency on %s", pkg)
			continue
		}
		if !got.Equal(mustSpecifierSet(t, ">=2.0")) {
			t.Errorf("%s allowed = %v, want >=2.0", pkg, got)
		}
	}
}

// The same-version link. Without it the extra could resolve to a version other
// than the base package it is an extra OF, and the installed set would be
// incoherent.
func TestExtraDependsOnItsBaseAtExactlyTheSameVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, "werkzeug>=3.0", `asgiref>=3.2; extra == "async"`),
			ProvidesExtra: []string{"async"},
		})

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.WithExtra("flask", "async"), "3.0.0"))

	got, ok := byPkg[provider.Project("flask")]
	if !ok {
		t.Fatalf("the extra does not depend on its base package: %v", byPkg)
	}
	if !got.Equal(pep440set.Exactly(mustVersion(t, "3.0.0"))) {
		t.Errorf("base link allowed = %v, want exactly 3.0.0", got)
	}

	// The extra's own requirement, which the base package does not have.
	if got, ok := byPkg[provider.Project("asgiref")]; !ok {
		t.Error("the extra-gated requirement is missing from the extra package")
	} else if !got.Equal(mustSpecifierSet(t, ">=3.2")) {
		t.Errorf("asgiref allowed = %v, want >=3.2", got)
	}

	// The base package's unconditional requirements reach the solve through
	// the base package itself, so they are not duplicated onto the extra.
	if _, ok := byPkg[provider.Project("werkzeug")]; ok {
		t.Error("the extra duplicates the base package's unconditional requirement")
	}
}

// PackageMetadata.ProvidesExtra exists precisely so pkg[tests] where the extra
// is spelled test does not resolve happily and install nothing. Asserted
// through Candidates, because found == false is exactly the signal the solver
// reads as "no such thing" and turns into an explanation.
func TestUnknownExtraHasNoCandidates(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			ProvidesExtra: []string{"async"},
		})

	p := provider.New(context.Background(), idx, testOptions(t))

	if _, found, _, err := p.Candidates(provider.WithExtra("flask", "asynk"), pep440set.All()); err != nil || found {
		t.Errorf("misspelled extra: found = %v, err = %v; want false, nil", found, err)
	}
	if _, found, _, err := p.Candidates(provider.WithExtra("flask", "async"), pep440set.All()); err != nil || !found {
		t.Errorf("declared extra: found = %v, err = %v; want true, nil", found, err)
	}
	// The base package is unaffected by either.
	if _, found, _, err := p.Candidates(provider.Project("flask"), pep440set.All()); err != nil || !found {
		t.Errorf("base package: found = %v, err = %v; want true, nil", found, err)
	}
}

// Only the versions that declare the extra are SELECTABLE for it, which is what
// makes "this package has that extra only from 3.0 on" resolvable rather than a
// silent no-op.
//
// ⚠️ Note what rank does and does not say here. Three versions are in range and
// only two provide the extra, and rank reports 3 — it counts what is in range
// before usability is tested, deliberately, because testing usability is the cost
// this provider exists to avoid. Over-counting is what this provider chooses --
// go-pubgrub requires no bound either way -- and this is that gap in action. What
// must still be exact is best (the newest version actually providing the extra)
// and found.
func TestCandidatesForAnExtraSelectOnlyVersionsThatProvideIt(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "2.0", index.PackageMetadata{}).
		SetMetadata("flask", "3.0", index.PackageMetadata{ProvidesExtra: []string{"async"}}).
		SetMetadata("flask", "4.0", index.PackageMetadata{ProvidesExtra: []string{"async"}})

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.WithExtra("flask", "async"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true: 3.0 and 4.0 both provide the extra")
	}
	if got := bestVersion(t, best); got.String() != "4.0" {
		t.Errorf("best = %s, want 4.0 — the newest version that actually provides the extra, "+
			"which is the part that must NOT be approximate", got)
	}
	if rank < 2 {
		t.Errorf("rank = %d, want at least 2: rank may over-count but must never under-count "+
			"the usable versions, or the heuristic would prefer this package over one that "+
			"genuinely has fewer", rank)
	}

	// The base package: all three are selectable, and none is filtered out.
	baseBest, found, _, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil || !found {
		t.Fatalf("base package: found = %v, err = %v; want true, nil", found, err)
	}
	if got := bestVersion(t, baseBest); got.String() != "4.0" {
		t.Errorf("base package best = %s, want 4.0", got)
	}
}

// PEP 685: an extra written as Async_IO in a requirement is the extra the index
// records as async-io. gpp normalizes Requirement.Extras at parse time and
// WithExtra normalizes again, which is idempotent -- neither half is assumed
// from the other, because Requirement.Name is NOT normalized the same way.
func TestExtraNamesNormalize(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "flask[Async_IO]>=2.0").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{ProvidesExtra: []string{"async-io"}})

	p := provider.New(context.Background(), idx, testOptions(t))

	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("app"), "1.0"))
	if _, ok := byPkg[provider.WithExtra("flask", "async-io")]; !ok {
		t.Errorf("no dependency on the normalized extra flask[async-io]: %v", byPkg)
	}

	if _, found, _, err := p.Candidates(provider.WithExtra("flask", "Async_IO"), pep440set.All()); err != nil || !found {
		t.Errorf("found = %v, err = %v; want true, nil", found, err)
	}
}

// MEASURED, and it contradicts what this package was planned against.
//
// The expectation was an asymmetry: provides_extra is PEP 685-normalized in the
// RSF while requires_dist is verbatim, so a publisher's non-normalized
// `extra == "Async_IO"` would never match the normalized extra we pass. That is
// NOT what go-python-packaging v0.5.0 does. It normalizes the literal side of an
// `extra` comparison once at parse time (internal/pep508.normalizeExtraLiteral)
// and the caller-supplied active list at evaluation time, so both sides are
// normalized and the two spellings match.
//
// This test pins that, because the behaviour is load-bearing -- an
// extra-gated requirement silently dropping out would install nothing for the
// extra and report success -- and because it lives in a dependency that could
// change it.
func TestExtraMarkerLiteralIsNormalizedByGpp(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, `asgiref>=3.2; extra == "Async_IO"`),
			ProvidesExtra: []string{"async-io"},
		})

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.WithExtra("flask", "async-io"), "3.0.0"))

	if _, ok := byPkg[provider.Project("asgiref")]; !ok {
		t.Errorf("a non-normalized marker literal did not match the normalized active extra: %v", byPkg)
	}
}

// An extra reaches the interpreter constraint through the same-version link to
// its base, so emitting it a second time would only duplicate an
// incompatibility.
func TestExtraDoesNotRepeatTheInterpreterConstraint(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0.0", index.PackageMetadata{
			RequiresPython:    mustSpecifiers(t, ">=3.8"),
			RequiresPythonRaw: ">=3.8",
			ProvidesExtra:     []string{"async"},
		})

	p := provider.New(context.Background(), idx, testOptions(t))
	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.WithExtra("flask", "async"), "3.0.0"))

	if _, ok := byPkg[provider.Python()]; ok {
		t.Error("the extra package repeats its base package's interpreter constraint")
	}
}
