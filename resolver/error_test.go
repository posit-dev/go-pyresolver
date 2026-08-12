// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// resolutionError runs a resolve that must fail as a conflict and returns the
// explained error.
func resolutionError(t *testing.T, idx index.MetadataIndex, reqs ...string) *resolver.ResolutionError {
	t.Helper()
	_, err := resolve(t, idx, reqs...)
	if err == nil {
		t.Fatal("Resolve succeeded, but the requirements cannot be satisfied")
	}
	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *resolver.ResolutionError", err, err)
	}
	return re
}

func TestFailedResolveCarriesAnExplanation(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "0.9").
		AddVersion("foo", "1.0")

	re := resolutionError(t, idx, "foo>=1.0", "bar")

	if re.Report == nil || len(re.Report.Lines) == 0 {
		t.Fatalf("the error carries no explanation: %+v", re)
	}
	// Each Line carries the incompatibility behind the sentence, which is what
	// lets a consumer build its own presentation without re-walking the
	// derivation graph.
	for i, line := range re.Report.Lines {
		if line.Node == nil {
			t.Errorf("line %d (%q) carries no incompatibility", i, line.Text)
		}
	}
	if !strings.Contains(re.Error(), "foo") {
		t.Errorf("the message does not name foo, which is what the conflict is about:\n%s", re.Error())
	}
}

// The proof has to stay reachable. A consumer that wants the derivation graph
// itself, or that distinguishes "these requirements conflict" from "the index
// failed", gets there through the wrapped error.
func TestResolutionErrorUnwrapsToTheSolversProof(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "1.0")

	_, err := resolve(t, idx, "foo>=1.0", "bar")

	var unsolvable *solver.Unsolvable[provider.Package, pep440set.Set]
	if !errors.As(err, &unsolvable) {
		t.Fatalf("errors.As did not reach *solver.Unsolvable through the error chain: %v", err)
	}
	if unsolvable.RootCause == nil {
		t.Error("the unwrapped proof carries no root cause")
	}
}

// ⚠️ THE WORST FAILURE IN THE SET, and an explicit acceptance criterion of
// #18657. Without this the report says "no version of flask matches >=3.0" for
// a version the user can plainly see on PyPI, and nothing tells them why.
func TestResolutionErrorExplainsAnSdistOnlyRelease(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	re := resolutionError(t, idx, "flask>=3.0")
	msg := re.Error()

	for _, want := range []string{"flask", "3.0", "sdist"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q:\n%s", want, msg)
		}
	}
	// It must say what to do about it, not merely what happened.
	if !strings.Contains(msg, "wheel") {
		t.Errorf("the message does not point at a remedy:\n%s", msg)
	}
}

// A version excluded from a package that resolved perfectly well is noise, and
// noise in a failure report is what makes people stop reading them.
func TestResolutionErrorIgnoresAnUnrelatedSdistOnlyRelease(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "0.9").
		AddVersion("foo", "1.0").
		// quux resolves fine at 1.0; its 2.0 is sdist-only and has nothing
		// whatever to do with the foo/bar conflict.
		AddVersion("quux", "1.0").
		SetUnavailable("quux", "2.0")

	re := resolutionError(t, idx, "foo>=1.0", "bar", "quux")

	if !hasSdistRecord(re.Unusable, "quux", "2.0") {
		t.Fatal("the provider never recorded quux 2.0, so this test proves nothing")
	}
	if strings.Contains(re.Error(), "quux") {
		t.Errorf("the message mentions quux, which resolved fine:\n%s", re.Error())
	}
}

// Offered == true means the version WAS a candidate: the record is a note about
// how it was treated, not a reason it could not be used. Putting one in a
// failure explanation tells the user a version was rejected when it was not.
func TestResolutionErrorOmitsRecordsForVersionsThatWereOffered(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist:             mustRequirements(t, "werkzeug>=99"),
			RequiresPythonRaw:        "not a version",
			RequiresPythonUnreadable: true,
		}).
		AddVersion("werkzeug", "3.0.1")

	re := resolutionError(t, idx, "flask")

	var offered []provider.Unusable
	for _, u := range re.Unusable {
		if u.Offered {
			offered = append(offered, u)
		}
	}
	if len(offered) == 0 {
		t.Fatal("no Offered record was made, so this test proves nothing")
	}
	for _, u := range offered {
		if strings.Contains(re.Error(), u.Reason) {
			t.Errorf("the message repeats a record for a version that WAS offered (%s %s):\n%s",
				u.Package, u.Version, re.Error())
		}
	}
}

// An extra is a separate SOLVER package for the same project, and the
// provider's dedupe key is the solver package -- so flask and flask[async] each
// record flask 3.0 as sdist-only. The paragraph reads only the project name and
// the version, so both records render byte-identically, and a report that says
// the same thing twice reads like two problems rather than one.
func TestResolutionErrorExplainsAnSdistOnlyReleaseOnlyOnce(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "2.0", index.PackageMetadata{
			ProvidesExtra: []string{"async"},
		}).
		SetUnavailable("flask", "3.0")

	re := resolutionError(t, idx, "flask[async]", "flask>=3.0")

	// The premise: two records, one project, one version. Without both this
	// test would pass for the wrong reason.
	var records int
	for _, u := range re.Unusable {
		if u.Package.Name == "flask" && u.Version.Equal(version.MustParse("3.0")) &&
			u.Reason == provider.ReasonMetadataUnavailable {
			records++
		}
	}
	if records < 2 {
		t.Fatalf("the provider made %d records for flask 3.0, want 2 (one per solver "+
			"package); this test proves nothing", records)
	}

	msg := re.Error()
	if got := strings.Count(msg, "Note: flask 3.0 exists"); got != 1 {
		t.Errorf("the message carries the sdist-only note %d times, want 1:\n%s", got, msg)
	}
}

// Offered == true is checked FIRST, before the record is matched against the
// report, and the two filters are not interchangeable: an offered version can
// be named by the report at a version inside a range it mentions -- that is
// what being a candidate MEANS -- so reportNames alone lets it through.
//
// The record is built by hand because the provider does not currently record
// ReasonMetadataUnavailable with Offered true. It is the guard that keeps that
// true from the report's side, and a guard no test exercises is a guard that
// gets deleted.
func TestResolutionErrorOmitsAnOfferedSdistOnlyRecord(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	re := resolutionError(t, idx, "flask>=3.0")
	if !strings.Contains(re.Error(), "Note: flask 3.0 exists") {
		t.Fatalf("the unmodified error does not carry the note, so flipping Offered "+
			"proves nothing:\n%s", re.Error())
	}

	var offered []provider.Unusable
	for _, u := range re.Unusable {
		if u.Reason != provider.ReasonMetadataUnavailable {
			continue
		}
		u.Offered = true
		offered = append(offered, u)
	}
	if len(offered) == 0 {
		t.Fatal("no sdist-only record was made, so this test proves nothing")
	}

	// Same report, same records, one field flipped.
	reoffered := &resolver.ResolutionError{Report: re.Report, Unusable: offered}
	if strings.Contains(reoffered.Error(), "Note: flask 3.0 exists") {
		t.Errorf("the message reports a version that WAS offered as one that could not "+
			"be used:\n%s", reoffered.Error())
	}
}

// An index that cannot answer is not a conflict between requirements. Dressing
// an outage up as one sends the caller looking for a problem in their own
// requirements.
func TestResolveReturnsAnIndexFailureAsItself(t *testing.T) {
	idx := &refusingIndex{}
	_, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, testOptions(t))
	if err == nil {
		t.Fatal("Resolve succeeded against an index that refuses every call")
	}
	if !errors.Is(err, errRefused) {
		t.Errorf("err = %v, which does not wrap the index's own failure", err)
	}
	var re *resolver.ResolutionError
	if errors.As(err, &re) {
		t.Errorf("an index failure was reported as a resolution conflict:\n%s", re.Error())
	}
}

// An error message is what someone sees when something has already gone wrong,
// so it is the last place that should introduce a second failure. A caller can
// build one of these by hand -- both fields are exported -- and Error must not
// panic on it.
func TestResolutionErrorWithNoReportStillHasAMessage(t *testing.T) {
	re := &resolver.ResolutionError{}
	if msg := re.Error(); msg == "" {
		t.Error("Error() is empty, which will read as a missing message")
	}
	if err := re.Unwrap(); err != nil {
		t.Errorf("Unwrap() = %v, want nil", err)
	}
}

func hasSdistRecord(records []provider.Unusable, name index.PackageName, ver string) bool {
	want := version.MustParse(ver)
	for _, u := range records {
		if u.Package.Name == name && u.Version.Equal(want) && !u.Offered {
			return true
		}
	}
	return false
}
