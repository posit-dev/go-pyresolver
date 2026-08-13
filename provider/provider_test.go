// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

// testEnv is the marker environment every test in this package resolves
// against: CPython 3.11.4 on linux/x86_64.
//
// Built through EnvironmentFromTarget rather than as a struct literal, because
// a literal zero-fills the ten fields it does not mention -- python_version
// becomes "" and a marker like python_version >= "3.8" silently flips its
// answer. marker.Environment's own documentation calls that out.
func testEnv(t *testing.T) marker.Environment {
	t.Helper()
	env, err := marker.EnvironmentFromTarget(
		tags.Target{Implementation: "cp", PyMajor: 3, PyMinor: 11, OS: "linux", Arch: "x86_64"},
		marker.InterpreterIdentity{
			ImplementationName:           "cpython",
			PlatformPythonImplementation: "CPython",
			PythonFullVersion:            "3.11.4",
			ImplementationVersion:        "3.11.4",
		},
	)
	if err != nil {
		t.Fatalf("build marker environment: %v", err)
	}
	return env
}

// testOptions returns Options wired to testEnv with Python 3.11.4.
func testOptions(t *testing.T) provider.Options {
	t.Helper()
	return provider.Options{
		Environment:   testEnv(t),
		PythonVersion: version.MustParse("3.11.4"),
	}
}

func mustVersion(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatalf("parse version %q: %v", s, err)
	}
	return v
}

// atLeast builds the set a ">=lo" requirement would produce.
func atLeast(t *testing.T, lo string) pep440set.Set {
	t.Helper()
	specs, err := version.NewSpecifiers(">=" + lo)
	if err != nil {
		t.Fatalf("parse specifiers >=%s: %v", lo, err)
	}
	s, err := pep440set.FromSpecifiers(specs)
	if err != nil {
		t.Fatalf("convert specifiers >=%s: %v", lo, err)
	}
	return s
}

// bestVersion extracts the single version Candidates offered, failing the test
// if best is not a singleton -- which go-pubgrub requires it to be.
func bestVersion(t *testing.T, best pep440set.Set) version.Version {
	t.Helper()
	v, ok := best.Singleton()
	if !ok {
		t.Fatalf("best %v is not a single version", best)
	}
	return v
}

func TestCandidatesExcludesVersionsOutsideAllowed(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0").
		AddVersion("flask", "3.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.Project("flask"), atLeast(t, "2.0"))
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if rank != 2 {
		t.Errorf("rank = %d, want 2 (1.0 lies outside the allowed set)", rank)
	}
	if got := bestVersion(t, best); got.String() != "3.0" {
		t.Errorf("best = %s, want 3.0", got)
	}
}

// go-pubgrub rejects a decision outside the accumulated term rather than
// trusting it, because such a decision corrupts the partial solution in a way
// no later error points back to. So the versions outside allowed are discarded
// BEFORE ranking, and this is the assertion that catches a reordering.
//
// ⚠️ Ranking now happens before the usability walk, so this is the assertion
// that catches the range filter being moved after it too — the ranked list must
// already be confined to allowed, or the first usable version out of it can sit
// outside.
func TestCandidatesBestLiesWithinAllowed(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0").
		AddVersion("flask", "9.9")

	// A policy that prefers the HIGHEST version overall would pick 9.9, which
	// is outside the allowed set. Newest plus the allowed filter must not.
	allowed := pep440set.Exactly(mustVersion(t, "2.0")).
		Union(pep440set.Exactly(mustVersion(t, "1.0")))

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.Project("flask"), allowed)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if rank != 2 {
		t.Fatalf("rank = %d, want 2", rank)
	}
	got := bestVersion(t, best)
	if !allowed.Contains(got) {
		t.Errorf("best = %s, which is outside the allowed set %v", got, allowed)
	}
	if got.String() != "2.0" {
		t.Errorf("best = %s, want 2.0", got)
	}
}

func TestCandidatesPrereleaseAdmission(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0rc1")

	t.Run("excluded when the package is not enabled", func(t *testing.T) {
		p := provider.New(context.Background(), idx, testOptions(t))
		best, found, rank, err := p.Candidates(provider.Project("flask"), pep440set.All())
		if err != nil {
			t.Fatalf("Candidates: %v", err)
		}
		if !found {
			t.Fatal("found = false, want true")
		}
		if rank != 1 {
			t.Errorf("rank = %d, want 1 (the release candidate is not admissible, and "+
				"pre-release admission is part of the in-range filter rank counts)", rank)
		}
		if got := bestVersion(t, best); got.String() != "1.0" {
			t.Errorf("best = %s, want 1.0", got)
		}
	})

	t.Run("included when the package is enabled", func(t *testing.T) {
		opts := testOptions(t)
		opts.Prereleases = candidate.PrereleaseSet{"flask": true}
		p := provider.New(context.Background(), idx, opts)

		best, found, rank, err := p.Candidates(provider.Project("flask"), pep440set.All())
		if err != nil {
			t.Fatalf("Candidates: %v", err)
		}
		if !found {
			t.Fatal("found = false, want true")
		}
		if rank != 2 {
			t.Errorf("rank = %d, want 2", rank)
		}
		if got := bestVersion(t, best); got.String() != "2.0rc1" {
			t.Errorf("best = %s, want 2.0rc1", got)
		}
	})
}

// An unknown package is found == false and NO error: the solver reads that as "no
// version of this is available in this range" and explains it through the
// derivation graph. An error would abort the whole resolve over a typo.
func TestCandidatesUnknownPackageIsNotFoundAndNoError(t *testing.T) {
	p := provider.New(context.Background(), index.NewMockIndex("test"), testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("nosuchpkg"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v, want nil", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

func TestCandidatesKnownPackageWithNoAdmissibleVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddPackage("empty").
		AddVersion("flask", "1.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	if _, found, _, err := p.Candidates(provider.Project("empty"), pep440set.All()); err != nil || found {
		t.Errorf("a registered package with no versions: found = %v, err = %v; want false, nil", found, err)
	}
	if _, found, _, err := p.Candidates(provider.Project("flask"), atLeast(t, "2.0")); err != nil || found {
		t.Errorf("no version in range: found = %v, err = %v; want false, nil", found, err)
	}
}

// oldestFirst is a Policy that inverts candidate.Newest.
type oldestFirst struct{}

func (oldestFirst) Less(_ index.PackageName, a, b version.Version) bool { return a.Compare(b) < 0 }

// THE invariant most likely to rot. candidate.Policy ranks and never filters:
// a version a policy dislikes is still a version the solver may use, and
// dropping it would be indistinguishable from the version not existing -- which
// makes the failure report describe a conflict that is not the real one.
//
// ⚠️ This matters MORE now that ranking happens before the usability walk. Rank
// decides the order versions are tried in, so a Policy that filtered would not
// merely reorder preferences: it would make the skipped versions unreachable, and
// a package whose only usable version the Policy disliked would report
// found == false.
func TestPolicyChangesBestButNotRank(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0").
		AddVersion("flask", "3.0")

	newestOpts := testOptions(t)
	newestBest, newestFound, newestRank, err := provider.New(context.Background(), idx, newestOpts).
		Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates (default policy): %v", err)
	}

	oldestOpts := testOptions(t)
	oldestOpts.Policy = oldestFirst{}
	oldestBest, oldestFound, oldestRank, err := provider.New(context.Background(), idx, oldestOpts).
		Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates (oldest-first policy): %v", err)
	}

	if !newestFound || !oldestFound {
		t.Fatalf("found = %v and %v, want true and true: a Policy must not make a version "+
			"unreachable", newestFound, oldestFound)
	}
	if newestRank != 3 || oldestRank != 3 {
		t.Errorf("ranks = %d and %d, want 3 and 3: a Policy must not filter", newestRank, oldestRank)
	}
	if got := bestVersion(t, newestBest); got.String() != "3.0" {
		t.Errorf("default policy best = %s, want 3.0", got)
	}
	if got := bestVersion(t, oldestBest); got.String() != "1.0" {
		t.Errorf("oldest-first policy best = %s, want 1.0", got)
	}
}

// The root and the interpreter each have exactly one version and never touch
// the index -- so a provider with an EMPTY index must still answer for them.
func TestCandidatesForRootAndPython(t *testing.T) {
	opts := testOptions(t)
	opts.RootVersion = version.MustParse("1")
	p := provider.New(context.Background(), index.NewMockIndex("test"), opts)

	best, found, rank, err := p.Candidates(provider.Root(), pep440set.All())
	if err != nil || !found || rank != 1 {
		t.Fatalf("root: found = %v, rank = %d, err = %v; want true, 1, nil", found, rank, err)
	}
	if got := bestVersion(t, best); got.String() != "1" {
		t.Errorf("root best = %s, want 1", got)
	}

	best, found, rank, err = p.Candidates(provider.Python(), pep440set.All())
	if err != nil || !found || rank != 1 {
		t.Fatalf("python: found = %v, rank = %d, err = %v; want true, 1, nil", found, rank, err)
	}
	if got := bestVersion(t, best); got.String() != "3.11.4" {
		t.Errorf("python best = %s, want 3.11.4", got)
	}

	// Outside the allowed set, both are unavailable rather than offered anyway.
	if _, found, _, err := p.Candidates(provider.Python(), atLeast(t, "3.12")); err != nil || found {
		t.Errorf("python outside allowed: found = %v, err = %v; want false, nil", found, err)
	}
	if _, found, _, err := p.Candidates(provider.Root(), atLeast(t, "2")); err != nil || found {
		t.Errorf("root outside allowed: found = %v, err = %v; want false, nil", found, err)
	}
}

// RootVersion defaults rather than leaving the zero version, which would make
// the root's identity depend on whether the caller filled the field in.
func TestRootVersionDefaults(t *testing.T) {
	p := provider.New(context.Background(), index.NewMockIndex("test"), testOptions(t))

	best, found, rank, err := p.Candidates(provider.Root(), pep440set.All())
	if err != nil || !found || rank != 1 {
		t.Fatalf("root: found = %v, rank = %d, err = %v; want true, 1, nil", found, rank, err)
	}
	if got := bestVersion(t, best); got.String() != "0" {
		t.Errorf("default root version = %s, want 0", got)
	}
}
