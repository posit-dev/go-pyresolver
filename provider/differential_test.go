// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// TestCandidatesAgreeWithAnExactCountOnTheRealIndex is the differential that
// licenses the found/rank split, run against real published metadata rather than
// fixtures.
//
// For every sampled package it requires that:
//
//   - found agrees EXACTLY with the exact implementation. This is the
//     correctness-bearing half: a disagreement means a range is being forbidden
//     that is fine, or offered when nothing is usable.
//   - best is IDENTICAL. Ranking before the usability walk is what makes this true,
//     because candidate.Rank is a stable sort over a pairwise Less and therefore
//     orders a subset consistently with the superset it came from. If that ever
//     stops holding, this is the test that says so.
//   - rank never UNDER-counts the usable versions. Over-counting is by design;
//     under-counting would make the solver prefer this package over one that
//     genuinely has fewer candidates.
//
// Set PYPIRSF_TEST_FILE to a full snapshot for a real run. Without it this uses the
// committed excerpt, which is small but is still real published metadata, so the
// test is meaningful in CI rather than skipped. GPR_SAMPLE and GPR_SEED bound and
// reproduce the sample.
func TestCandidatesAgreeWithAnExactCountOnTheRealIndex(t *testing.T) {
	path := os.Getenv("PYPIRSF_TEST_FILE")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("expand home: %v", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		path = "../index/testdata/pypi-trimmed.rsf"
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	names := file.Packages()
	sort.Strings(names)

	// Default 0 means "all of them", which is what the excerpt wants.
	limit := 0
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	seed := int64(1)
	if s := os.Getenv("GPR_SEED"); s != "" {
		if seed, err = strconv.ParseInt(s, 10, 64); err != nil {
			t.Fatalf("GPR_SEED=%q: %v", s, err)
		}
	}

	sampled := names
	if limit > 0 && limit < len(names) {
		rng := rand.New(rand.NewSource(seed))
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}
	t.Logf("snapshot %s holds %d packages; comparing %d (seed %d)",
		path, len(names), len(sampled), seed)

	// A Provider per side, so neither sees the other's Unusable records. The index
	// is shared on purpose: its memo is warm for both, which keeps the comparison
	// about call SHAPE rather than about who ran first.
	p := provider.New(context.Background(), idx, testOptions(t))
	ref := provider.New(context.Background(), idx, testOptions(t))

	var compared, available, overCounted int
	for _, name := range sampled {
		pkg := provider.Project(index.PackageName(name))

		gotBest, gotFound, gotRank, gotErr := p.Candidates(pkg, pep440set.All())
		wantBest, wantFound, wantCount, wantErr := ref.ExactCandidates(pkg, pep440set.All())

		// ⚠️ Errors are compared in ONE direction only, deliberately.
		//
		// p.usable returns an error when the index cannot answer, and the short-circuit
		// walk examines a SUBSET of the versions the exhaustive one does. So the
		// exhaustive reference can hit an unreadable older version that the real path
		// never reaches, and reporting that as a disagreement would be asserting the
		// exact property this design gives up. The other direction still holds and is
		// worth pinning: anything the short-circuit walk errors on, the exhaustive walk
		// must also have errored on, because it looked at strictly more.
		if gotErr != nil && wantErr == nil {
			t.Errorf("%s: the short-circuit walk errored (%v) where the exhaustive one did "+
				"not, but it examines strictly fewer versions", name, gotErr)
			continue
		}
		if gotErr != nil || wantErr != nil {
			continue
		}
		compared++

		if gotFound != wantFound {
			t.Errorf("%s: found = %v, exact found = %v — the correctness-bearing half disagrees",
				name, gotFound, wantFound)
			continue
		}
		if !gotFound {
			continue
		}
		available++

		if !gotBest.Equal(wantBest) {
			t.Errorf("%s: best = %v, exact best = %v — ranking before the usability walk is "+
				"what makes these identical", name, gotBest, wantBest)
		}
		if gotRank < wantCount {
			t.Errorf("%s: rank = %d but %d versions are usable — rank may over-count and must "+
				"never under-count", name, gotRank, wantCount)
		}
		if gotRank > wantCount {
			overCounted++
		}
	}

	t.Logf("compared %d packages, %d with something available, %d where rank over-counted",
		compared, available, overCounted)
	if compared == 0 {
		t.Fatal("compared nothing, so this would have passed vacuously")
	}
}
