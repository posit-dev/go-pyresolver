// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
)

func rankMemoIndex() *index.MockIndex {
	idx := index.NewMockIndex("test")
	for _, v := range []string{"1.0", "1.1", "2.0", "2.1rc1"} {
		idx.AddVersion("app", v, "lib>=1", "tool")
	}
	for _, v := range []string{"1.0", "2.0", "3.0", "3.1b1"} {
		idx.AddVersion("lib", v)
	}
	// Only pre-releases: PrereleaseIfNoFinal must still allow them.
	idx.AddVersion("tool", "0.1a1").AddVersion("tool", "0.2b1")
	return idx
}

func solveOnce(t *testing.T, idx index.MetadataIndex, memo *provider.RankMemo, noFinal bool) (map[string]string, error) {
	t.Helper()
	opts := testOptions(t)
	opts.RankMemo = memo
	opts.PrereleaseIfNoFinal = noFinal
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "app"), idx, opts)
	if err != nil {
		return nil, err
	}
	return pins(t, res), nil
}

// A RankMemo shared across solves changes no answer, and a later solve does not
// ask the index for version lists again.
func TestRankMemoSharedAcrossSolvesGivesIdenticalAnswers(t *testing.T) {
	for _, noFinal := range []bool{false, true} {
		base := newCountingIndex(rankMemoIndex())
		want, err := solveOnce(t, base, nil, noFinal)
		if err != nil {
			t.Fatalf("noFinal=%v: base solve: %v", noFinal, err)
		}
		if base.versions.Load() == 0 {
			t.Fatal("base solve never asked for a version list")
		}

		memo := &provider.RankMemo{}
		idx := newCountingIndex(rankMemoIndex())
		for i := 0; i < 3; i++ {
			before := idx.versions.Load()
			got, err := solveOnce(t, idx, memo, noFinal)
			if err != nil {
				t.Fatalf("noFinal=%v solve %d: %v", noFinal, i, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("noFinal=%v solve %d: pins %v, want %v", noFinal, i, got, want)
			}
			if i > 0 && idx.versions.Load() != before {
				t.Errorf("noFinal=%v solve %d: asked the index for %d version lists despite a warm memo",
					noFinal, i, idx.versions.Load()-before)
			}
		}
	}
}

// Solves running at once may share one memo (run with -race).
func TestRankMemoConcurrentSolves(t *testing.T) {
	want, err := solveOnce(t, rankMemoIndex(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	memo := &provider.RankMemo{}
	idx := rankMemoIndex()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				got, err := solveOnce(t, idx, memo, true)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("pins %v (err %v), want %v", got, err, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func solveReqs(t *testing.T, idx index.MetadataIndex, memo *provider.RankMemo, noFinal bool, reqs ...string) (map[string]string, error) {
	t.Helper()
	return solveReqsAllow(t, idx, memo, noFinal, nil, reqs...)
}

func solveReqsAllow(t *testing.T, idx index.MetadataIndex, memo *provider.RankMemo, noFinal bool, allow []index.PackageName, reqs ...string) (map[string]string, error) {
	t.Helper()
	opts := testOptions(t)
	opts.AllowPrerelease = allow
	opts.RankMemo = memo
	opts.PrereleaseIfNoFinal = noFinal
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
	if err != nil {
		return nil, err
	}
	return pins(t, res), nil
}

func prereleaseMixIndex() *index.MockIndex {
	idx := index.NewMockIndex("test")
	for _, v := range []string{"1.0", "2.0", "3.1b1"} {
		idx.AddVersion("lib", v)
	}
	idx.AddVersion("other", "1.0", "lib>=1")
	// Names a lib pre-release only transitively, once it is selected.
	idx.AddVersion("other2", "1.0", "lib>=3.1a1")
	idx.AddVersion("only", "0.1a1").AddVersion("only", "0.2b1")
	return idx
}

// Solves with different root requirements enable different pre-releases. A memo
// primed by one solve must not change the answer of another: the shared entry
// holds nothing that depends on the solve's pre-release set.
func TestRankMemoSolvesWithDifferentPrereleaseSetsShareSafely(t *testing.T) {
	sets := [][]string{
		{"lib"},
		{"lib>=1.0a1"},
		{"lib", "other"},
		{"lib>=1.0a1", "other"},
		{"only"},
		{"lib", "only>=0.1a1"},
		{"other2"},
		{"lib", "other2"},
		{"other2", "lib<3"},
	}
	for _, allow := range allowDims {
		for _, noFinal := range []bool{false, true} {
			for i, a := range sets {
				for j, b := range sets {
					memo := &provider.RankMemo{}
					idx := prereleaseMixIndex()
					if _, err := solveReqsAllow(t, idx, memo, noFinal, allow, a...); err != nil && !strings.Contains(err.Error(), "cannot be satisfied") {
						t.Fatalf("prime %v: %v", a, err)
					}
					want, werr := solveReqsAllow(t, prereleaseMixIndex(), nil, noFinal, allow, b...)
					got, gerr := solveReqsAllow(t, idx, memo, noFinal, allow, b...)
					if (werr == nil) != (gerr == nil) || !reflect.DeepEqual(got, want) {
						t.Errorf("allow=%v noFinal=%v primed by #%d %v, then #%d %v: memo pins %v (err %v), no memo %v (err %v)",
							allow, noFinal, i, a, j, b, got, gerr, want, werr)
					}
				}
			}
		}
	}
}

var allowDims = [][]index.PackageName{nil, {"lib"}}

// Random requirement sets against one index: one shared memo across all of them
// gives the same pins as no memo.
func TestRankMemoRandomRequirementSetsMatchNoMemo(t *testing.T) {
	pool := []string{"lib", "lib>=1.0a1", "lib<3", "lib>=2", "other", "other>=1", "only", "only>=0.1a1", "lib!=2.0", "other2"}
	rng := rand.New(rand.NewSource(1))
	for _, allow := range allowDims {
		for _, noFinal := range []bool{false, true} {
			memo := &provider.RankMemo{}
			idx := prereleaseMixIndex()
			for n := 0; n < 200; n++ {
				var reqs []string
				for _, k := range rng.Perm(len(pool))[:1+rng.Intn(3)] {
					reqs = append(reqs, pool[k])
				}
				want, werr := solveReqsAllow(t, prereleaseMixIndex(), nil, noFinal, allow, reqs...)
				got, gerr := solveReqsAllow(t, idx, memo, noFinal, allow, reqs...)
				if (werr == nil) != (gerr == nil) || !reflect.DeepEqual(got, want) {
					t.Fatalf("allow=%v noFinal=%v reqs %v: memo pins %v (err %v), no memo %v (err %v)", allow, noFinal, reqs, got, gerr, want, werr)
				}
			}
		}
	}
}

// Different requirement sets solving at once against one memo (run with -race).
func TestRankMemoConcurrentDifferentRequirementSets(t *testing.T) {
	sets := [][]string{
		{"lib"}, {"lib>=1.0a1"}, {"lib", "other"}, {"only"}, {"other2"}, {"lib", "other2"},
	}
	for _, noFinal := range []bool{false, true} {
		wants := make([]map[string]string, len(sets))
		werrs := make([]error, len(sets))
		for i, r := range sets {
			wants[i], werrs[i] = solveReqs(t, prereleaseMixIndex(), nil, noFinal, r...)
		}
		memo := &provider.RankMemo{}
		idx := prereleaseMixIndex()
		var wg sync.WaitGroup
		for g := 0; g < 12; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for n := 0; n < 5; n++ {
					for k := range sets {
						i := (k + g) % len(sets)
						got, err := solveReqs(t, idx, memo, noFinal, sets[i]...)
						if (err == nil) != (werrs[i] == nil) || !reflect.DeepEqual(got, wants[i]) {
							t.Errorf("noFinal=%v %v: pins %v (err %v), want %v (err %v)", noFinal, sets[i], got, err, wants[i], werrs[i])
							return
						}
					}
				}
			}(g)
		}
		wg.Wait()
	}
}
