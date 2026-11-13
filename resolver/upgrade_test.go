// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

func resolveUp(t *testing.T, budget int, idx index.MetadataIndex, reqs ...string) map[string]string {
	t.Helper()
	opts := testOptions(t)
	opts.UpgradeRoots = budget
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pins(t, res)
}

// b 2.0 caps a below 2, so the newest-first answer leaves a two majors behind.
func upgradeFixture() *index.MockIndex {
	return index.NewMockIndex("test").
		AddVersion("a", "1.0").AddVersion("a", "2.0").AddVersion("a", "3.0").
		AddVersion("b", "1.0").
		AddVersion("b", "2.0", "a<2")
}

func TestUpgradeRootsRaisesARootThatIsMajorsBehind(t *testing.T) {
	idx := upgradeFixture()
	if got := resolveUp(t, 0, idx, "a", "b"); !reflect.DeepEqual(got, map[string]string{"a": "1.0", "b": "2.0"}) {
		t.Fatalf("off: %v", got)
	}
	if got := resolveUp(t, 12, idx, "a", "b"); !reflect.DeepEqual(got, map[string]string{"a": "3.0", "b": "1.0"}) {
		t.Errorf("on: %v, want a 3.0 and b 1.0", got)
	}
}

// Raising a would drop b four majors, which is worse than a being one behind.
func TestUpgradeRootsRefusesAMoveThatLowersAnotherRoot(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("a", "1.0").AddVersion("a", "2.0").
		AddVersion("b", "1.0").
		AddVersion("b", "2.0", "a<2").AddVersion("b", "3.0", "a<2").
		AddVersion("b", "4.0", "a<2").AddVersion("b", "5.0", "a<2")
	want := map[string]string{"a": "1.0", "b": "5.0"}
	if got := resolveUp(t, 12, idx, "a", "b"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The pass never runs more extra solves than its budget.
func TestUpgradeRootsBudget(t *testing.T) {
	idx := upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").AddVersion("c", "3.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2")
	reqs := []string{"a", "b", "c", "d"}
	base := map[string]string{"a": "1.0", "b": "2.0", "c": "1.0", "d": "2.0"}
	if got := resolveUp(t, 0, idx, reqs...); !reflect.DeepEqual(got, base) {
		t.Fatalf("off: %v", got)
	}
	count, restore := resolver.CountSolves()
	defer restore()
	for _, budget := range []int{1, 2, 3, 40} {
		before := count()
		got := resolveUp(t, budget, idx, reqs...)
		if used := count() - before - 1; used > budget {
			t.Errorf("budget %d: ran %d extra solves", budget, used)
		}
		if reflect.DeepEqual(got, base) {
			t.Errorf("budget %d: nothing was raised: %v", budget, got)
		}
	}
	if got := resolveUp(t, 1, idx, reqs...); got["a"] != "3.0" {
		t.Errorf("budget 1 should spend its solve on a, the first of the roots tied furthest behind: %v", got)
	}
}

// A context cancelled during the pass returns the base answer, not an error.
func TestUpgradeRootsCancelledReturnsBase(t *testing.T) {
	idx := upgradeFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	solves := 0
	defer resolver.OnSolve(func() {
		solves++
		if solves == 2 { // the first re-solve is about to start
			cancel()
		}
	})()
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	res, err := resolver.Resolve(ctx, mustRequirements(t, "a", "b"), idx, opts)
	if err != nil {
		t.Fatalf("a cut pass must not fail the resolution: %v", err)
	}
	got := pins(t, res)
	base := map[string]string{"a": "1.0", "b": "2.0"}
	raised := map[string]string{"a": "3.0", "b": "1.0"}
	if !reflect.DeepEqual(got, base) && !reflect.DeepEqual(got, raised) {
		t.Errorf("got %v: neither the base nor the raised answer", got)
	}
	if solves > 2 {
		t.Errorf("ran %d solves after cancel", solves)
	}
}

// Cancelled right after the base solve, before the pass reads anything.
func TestUpgradeRootsCancelledBeforePassReturnsBase(t *testing.T) {
	idx := upgradeFixture()
	reqs := mustRequirements(t, "a", "b")
	count := &callIndex{MetadataIndex: idx}
	if _, err := resolver.Resolve(context.Background(), reqs, count, testOptions(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cut := &callIndex{MetadataIndex: idx, cancelAt: count.calls + 1, cancel: cancel}
	opts := testOptions(t)
	opts.UpgradeRoots = 12
	res, err := resolver.Resolve(ctx, reqs, cut, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, base := pins(t, res), (map[string]string{"a": "1.0", "b": "2.0"}); !reflect.DeepEqual(got, base) {
		t.Errorf("got %v, want the base %v", got, base)
	}
}

// callIndex counts Versions calls and cancels on call number cancelAt.
type callIndex struct {
	index.MetadataIndex
	calls    int
	cancelAt int
	cancel   func()
}

func (c *callIndex) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	c.calls++
	if c.calls == c.cancelAt {
		c.cancel()
	}
	return c.MetadataIndex.Versions(ctx, pkg)
}

func TestUpgradeRootsIsDeterministic(t *testing.T) {
	idx := upgradeFixture().
		AddVersion("c", "1.0").AddVersion("c", "2.0").
		AddVersion("d", "1.0").AddVersion("d", "2.0", "c<2")
	first := resolveUp(t, 12, idx, "a", "b", "c", "d")
	for i := 0; i < 20; i++ {
		if got := resolveUp(t, 12, idx, "a", "b", "c", "d"); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: %v != %v", i, got, first)
		}
	}
}

// randomIndex builds packages p0..p(n-1); a package requires only later ones,
// so every index is acyclic. The root asks for every package.
func randomIndex(rng *rand.Rand, n int) (*index.MockIndex, []string) {
	idx := index.NewMockIndex("test")
	var roots []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("p%d", i)
		roots = append(roots, name)
		for v := 1; v <= 1+rng.Intn(5); v++ {
			var reqs []string
			for j := i + 1; j < n; j++ {
				if rng.Intn(3) != 0 {
					continue
				}
				switch rng.Intn(3) {
				case 0:
					reqs = append(reqs, fmt.Sprintf("p%d<%d", j, 1+rng.Intn(4)))
				case 1:
					reqs = append(reqs, fmt.Sprintf("p%d>=%d", j, 1+rng.Intn(3)))
				default:
					reqs = append(reqs, fmt.Sprintf("p%d", j))
				}
			}
			idx.AddVersion(name, fmt.Sprintf("%d.0", v), reqs...)
		}
	}
	return idx, roots
}

// Every answer, with the pass on, satisfies the requirements of every package it pins.
func TestUpgradeRootsAnswersAreValid(t *testing.T) {
	changed := 0
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		idx, roots := randomIndex(rng, 3+rng.Intn(4))
		opts := testOptions(t)
		baseRes, err := resolver.Resolve(context.Background(), mustRequirements(t, roots...), idx, opts)
		opts.UpgradeRoots = 12
		res, upErr := resolver.Resolve(context.Background(), mustRequirements(t, roots...), idx, opts)
		if (err == nil) != (upErr == nil) {
			t.Fatalf("seed %d: base err %v, upgrade err %v", seed, err, upErr)
		}
		if err != nil {
			continue
		}
		checkValid(t, seed, idx, roots, res)
		if !reflect.DeepEqual(pins(t, baseRes), pins(t, res)) {
			changed++
		}
	}
	if changed == 0 {
		t.Error("the pass never changed an answer: the property is not being exercised")
	}
}

func checkValid(t *testing.T, seed int64, idx index.MetadataIndex, roots []string, res *resolver.Resolution) {
	t.Helper()
	for _, r := range roots {
		if _, ok := res.Pinned[index.NewPackageName(r)]; !ok {
			t.Fatalf("seed %d: root %s unpinned", seed, r)
		}
	}
	for name, v := range res.Pinned {
		meta, err := idx.Metadata(context.Background(), name, v)
		if err != nil {
			t.Fatalf("seed %d: %s %s: %v", seed, name, v, err)
		}
		for _, q := range meta.RequiresDist {
			dep, ok := res.Pinned[index.NewPackageName(q.Name)]
			if !ok || (q.Specifiers.Len() > 0 && !q.Specifiers.ContainsVersion(dep)) {
				t.Fatalf("seed %d: %s %s requires %s, answer has %v", seed, name, v, q.String(), dep)
			}
		}
	}
}
