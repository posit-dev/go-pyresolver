// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// yankIndex builds a two-version "acme" index (1.0.0 yanked, 0.9.0 clean),
// with yank data declared captured.
func yankIndex(t *testing.T) *index.MockIndex {
	t.Helper()
	idx := index.NewMockIndex("test").
		AddVersion("acme", "0.9.0").
		AddVersion("acme", "1.0.0").
		SetYanksCaptured(true)

	clean, err := index.ParseRecord(index.RawRecord{})
	if err != nil {
		t.Fatalf("ParseRecord clean: %v", err)
	}
	idx.SetMetadata("acme", "0.9.0", clean)

	yanked, err := index.ParseRecord(index.RawRecord{Yanked: true})
	if err != nil {
		t.Fatalf("ParseRecord yanked: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", yanked)

	return idx
}

func findUnusable(t *testing.T, p *provider.Provider, ver string) (provider.Unusable, bool) {
	t.Helper()
	for _, u := range p.Unusable() {
		if u.Version.String() == ver {
			return u, true
		}
	}
	return provider.Unusable{}, false
}

// Test 1: yanked newest skipped, the older clean version stays selectable.
func TestYankedVersionRejectedOlderSelected(t *testing.T) {
	idx := yankIndex(t)
	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("acme"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false; want the clean 0.9.0 to still be usable")
	}

	u, ok := findUnusable(t, p, "1.0.0")
	if !ok {
		t.Fatal("expected 1.0.0 recorded as unusable")
	}
	if u.Kind != provider.KindYanked {
		t.Errorf("Kind = %v, want KindYanked", u.Kind)
	}
	if _, ok := findUnusable(t, p, "0.9.0"); ok {
		t.Error("0.9.0 must not be recorded as unusable")
	}
}

// Test 4: no marker (YanksCaptured false) means nothing is filtered, even
// though the metadata says Yanked.
func TestNoMarkerMeansNothingFiltered(t *testing.T) {
	idx := yankIndex(t)
	idx.SetYanksCaptured(false)
	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("acme"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if !found {
		t.Fatal("found = false; want 1.0.0 usable since yank data is not captured")
	}
	if _, ok := findUnusable(t, p, "1.0.0"); ok {
		t.Error("1.0.0 must not be rejected when YanksCaptured is false")
	}
}

// Test 2 + 3: a root == pin exempts the yanked version; a transitive ==
// (not in Options.Requirements) does not.
func TestRootPinExemptsYankTransitiveDoesNot(t *testing.T) {
	idx := yankIndex(t)

	t.Run("root == exempts", func(t *testing.T) {
		opts := testOptions(t)
		req, err := requirement.Parse("acme==1.0.0")
		if err != nil {
			t.Fatalf("parse requirement: %v", err)
		}
		opts.Requirements = []requirement.Requirement{req}
		p := provider.New(context.Background(), idx, opts)

		if _, found, _, err := p.Candidates(provider.Project("acme"), pep440set.All()); err != nil || !found {
			t.Fatalf("Candidates: found=%v err=%v, want found=true", found, err)
		}
		if _, ok := findUnusable(t, p, "1.0.0"); ok {
			t.Error("1.0.0 must not be rejected when a root == pins it exactly")
		}
	})

	t.Run("transitive == does not exempt", func(t *testing.T) {
		// No root requirement pins acme; a transitive requirement (a
		// dependency's own Requires-Dist) never reaches Options.Requirements.
		p := provider.New(context.Background(), idx, testOptions(t))

		if _, found, _, err := p.Candidates(provider.Project("acme"), pep440set.All()); err != nil || !found {
			t.Fatalf("Candidates: found=%v err=%v, want found=true (0.9.0 still usable)", found, err)
		}
		u, ok := findUnusable(t, p, "1.0.0")
		if !ok || u.Kind != provider.KindYanked {
			t.Error("1.0.0 must still be rejected as yanked with no root pin")
		}
	})
}

// Test 5: an only-yanked package with no exempting pin has nothing usable,
// and the rejection names KindYanked.
func TestOnlyYankedNoPinFailsNamingKindYanked(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("acme", "1.0.0").SetYanksCaptured(true)
	yanked, err := index.ParseRecord(index.RawRecord{Yanked: true})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", yanked)

	p := provider.New(context.Background(), idx, testOptions(t))

	_, found, _, err := p.Candidates(provider.Project("acme"), pep440set.All())
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if found {
		t.Fatal("found = true; want false, the only version is yanked with no pin")
	}
	u, ok := findUnusable(t, p, "1.0.0")
	if !ok || u.Kind != provider.KindYanked {
		t.Fatalf("expected 1.0.0 recorded unusable with KindYanked, got ok=%v kind=%v", ok, u.Kind)
	}
}

// A decided dependency's exact pin exempts only the version it names: with
// foo 1.0 and 1.1 both yanked, asking for any foo picks the pinned 1.0, not
// the newer yanked 1.1.
func TestTransitivePinExemptsOnlyPinnedVersion(t *testing.T) {
	build := func(t *testing.T) *index.MockIndex {
		idx := index.NewMockIndex("test").SetYanksCaptured(true).
			AddVersion("app", "1.0", "foo==1.0").
			AddVersion("foo", "0.9").AddVersion("foo", "1.0").AddVersion("foo", "1.1")
		yanked, err := index.ParseRecord(index.RawRecord{Yanked: true})
		if err != nil {
			t.Fatalf("ParseRecord: %v", err)
		}
		idx.SetMetadata("foo", "1.0", yanked)
		idx.SetMetadata("foo", "1.1", yanked)
		return idx
	}
	best := func(t *testing.T, transitive bool) string {
		opts := testOptions(t)
		opts.YankExemptTransitivePins = transitive
		p := provider.New(context.Background(), build(t), opts)
		app := provider.Project("app")
		if _, _, _, err := p.Candidates(app, pep440set.All()); err != nil {
			t.Fatalf("Candidates(app): %v", err)
		}
		if _, err := p.Dependencies(app, pep440set.Exactly(version.MustParse("1.0"))); err != nil {
			t.Fatalf("Dependencies(app): %v", err)
		}
		set, found, _, err := p.Candidates(provider.Project("foo"), pep440set.All())
		if err != nil || !found {
			t.Fatalf("Candidates(foo): found=%v err=%v", found, err)
		}
		v, _ := set.Singleton()
		return v.String()
	}

	if got := best(t, false); got != "0.9" {
		t.Errorf("option off: best foo = %s, want 0.9", got)
	}
	if got := best(t, true); got != "1.0" {
		t.Errorf("option on: best foo = %s, want 1.0 (pinned, yanked), not 1.1", got)
	}
}
