// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/version"
)

// rankBySort is what Rank did before the monotonicity fast path: always
// sort.SliceStable, never look at the input's shape.
//
// It is the reference the fast path is differentiated against, and it lives in
// the internal test package so it uses the SAME Policy call the real path uses
// rather than a second opinion about what ordering means.
func rankBySort(pkg index.PackageName, versions []version.Version, p Policy) []version.Version {
	if p == nil {
		p = Newest{}
	}
	out := make([]version.Version, len(versions))
	copy(out, versions)
	sort.SliceStable(out, func(i, j int) bool {
		return p.Less(pkg, out[i], out[j])
	})
	return out
}

func sameOrder(a, b []version.Version) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].String() != b[i].String() {
			return false
		}
	}
	return true
}

// TestRankFastPathAgreesWithTheSort is the synthetic half: shapes chosen to hit
// each branch, including the ones a random generator would essentially never
// produce.
//
// ⚠️ The tie cases are the point. The fast path claims stability is vacuous on a
// STRICTLY descending input, so a list with equivalent elements must not take the
// reversed branch -- and the only way to be sure is to feed it one.
func TestRankFastPathAgreesWithTheSort(t *testing.T) {
	const pkg = index.PackageName("x")

	build := func(ss ...string) []version.Version {
		out := make([]version.Version, 0, len(ss))
		for _, s := range ss {
			out = append(out, mustV(t, s))
		}
		return out
	}

	cases := []struct {
		name  string
		input []version.Version
		want  shape
	}{
		{"empty", nil, ordered},
		{"one", build("1.0"), ordered},
		{"already-descending", build("3.0", "2.0", "1.0"), ordered},
		{"strictly-ascending", build("1.0", "2.0", "3.0"), reversed},
		{"unsorted", build("2.0", "3.0", "1.0"), unordered},
		{"two-ascending", build("1.0", "2.0"), reversed},
		{"two-descending", build("2.0", "1.0"), ordered},

		// PEP 440 equal spellings: Less is false both ways, so neither strict
		// shape holds beyond the pair itself.
		{"all-equivalent", build("1.0", "1.0.0", "1.0.0.0"), ordered},
		{"ascending-with-a-tie", build("1.0", "1.0.0", "2.0"), unordered},
		{"descending-with-a-tie", build("2.0", "1.0", "1.0.0"), ordered},

		// Pre-releases and epochs, where the ordering is least obvious.
		{"prerelease-ascending", build("1.0a1", "1.0b1", "1.0rc1", "1.0"), reversed},
		{"epoch-ascending", build("1.0", "1!0.1"), reversed},
		{"local-ascending", build("1.0", "1.0+a", "1.0+b"), reversed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := monotonicity(pkg, tc.input, Newest{}); got != tc.want {
				t.Errorf("monotonicity = %v, want %v", got, tc.want)
			}
			got := Rank(pkg, tc.input, nil)
			want := rankBySort(pkg, tc.input, nil)
			if !sameOrder(got, want) {
				t.Errorf("Rank      = %v\nrankBySort = %v", got, want)
			}
		})
	}
}

// TestRankFastPathAgreesWithTheSortOnRandomLists is the fuzzing half: many small
// lists drawn from a pool with deliberate duplicates and equivalent spellings, so
// ties are common rather than incidental.
func TestRankFastPathAgreesWithTheSortOnRandomLists(t *testing.T) {
	const pkg = index.PackageName("x")

	pool := []string{
		"1.0", "1.0.0", "1.0.0.0", "1!1.0", "2.0", "2.0.1", "0.9",
		"1.0a1", "1.0b2", "1.0rc1", "1.0.post1", "1.0.dev0", "1.0+local",
		"10.0", "3.4.5", "3.4.5.6", "2026.4.28", "2026.04.28",
	}
	parsed := make([]version.Version, len(pool))
	for i, s := range pool {
		parsed[i] = mustV(t, s)
	}

	rng := rand.New(rand.NewSource(20260813))
	for iter := 0; iter < 20000; iter++ {
		n := rng.Intn(12)
		in := make([]version.Version, n)
		for i := range in {
			in[i] = parsed[rng.Intn(len(parsed))]
		}
		if !sameOrder(Rank(pkg, in, nil), rankBySort(pkg, in, nil)) {
			t.Fatalf("iteration %d disagreed on %v:\n fast = %v\n sort = %v",
				iter, in, Rank(pkg, in, nil), rankBySort(pkg, in, nil))
		}
	}
}

// TestRankFastPathAgreesWithTheSortOnTheRealIndex runs the differential over
// every package's ACTUAL published version list.
//
// This is the one that matters, because the fast path exists for the shape real
// data has: index.RSFIndex returns versions ascending and deduped, so nearly
// every list here takes the reversed branch. The synthetic tests above prove the
// branches are right; this proves the REVERSED branch real inputs take is the
// right one for them.
//
// ⚠️ It proves nothing about the ORDERED branch, and the shape counts say why.
// Against the committed excerpt: 0 unordered, 1 ordered, 135 reversed -- and that
// single "ordered" is a one-version package hitting monotonicity's len < 2 early
// return, not a multi-version list that happened to arrive in policy order.
// Because RSFIndex is ascending and Newest is descending, a real list of length
// >= 2 essentially never takes the ordered branch. That branch is covered by the
// synthetic cases above and by nothing here, which is the honest scope: the
// guard below therefore requires reversed != 0 and deliberately does not require
// ordered != 0, since demanding it would be demanding an input real data does not
// produce.
//
// Set PYPIRSF_TEST_FILE to a full snapshot; without it this uses the committed
// excerpt so CI still exercises it.
func TestRankFastPathAgreesWithTheSortOnTheRealIndex(t *testing.T) {
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

	var byShape [3]int
	var versions int
	for _, name := range sampled {
		pkg := index.PackageName(name)
		all, err := idx.Versions(t.Context(), pkg)
		if err != nil {
			continue
		}
		if len(all) == 0 {
			continue
		}
		versions += len(all)
		byShape[monotonicity(pkg, all, Newest{})]++

		if !sameOrder(Rank(pkg, all, nil), rankBySort(pkg, all, nil)) {
			t.Errorf("%s: the fast path and the sort disagree", name)
		}
	}

	t.Logf("snapshot %s: %d packages compared, %d versions; shapes: unordered %d, ordered %d, reversed %d",
		path, len(sampled), versions, byShape[unordered], byShape[ordered], byShape[reversed])

	if versions == 0 {
		t.Fatal("compared no versions, so this would have passed vacuously")
	}
	// The fast path is only worth its pass if real lists actually take it. A
	// snapshot where everything fell through to the sort would mean the
	// measurement below describes nothing.
	if byShape[reversed] == 0 {
		t.Error("no package's version list was strictly ascending, so the branch this " +
			"optimization exists for was never taken")
	}
}
