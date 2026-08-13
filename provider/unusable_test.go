// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// errTransport stands in for the failures that are NOT facts about a package:
// a dropped connection, a truncated snapshot, a permissions error.
var errTransport = errors.New("connection reset by peer")

// failingIndex injects a transport failure into one method and delegates the
// rest, so a test can assert the difference between "this package has no such
// version" and "the index could not answer".
type failingIndex struct {
	*index.MockIndex
	failVersions bool
	failMetadata bool
}

func (f failingIndex) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	if f.failVersions {
		return nil, errTransport
	}
	return f.MockIndex.Versions(ctx, pkg)
}

func (f failingIndex) Metadata(ctx context.Context, pkg index.PackageName, ver version.Version) (index.PackageMetadata, error) {
	if f.failMetadata {
		return index.PackageMetadata{}, errTransport
	}
	return f.MockIndex.Metadata(ctx, pkg, ver)
}

// THE row that matters most. A version that cannot be USED is data the solver
// reasons with; an index that cannot ANSWER is not. Reporting a transport
// failure as found == false would let the resolution quietly settle on an older
// version, or blame the user's constraints for an outage -- and nothing in the
// report would point back here.
func TestTransportErrorsPropagateRatherThanReadingAsNoSuchVersion(t *testing.T) {
	mock := index.NewMockIndex("test").AddVersion("flask", "3.0.0")

	for _, tc := range []struct {
		name string
		idx  index.MetadataIndex
	}{
		{"Versions fails", failingIndex{MockIndex: mock, failVersions: true}},
		{"Metadata fails", failingIndex{MockIndex: mock, failMetadata: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := provider.New(context.Background(), tc.idx, testOptions(t))

			_, found, _, err := p.Candidates(provider.Project("flask"), pep440set.All())
			if !errors.Is(err, errTransport) {
				t.Fatalf("err = %v, want it to wrap the transport failure", err)
			}
			if found {
				t.Error("found = true alongside an error; want false")
			}
			if len(p.Unusable()) != 0 {
				t.Errorf("a transport failure was recorded as an unusable version: %v", p.Unusable())
			}
		})
	}
}

// An sdist-only release exists and is visible on PyPI, so "no versions
// available" is the worst thing the report could say about it. Not OFFERING it is
// what keeps the resolution honest; recording WHY is what lets the report say
// something true.
//
// ⚠️ rank is 2 here, not 1. rank counts the versions in range before usability is
// tested, so an unusable version is still counted — it is documented as a hint
// that may over-count, and this is that in action. What must stay exact is best
// (the usable version) and the record naming 3.0.
func TestSdistOnlyVersionIsNotOfferedAndIsRecorded(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true: 2.0 is usable")
	}
	if rank != 2 {
		t.Errorf("rank = %d, want 2 (both versions are in range; rank is counted before "+
			"usability)", rank)
	}
	if got := bestVersion(t, best); got.String() != "2.0" {
		t.Errorf("best = %s, want 2.0 — 3.0 has no readable metadata", got)
	}

	rec := onlyRecord(t, p)
	if rec.Package != provider.Project("flask") || rec.Version.String() != "3.0" {
		t.Errorf("recorded %s %s, want flask 3.0", rec.Package, rec.Version)
	}
	if rec.Offered {
		t.Error("an excluded version must not be recorded as offered")
	}
	if !strings.Contains(rec.Reason, "sdist") {
		t.Errorf("reason %q does not explain the sdist-only case", rec.Reason)
	}
}

// A specifier pep440set cannot represent is refused rather than approximated,
// because an approximation resolves to a confident wrong answer.
func TestUnrepresentableRequirementExcludesTheVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "foo ===lolwat").
		AddVersion("app", "0.9", "foo>=1.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.Project("app"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true: 0.9 is usable")
	}
	if rank != 2 {
		t.Fatalf("rank = %d, want 2 (counted before usability)", rank)
	}
	if got := bestVersion(t, best); got.String() != "0.9" {
		t.Errorf("best = %s, want 0.9", got)
	}

	rec := onlyRecord(t, p)
	if rec.Version.String() != "1.0" || rec.Offered {
		t.Errorf("recorded %s offered=%v, want 1.0 offered=false", rec.Version, rec.Offered)
	}
	if !strings.Contains(rec.Reason, "===lolwat") {
		t.Errorf("reason %q does not name the requirement", rec.Reason)
	}
}

// Same rule applied to the interpreter constraint.
func TestUnrepresentableRequiresPythonExcludesTheVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresPython:    mustSpecifiers(t, "===3.11"),
			RequiresPythonRaw: "===3.11",
		}).
		AddVersion("flask", "2.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, rank, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true: 2.0 is usable")
	}
	if rank != 2 {
		t.Errorf("rank = %d, want 2 (counted before usability)", rank)
	}

	rec := onlyRecord(t, p)
	if !strings.Contains(rec.Reason, "Requires-Python") {
		t.Errorf("reason %q does not name Requires-Python", rec.Reason)
	}
}

// A direct-reference requirement carries no version specifier, which would
// otherwise read as "any version" and let the resolver pick an index version
// the publisher never asked for -- a different artifact, silently.
func TestDirectReferenceRequirementExcludesTheDependingVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "2.0", "foo @ https://example.com/foo-1.0-py3-none-any.whl").
		AddVersion("app", "1.0", "foo>=1.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	best, found, rank, err := p.Candidates(provider.Project("app"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true: 1.0 is usable")
	}
	if rank != 2 {
		t.Fatalf("rank = %d, want 2 (counted before usability)", rank)
	}
	if got := bestVersion(t, best); got.String() != "1.0" {
		t.Errorf("best = %s, want 1.0", got)
	}

	rec := onlyRecord(t, p)
	if rec.Version.String() != "2.0" || rec.Offered {
		t.Errorf("recorded %s offered=%v, want 2.0 offered=false", rec.Version, rec.Offered)
	}
	if !strings.Contains(rec.Reason, "https://example.com/foo-1.0-py3-none-any.whl") {
		t.Errorf("reason %q does not name the URL", rec.Reason)
	}
}

// The one row where the version is KEPT. The index decoder deliberately treats
// an unparseable Requires-Python as unconstrained, because over-admitting a
// candidate surfaces later as an install-time failure while under-constraining
// would silently change the resolution. That choice is only defensible if it is
// visible, which is what the record is for.
func TestUnreadableRequiresPythonKeepsTheVersionAndRecordsWhy(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresPythonRaw:        ">= 3.8, !!bogus",
			RequiresPythonUnreadable: true,
		})

	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, rank, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found || rank != 1 {
		t.Fatalf("found = %v, rank = %d; want true, 1: the version stays a candidate",
			found, rank)
	}

	byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("flask"), "3.0"))
	if _, ok := byPkg[provider.Python()]; ok {
		t.Error("an unreadable Requires-Python must leave the interpreter unconstrained")
	}

	rec := onlyRecord(t, p)
	if !rec.Offered {
		t.Error("the version WAS offered; recording it as excluded would misreport it")
	}
	if !strings.Contains(rec.Reason, ">= 3.8, !!bogus") {
		t.Errorf("reason %q does not quote the constraint that could not be read", rec.Reason)
	}
}

// A version that is later EXCLUDED must never carry an Offered:true record.
//
// The unreadable-Requires-Python record is written early, before the rest of
// the requirements have been examined, so a version can be recorded as offered
// and then excluded by something further down. Offered exists so a consumer
// need not infer the distinction from the reason text; a stale true makes it
// report that a version resolved with an unconstrained interpreter when that
// version was never a candidate at all.
func TestExcludedVersionIsNeverRecordedAsOffered(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresPythonRaw:        ">= 3.8, !!bogus",
			RequiresPythonUnreadable: true,
			// Arbitrary equality has no version-set equivalent, so this
			// version cannot be offered -- decided AFTER the record above.
			RequiresDist: mustRequirements(t, "foo ===lolwat"),
		})

	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if found {
		t.Fatal("found = true, want false: the version cannot be used")
	}

	for _, rec := range p.Unusable() {
		if rec.Offered {
			t.Errorf("flask 3.0 was excluded (found false) but is recorded as Offered: %+v", rec)
		}
	}
}

// An unknown package is not an unusable VERSION -- there is no version to name.
func TestUnknownPackageRecordsNothing(t *testing.T) {
	p := provider.New(context.Background(), index.NewMockIndex("test"), testOptions(t))

	if _, found, _, err := p.Candidates(provider.Project("nope"), pep440set.All()); err != nil || found {
		t.Fatalf("found = %v, err = %v; want false, nil", found, err)
	}
	if got := p.Unusable(); len(got) != 0 {
		t.Errorf("recorded %v for a package that does not exist", got)
	}
}

// Candidates is asked about the same package many times as the solver
// backtracks. A report that listed one sdist-only version forty times would be
// unreadable.
func TestUnusableRecordsAreDeduplicated(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	p := provider.New(context.Background(), idx, testOptions(t))
	for range 5 {
		if _, _, _, err := p.Candidates(provider.Project("flask"), pep440set.All()); err != nil {
			t.Fatalf("Candidates: %v", err)
		}
	}

	if got := p.Unusable(); len(got) != 1 {
		t.Errorf("got %d records after five identical queries, want 1: %v", len(got), got)
	}
}

// TestEveryReasonIsRecordedWhenNOTHINGIsUsable pins the claim the whole shrinking of
// Unusable() rests on.
//
// Candidates stops at the first usable version, so versions ranked below the chosen
// one are never examined and never recorded — Unusable() became what the resolution
// ENCOUNTERED rather than an audit of everything published. The defence of that is
// narrow and specific: when a package has NOTHING usable, establishing "nothing"
// requires examining all of it, so every reason is still recorded. That is the case
// a failure report actually needs, because it is the one that produces "no version
// of X matches".
//
// ⚠️ Until this test, that defence was an argument and nothing more. It is stated in
// three doc comments and a CHANGELOG entry, so it needs to be true.
func TestEveryReasonIsRecordedWhenNOTHINGIsUsable(t *testing.T) {
	// Three versions, all in range, none usable, for three DIFFERENT reasons. The
	// reasons differ so that a record naming the wrong version is visible rather
	// than plausible; deduplication is not the hazard, since record keys on package
	// AND version AND reason, so distinct versions could never collapse anyway.
	idx := index.NewMockIndex("test").
		SetUnavailable("flask", "3.0").
		SetMetadata("flask", "2.0", index.PackageMetadata{
			RequiresDist: mustRequirements(t, "foo ===lolwat"),
		}).
		SetMetadata("flask", "1.0", index.PackageMetadata{
			RequiresDist: mustRequirements(t, "bar @ https://example.com/bar-1.0-py3-none-any.whl"),
		})

	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if found {
		t.Fatal("found = true, but no version of flask is usable")
	}

	got := p.Unusable()
	if len(got) != 3 {
		t.Fatalf("recorded %d reasons, want 3 — one per version. When nothing is usable "+
			"the walk is exhaustive by necessity, so a missing record means the failure "+
			"report has lost a version it could have explained: %+v", len(got), got)
	}

	byVersion := map[string]provider.Unusable{}
	for _, rec := range got {
		byVersion[rec.Version.String()] = rec
	}
	for _, want := range []string{"1.0", "2.0", "3.0"} {
		rec, ok := byVersion[want]
		if !ok {
			t.Errorf("no record for flask %s; got records for %v", want, byVersion)
			continue
		}
		if rec.Offered {
			t.Errorf("flask %s is recorded as offered, but nothing was usable", want)
		}
		if rec.Reason == "" {
			t.Errorf("flask %s recorded with no reason, which explains nothing", want)
		}
	}
}

// A root requirement that cannot be expressed has no other version to fall back
// to, so it aborts the resolve instead of quietly excluding something.
func TestRootRequirementsThatCannotBeExpressedAreAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  string
		want string
	}{
		{"direct reference", "foo @ https://example.com/foo-1.0.whl", "https://example.com/foo-1.0.whl"},
		{"unrepresentable specifier", "foo ===lolwat", "===lolwat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Requirements = mustRequirements(t, tc.req)
			p := provider.New(context.Background(), index.NewMockIndex("test"), opts)

			_, err := p.Dependencies(provider.Root(), pep440set.Exactly(mustVersion(t, "0")))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// onlyRecord asserts exactly one version was recorded and returns it.
func onlyRecord(t *testing.T, p *provider.Provider) provider.Unusable {
	t.Helper()
	got := p.Unusable()
	if len(got) != 1 {
		t.Fatalf("got %d records, want exactly 1: %v", len(got), got)
	}
	return got[0]
}
