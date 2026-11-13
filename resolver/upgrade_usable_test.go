// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// extraSolves resolves with the pass on and returns the answer and the number
// of resolutions beyond the first.
func extraSolves(t *testing.T, budget int, idx index.MetadataIndex, opts resolver.Options, reqs ...string) (map[string]string, int) {
	t.Helper()
	count, restore := resolver.CountSolves()
	defer restore()
	opts.UpgradeRoots = budget
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pins(t, res), count() - 1
}

// A top version the solver can never pick is not the target: a root already
// on the newest usable version is not behind, and costs no re-solve.
func TestUpgradeRootsIgnoresAnUnusableTop(t *testing.T) {
	base := map[string]string{"a": "1.0"}
	cases := map[string]func() *index.MockIndex{
		"metadata unavailable": func() *index.MockIndex {
			return index.NewMockIndex("test").AddVersion("a", "1.0").SetUnavailable("a", "2.0")
		},
		"requires-python": func() *index.MockIndex {
			return index.NewMockIndex("test").AddVersion("a", "1.0").
				SetMetadata("a", "2.0", index.PackageMetadata{RequiresPython: mustSpecifiers(t, ">=3.12")})
		},
		"yanked": func() *index.MockIndex {
			return index.NewMockIndex("test").SetYanksCaptured(true).AddVersion("a", "1.0").
				SetMetadata("a", "2.0", index.PackageMetadata{Yanked: true})
		},
		"no compatible wheel": func() *index.MockIndex {
			return index.NewMockIndex("test").SetWheelTagsComplete(true).
				SetMetadata("a", "1.0", index.PackageMetadata{TagsCaptured: true, WheelTags: []string{"py3-none-any"}}).
				SetMetadata("a", "2.0", index.PackageMetadata{TagsCaptured: true, WheelTags: []string{"cp39-cp39-win_amd64"}})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t)
			if name == "no compatible wheel" {
				target, err := buildTarget(pythonSpec{full: "3.11.4", major: 3, minor: 11}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if opts.WheelTags, err = wheelTagFilter(target, "cp311 linux"); err != nil {
					t.Fatal(err)
				}
			}
			got, extra := extraSolves(t, 12, mk(), opts, "a")
			if !reflect.DeepEqual(got, base) {
				t.Errorf("got %v, want %v", got, base)
			}
			if extra != 0 {
				t.Errorf("ran %d re-solves for a version nothing can pick", extra)
			}
		})
	}
}

// A level whose first version is unusable is still raised to, through its
// next usable version; a level with no usable version is skipped.
func TestUpgradeRootsFloorsALevelAtItsFirstUsableVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").
		AddVersion("a", "3.1").
		AddVersion("b", "1.0").
		AddVersion("b", "1.1", "a<2").
		SetUnavailable("a", "2.0").AddVersion("a", "2.1").
		SetUnavailable("a", "3.0").
		SetUnavailable("a", "4.0")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b")
	if want := map[string]string{"a": "3.1", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// With every version above the pin unusable but one level, the first re-solve
// is for that level.
func TestUpgradeRootsFirstVersionOfLevelUnusable(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").SetUnavailable("a", "2.0").AddVersion("a", "2.1").
		AddVersion("b", "1.0").AddVersion("b", "1.1", "a<2")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b")
	if want := map[string]string{"a": "2.1", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A higher epoch is newer than any release without one, so a root capped below
// it is behind.
func TestUpgradeRootsEpochIsBehind(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").AddVersion("a", "2.0").AddVersion("a", "1!0.5").
		AddVersion("b", "1.0").AddVersion("b", "1.1", "a<1!0")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b")
	if want := map[string]string{"a": "1!0.5", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// For a 0.x package the minor is the level.
func TestUpgradeRootsZeroXLevels(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "0.1").AddVersion("a", "0.2").AddVersion("a", "0.3").
		AddVersion("b", "1.0").AddVersion("b", "1.1", "a<0.2")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b")
	if want := map[string]string{"a": "0.3", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A pre-release pin is raised among pre-releases.
func TestUpgradeRootsPreReleasePin(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0a1").AddVersion("a", "2.0a1").AddVersion("a", "3.0a1").
		AddVersion("b", "1.0").AddVersion("b", "1.1", "a<2")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a>=1.0a1", "b")
	if want := map[string]string{"a": "3.0a1", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A local version label does not change its level.
func TestUpgradeRootsLocalVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").AddVersion("a", "2.0+cpu").
		AddVersion("b", "1.0").AddVersion("b", "1.1", "a<2")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b")
	if want := map[string]string{"a": "2.0+cpu", "b": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A re-solve that fails still spends budget.
func TestUpgradeRootsFailedResolveConsumesBudget(t *testing.T) {
	idx := upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").AddVersion("c", "3.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2")
	// b>=2 forces a<2, so raising a fails; with one solve of budget, c is never tried.
	got, extra := extraSolves(t, 1, idx, testOptions(t), "a", "b>=2", "c", "d")
	if extra != 1 {
		t.Errorf("ran %d re-solves, want 1", extra)
	}
	if want := map[string]string{"a": "1.0", "b": "2.0", "c": "1.0", "d": "2.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A root a re-solve cannot move is only tried once, and the budget keeps going.
func TestUpgradeRootsFailedResolveThenNextRoot(t *testing.T) {
	idx := upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").AddVersion("c", "3.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2")
	got, _ := extraSolves(t, 12, idx, testOptions(t), "a", "b>=2", "c", "d")
	if got["c"] != "3.0" {
		t.Errorf("c was not raised after a failed re-solve for a: %v", got)
	}
}

// Cancelling as the first re-solve is about to start returns the base answer.
func TestUpgradeRootsCancelInHookReturnsBaseExactly(t *testing.T) {
	idx := upgradeFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	solves := 0
	defer resolver.OnSolve(func() {
		solves++
		if solves == 2 {
			cancel()
		}
	})()
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	res, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b"), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), (map[string]string{"a": "1.0", "b": "2.0"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want the base %v", got, want)
	}
	if solves != 2 {
		t.Errorf("ran %d solves, want exactly 2 (base and the cut one)", solves)
	}
}

// Cancelling as the second re-solve starts keeps the first re-solve's gain.
func TestUpgradeRootsCancelKeepsEarlierGain(t *testing.T) {
	idx := upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").AddVersion("c", "3.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	solves := 0
	defer resolver.OnSolve(func() {
		solves++
		if solves == 3 {
			cancel()
		}
	})()
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	res, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b", "c", "d"), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "3.0", "b": "1.0", "c": "1.0", "d": "2.0"}
	if got := pins(t, res); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if solves != 3 {
		t.Errorf("ran %d solves, want exactly 3", solves)
	}
}
