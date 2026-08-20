// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"errors"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// findUnusable returns the entry for one package version, and whether it is
// present at all.
func findUnusable(us []provider.Unusable, pkg, ver string) (provider.Unusable, bool) {
	for _, u := range us {
		if u.Package.String() == pkg && u.Version.String() == ver {
			return u, true
		}
	}
	return provider.Unusable{}, false
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

	u, ok := findUnusable(res.Unusable, "flask", "3.0")
	if !ok {
		t.Fatalf("Unusable = %+v, want an entry for flask 3.0", res.Unusable)
	}
	if u.Reason != provider.ReasonMetadataUnavailable {
		t.Errorf("Reason = %q, want %q", u.Reason, provider.ReasonMetadataUnavailable)
	}
	if u.Offered {
		t.Errorf("Offered = true, want false: the version was passed over, not selected")
	}
}

// The zero case matters as much as the reporting case: a caller that fails
// closed on a non-empty Unusable would refuse every ordinary resolution if this
// were populated with noise.
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

// The success and failure paths read the same provider, so an entry both paths
// examined must be reported identically by both. Pinning them together is what
// keeps the success path from drifting once it has its own field to populate.
func TestResolveUnusableAgreesWithTheErrorPath(t *testing.T) {
	build := func() index.MetadataIndex {
		idx := index.NewMockIndex("test").
			AddVersion("flask", "2.0").
			AddVersion("flask", "3.0")
		idx.SetUnavailable("flask", "3.0")
		return idx
	}

	// Succeeds by falling back to 2.0, having examined and set aside 3.0.
	res, err := resolve(t, build(), "flask>=2.0")
	if err != nil {
		t.Fatalf("Resolve (success arm): %v", err)
	}

	// Fails: 3.0 is the only version in range and it is unusable.
	_, err = resolve(t, build(), "flask>=3.0")
	if err == nil {
		t.Fatal("Resolve (failure arm): expected an error, got nil")
	}
	var rerr *resolver.ResolutionError
	if !errors.As(err, &rerr) {
		t.Fatalf("Resolve (failure arm): error is %T, want *resolver.ResolutionError", err)
	}

	fromSuccess, ok := findUnusable(res.Unusable, "flask", "3.0")
	if !ok {
		t.Fatalf("success arm Unusable = %+v, want an entry for flask 3.0", res.Unusable)
	}
	fromError, ok := findUnusable(rerr.Unusable, "flask", "3.0")
	if !ok {
		t.Fatalf("error arm Unusable = %+v, want an entry for flask 3.0", rerr.Unusable)
	}

	if fromSuccess.Reason != fromError.Reason {
		t.Errorf("Reason differs: success %q, error %q", fromSuccess.Reason, fromError.Reason)
	}
	if fromSuccess.Offered != fromError.Offered {
		t.Errorf("Offered differs: success %v, error %v", fromSuccess.Offered, fromError.Offered)
	}
}
