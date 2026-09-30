// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/requirement"
)

// addRelease registers one release of name, optionally yanked, on a yank-aware
// index.
func addRelease(t *testing.T, idx *index.MockIndex, name, ver string, yanked bool, requires ...string) {
	t.Helper()
	reqs := mustRequirements(t, requires...)
	idx.SetMetadata(name, ver, index.PackageMetadata{
		RequiresDist: append([]requirement.Requirement(nil), reqs...),
		Yanked:       yanked,
	})
}

func newYankIndex() *index.MockIndex {
	return index.NewMockIndex("test").SetYanksCaptured(true)
}

func resolveYank(
	t *testing.T, idx index.MetadataIndex, transitive bool, reqs ...string,
) (*resolver.Resolution, error) {
	t.Helper()
	opts := testOptions(t)
	opts.YankExemptTransitivePins = transitive
	return resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
}

func wantYankedKind(t *testing.T, err error, pkg, ver string) {
	t.Helper()
	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *resolver.ResolutionError", err)
	}
	for _, u := range re.Unusable {
		if u.Package.Name.String() == pkg && u.Version.String() == ver && u.Kind == provider.KindYanked {
			return
		}
	}
	t.Fatalf("no KindYanked record for %s %s in %+v", pkg, ver, re.Unusable)
}

type pinView struct {
	Package, Version, By string
}

func pinViews(res *resolver.Resolution) []pinView {
	var out []pinView
	for _, yp := range res.YankedPins {
		by := "root"
		if !yp.RequestedBy.Root {
			by = yp.RequestedBy.Package.String() + " " + yp.RequestedBy.Version.String()
		}
		out = append(out, pinView{yp.Package.String(), yp.Version.String(), by})
	}
	return out
}

func TestTransitiveExactPinToYankedVersion(t *testing.T) {
	for _, pin := range []string{"foo==1.0", "foo==1.0.0"} {
		t.Run(pin, func(t *testing.T) {
			idx := newYankIndex()
			addRelease(t, idx, "app", "1.0", false, pin)
			addRelease(t, idx, "foo", "0.9", false)
			addRelease(t, idx, "foo", "1.0", true)

			_, err := resolveYank(t, idx, false, "app")
			wantYankedKind(t, err, "foo", "1.0")

			res, err := resolveYank(t, idx, true, "app")
			if err != nil {
				t.Fatalf("option on: %v", err)
			}
			if got := pins(t, res)["foo"]; got != "1.0" {
				t.Errorf("foo = %s, want 1.0", got)
			}
			want := []pinView{{"foo", "1.0", "app 1.0"}}
			if got := pinViews(res); !reflect.DeepEqual(got, want) {
				t.Errorf("YankedPins = %v, want %v", got, want)
			}
		})
	}
}

// pep440set cannot express `===`, so a requirement using it makes its
// requester unusable in either mode, and it never exempts transitively.
func TestTransitiveArbitraryEqualityIsUnrepresentable(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo===1.0")
	addRelease(t, idx, "foo", "1.0", true)

	for _, transitive := range []bool{false, true} {
		_, err := resolveYank(t, idx, transitive, "app")
		var re *resolver.ResolutionError
		if !errors.As(err, &re) {
			t.Fatalf("transitive=%v: err = %v, want a ResolutionError", transitive, err)
		}
	}
}

func TestTransitiveRangeNeverExemptsYankedVersion(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo>=1.0")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	_, err := resolveYank(t, idx, true, "app")
	wantYankedKind(t, err, "foo", "1.0")
}

func TestTransitiveWildcardPinNeverExemptsYankedVersion(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.*")
	addRelease(t, idx, "foo", "1.0", true)

	_, err := resolveYank(t, idx, true, "app")
	wantYankedKind(t, err, "foo", "1.0")
}

// A pin on one yanked version must not exempt another: foo 1.1 is yanked too
// and the range wants it, but only foo 1.0 is pinned.
func TestTransitivePinExemptsOnlyTheVersionItPins(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.0")
	addRelease(t, idx, "other", "1.0", false, "foo>=1.1")
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "foo", "1.1", true)

	_, err := resolveYank(t, idx, true, "app", "other")
	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	wantYankedKind(t, err, "foo", "1.1")
}

// app pins yanked foo==1.0 while other needs a non-yanked foo>=2.0: no version
// satisfies both, so the exemption must not paper over the conflict.
func TestTransitivePinConflictingWithCleanRequirementStillFails(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.0")
	addRelease(t, idx, "other", "1.0", false, "foo>=2.0")
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "foo", "2.0", false)

	res, err := resolveYank(t, idx, true, "app", "other")
	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (res %v), want a conflict", err, res)
	}
}

// pip's answer: app pins yanked foo==1.0 and other asks foo>=1.0 (a clean 1.1
// exists). The pin wins and 1.0 is installed, whichever the solver tries first.
func TestTransitivePinWinsOverCompatibleRange(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.0")
	addRelease(t, idx, "other", "1.0", false, "foo>=1.0")
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "foo", "1.1", false)

	for _, reqs := range [][]string{{"app", "other"}, {"other", "app"}, {"other", "foo>=1.0", "app"}} {
		res, err := resolveYank(t, idx, true, reqs...)
		if err != nil {
			t.Fatalf("%v: %v", reqs, err)
		}
		if got := pins(t, res)["foo"]; got != "1.0" {
			t.Errorf("%v: foo = %s, want 1.0", reqs, got)
		}
	}
}

// Same, but 1.0 is the only release of foo, so the root's range alone matches
// only a yanked version and foo has nothing usable before app is chosen. pip
// fails here too (the same shape as its case M); see yank_permit_test.go.
func TestTransitivePinWhenOnlyYankedVersionExistsFails(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.0")
	addRelease(t, idx, "foo", "1.0", true)

	for _, reqs := range [][]string{{"app", "foo>=1.0"}, {"foo>=1.0", "app"}} {
		_, err := resolveYank(t, idx, true, reqs...)
		wantYankedKind(t, err, "foo", "1.0")
	}
}

func TestRootPinBehaviourUnchanged(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	for _, transitive := range []bool{false, true} {
		res, err := resolveYank(t, idx, transitive, "foo==1.0")
		if err != nil {
			t.Fatalf("transitive=%v: %v", transitive, err)
		}
		if got := pins(t, res)["foo"]; got != "1.0" {
			t.Errorf("transitive=%v: foo = %s, want 1.0", transitive, got)
		}
		want := []pinView{{"foo", "1.0", "root"}}
		if got := pinViews(res); !reflect.DeepEqual(got, want) {
			t.Errorf("transitive=%v: YankedPins = %v, want %v", transitive, got, want)
		}

		// An unpinned root requirement still skips the yanked release.
		res, err = resolveYank(t, idx, transitive, "foo")
		if err != nil {
			t.Fatalf("transitive=%v unpinned: %v", transitive, err)
		}
		if got := pins(t, res)["foo"]; got != "0.9" || len(res.YankedPins) != 0 {
			t.Errorf("transitive=%v unpinned: foo = %s pins=%v, want 0.9 and none", transitive, got, res.YankedPins)
		}
	}
}
