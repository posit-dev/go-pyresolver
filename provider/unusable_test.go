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
// failure as count 0 would let the resolution quietly settle on an older
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

			_, count, err := p.Candidates(provider.Project("flask"), pep440set.All())
			if !errors.Is(err, errTransport) {
				t.Fatalf("err = %v, want it to wrap the transport failure", err)
			}
			if count != 0 {
				t.Errorf("count = %d alongside an error; want 0", count)
			}
			if len(p.Unusable()) != 0 {
				t.Errorf("a transport failure was recorded as an unusable version: %v", p.Unusable())
			}
		})
	}
}

// An sdist-only release exists and is visible on PyPI, so "no versions
// available" is the worst thing the report could say about it. Excluding it
// from the count is what keeps the solver's arithmetic honest; recording WHY is
// what lets the report say something true.
func TestSdistOnlyVersionIsExcludedAndRecorded(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	p := provider.New(context.Background(), idx, testOptions(t))

	best, count, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (3.0 has no readable metadata)", count)
	}
	if got := bestVersion(t, best); got.String() != "2.0" {
		t.Errorf("best = %s, want 2.0", got)
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

	best, count, err := p.Candidates(provider.Project("app"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
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

	_, count, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
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

	best, count, err := p.Candidates(provider.Project("app"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
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

	_, count, err := p.Candidates(provider.Project("flask"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1: the version stays a candidate", count)
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

// An unknown package is not an unusable VERSION -- there is no version to name.
func TestUnknownPackageRecordsNothing(t *testing.T) {
	p := provider.New(context.Background(), index.NewMockIndex("test"), testOptions(t))

	if _, count, err := p.Candidates(provider.Project("nope"), pep440set.All()); err != nil || count != 0 {
		t.Fatalf("count = %d, err = %v; want 0, nil", count, err)
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
		if _, _, err := p.Candidates(provider.Project("flask"), pep440set.All()); err != nil {
			t.Fatalf("Candidates: %v", err)
		}
	}

	if got := p.Unusable(); len(got) != 1 {
		t.Errorf("got %d records after five identical queries, want 1: %v", len(got), got)
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
