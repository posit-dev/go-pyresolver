// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"reflect"
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
