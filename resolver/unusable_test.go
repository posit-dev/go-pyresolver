// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// countUnusable counts the records for one project at one version, keyed the way
// a consumer should key them: on Package.Name, not Package.String(), because an
// extra is a separate solver package and renders as "flask[async]".
func countUnusable(us []provider.Unusable, project, ver string, reason string) int {
	var n int
	for _, u := range us {
		if string(u.Package.Name) == project && u.Version.String() == ver &&
			(reason == "" || u.Reason == reason) {
			n++
		}
	}
	return n
}

// newestFirstIsDemoted ranks 3.0 last while leaving every other pair alone. It
// stands in for the reason candidate.Policy exists: an embedder demoting a
// version that is blocked by an administrator or carries a known vulnerability.
type newestFirstIsDemoted struct{}

func (newestFirstIsDemoted) Less(_ index.PackageName, a, b version.Version) bool {
	three := version.MustParse("3.0")
	if a.Equal(three) {
		return false
	}
	if b.Equal(three) {
		return true
	}
	return b.LessThan(a)
}

// A resolution that succeeds by passing over a newer release still has to say
// it did so. Under the default newest-first policy the release the caller most
// likely meant is the one that was skipped, and reporting nothing turns
// "flask 3.0 publishes no usable metadata" into a silent downgrade to 2.0.
func TestResolveReportsAVersionSetAsideOnSuccess(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		AddVersion("flask", "3.0")
	idx.SetUnavailable("flask", "3.0")

	res, err := resolve(t, idx, "flask>=2.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := res.Pinned[index.NewPackageName("flask")].String(), "2.0"; got != want {
		t.Fatalf("Pinned flask = %q, want %q (the premise of this test)", got, want)
	}

	if n := countUnusable(res.Unusable, "flask", "3.0", provider.ReasonMetadataUnavailable); n != 1 {
		t.Fatalf("records for flask 3.0 = %d, want 1; Unusable = %+v", n, res.Unusable)
	}
	for _, u := range res.Unusable {
		if u.Version.String() == "3.0" && u.Offered {
			t.Errorf("Offered = true, want false: the version was passed over, not selected")
		}
	}
}

// The zero case matters as much as the reporting case: a caller that fails
// closed on a non-empty Unusable would refuse every ordinary resolution if this
// were populated with noise.
//
// ⚠️ This one cannot fail against an unpopulated field. It is a guard against
// noise, not a demonstration that population works.
func TestResolveReportsNoUnusableWhenNothingWasSetAside(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0", "werkzeug>=2.0").
		AddVersion("werkzeug", "2.1")

	res, err := resolve(t, idx, "flask>=2.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Unusable) != 0 {
		t.Errorf("Unusable = %+v, want empty", res.Unusable)
	}
}

// ⚠️ A record is NOT proof a version was rejected. Offered:true means the
// version was accepted with a note, and it happens on ordinary successes -- an
// unreadable Requires-Python on the very version that gets pinned.
//
// This is the test that stops a consumer implementing "fail if anything was set
// aside" as len(Unusable) != 0, which would reject this perfectly good
// resolution.
func TestResolveReportsAnOfferedRecordForTheVersionItChose(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "2.0", index.PackageMetadata{
			RequiresPythonRaw:        "not a version",
			RequiresPythonUnreadable: true,
		})

	res, err := resolve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := res.Pinned[index.NewPackageName("flask")].String(), "2.0"; got != want {
		t.Fatalf("Pinned flask = %q, want %q (the premise of this test)", got, want)
	}

	var offered int
	for _, u := range res.Unusable {
		if u.Offered {
			offered++
		}
	}
	if offered == 0 {
		t.Fatalf("no Offered:true record on a successful resolution, so this test proves "+
			"nothing; Unusable = %+v", res.Unusable)
	}
}

// The provider's dedupe key is the SOLVER package, and an extra is a separate
// solver package for the same project -- so flask and flask[async] each record
// flask 3.0. ResolutionError.Error dedupes on (project, version) before
// rendering; a Resolution does not, so a consumer that renders one line per
// entry prints the same release twice.
//
// Pinning it here because the field's doc comment tells callers to dedupe, and a
// promise nothing tests is a promise that can quietly stop being true.
func TestResolveUnusableCanHoldOneProjectVersionTwice(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "2.0", index.PackageMetadata{
			ProvidesExtra: []string{"async"},
		}).
		SetUnavailable("flask", "3.0")

	res, err := resolve(t, idx, "flask[async]>=2.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if n := countUnusable(res.Unusable, "flask", "3.0", provider.ReasonMetadataUnavailable); n != 2 {
		t.Errorf("records for flask 3.0 = %d, want 2 (one per solver package); Unusable = %+v",
			n, res.Unusable)
	}
}

// ⚠️ The field is not exhaustive, and this is the shape of the gap: candidate
// selection stops TESTING at the first usable version in RANKED order, so a
// version ranked below the winner is never examined and cannot be reported.
//
// Without this test the doc's "not exhaustive" caveat is unverified prose.
func TestResolveDoesNotReportVersionsRankedBelowTheChosenOne(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0")
	idx.SetUnavailable("flask", "1.0")

	res, err := resolve(t, idx, "flask>=1.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := res.Pinned[index.NewPackageName("flask")].String(), "2.0"; got != want {
		t.Fatalf("Pinned flask = %q, want %q (the premise of this test)", got, want)
	}

	if n := countUnusable(res.Unusable, "flask", "1.0", ""); n != 0 {
		t.Errorf("flask 1.0 is reported %d times, but it ranks below the chosen 2.0 and is "+
			"never examined; Unusable = %+v", n, res.Unusable)
	}
}

// ⚠️ The gap is RANKED order, not version order, so a non-default Policy moves
// it. A newer release demoted below the winner is passed over WITHOUT being
// reported -- which is the exact silent downgrade this field exists to prevent,
// reachable through a public option.
//
// candidate.Policy's own doc names Package Manager demoting blocked or
// vulnerable versions as the motivating case, so this is not a hypothetical.
func TestResolveDoesNotReportANewerVersionDemotedByThePolicy(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		AddVersion("flask", "3.0")
	idx.SetUnavailable("flask", "3.0")

	opts := testOptions(t)
	opts.Policy = newestFirstIsDemoted{}
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask>=2.0"), idx, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := res.Pinned[index.NewPackageName("flask")].String(), "2.0"; got != want {
		t.Fatalf("Pinned flask = %q, want %q (the premise of this test)", got, want)
	}

	if n := countUnusable(res.Unusable, "flask", "3.0", ""); n != 0 {
		t.Errorf("flask 3.0 is reported %d times under a policy that demoted it below the "+
			"winner, so it was never examined; Unusable = %+v", n, res.Unusable)
	}
}

// "In the order first encountered" is a promise in the field's doc. Two set-aside
// versions of one project under the default policy are encountered newest-first,
// so the slice has to read 3.0 before 2.5.
func TestResolveUnusablePreservesEncounterOrder(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		AddVersion("flask", "2.5").
		AddVersion("flask", "3.0")
	idx.SetUnavailable("flask", "2.5")
	idx.SetUnavailable("flask", "3.0")

	res, err := resolve(t, idx, "flask>=2.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var order []string
	for _, u := range res.Unusable {
		if string(u.Package.Name) == "flask" {
			order = append(order, u.Version.String())
		}
	}
	if len(order) != 2 {
		t.Fatalf("set-aside flask versions = %v, want two (the premise of this test)", order)
	}
	if order[0] != "3.0" || order[1] != "2.5" {
		t.Errorf("order = %v, want [3.0 2.5]: newest is encountered first", order)
	}
}
