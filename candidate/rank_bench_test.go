// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/version"
)

// ascending builds n distinct versions in ascending order, which is the shape
// index.RSFIndex actually returns.
func ascending(tb testing.TB, n int) []version.Version {
	tb.Helper()
	out := make([]version.Version, 0, n)
	for i := 0; i < n; i++ {
		v, err := version.Parse(fmt.Sprintf("%d.%d.%d", i/10000, (i/100)%100, i%100))
		if err != nil {
			tb.Fatalf("parse: %v", err)
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LessThan(out[j]) })
	return out
}

func shuffled(tb testing.TB, n int) []version.Version {
	tb.Helper()
	out := ascending(tb, n)
	rng := rand.New(rand.NewSource(11))
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// BenchmarkRank isolates the mechanism the resolution benchmark only shows in
// aggregate: what Rank costs on each input shape, at each size that matters.
//
// The three shapes are not equally interesting. `ascending` is what real index
// output looks like under the default Newest policy and is the case the fast
// path exists for. `descending` is already in policy order and takes the other
// fast path. `shuffled` is the case that still has to sort, and it is here to
// bound the REGRESSION -- the detection pass is wasted work there, and this
// says how much.
func BenchmarkRank(b *testing.B) {
	const pkg = index.PackageName("x")

	for _, n := range []int{8, 64, 512, 4096, 14000} {
		asc := ascending(b, n)
		desc := make([]version.Version, n)
		for i := range asc {
			desc[i] = asc[n-1-i]
		}
		shuf := shuffled(b, n)

		for _, shape := range []struct {
			name string
			in   []version.Version
		}{
			{"ascending", asc},
			{"descending", desc},
			{"shuffled", shuf},
		} {
			b.Run(fmt.Sprintf("n=%d/%s/fast", n, shape.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = Rank(pkg, shape.in, nil)
				}
			})
			b.Run(fmt.Sprintf("n=%d/%s/sort", n, shape.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = rankBySort(pkg, shape.in, nil)
				}
			})
		}
	}
}

// BenchmarkPolicyLessCalls counts comparisons rather than timing them, which is
// the number the fast path actually changes. Reported as a metric so it appears
// on the benchmark line next to the time.
func BenchmarkPolicyLessCalls(b *testing.B) {
	const pkg = index.PackageName("x")

	for _, n := range []int{64, 4096, 14000} {
		asc := ascending(b, n)
		shuf := shuffled(b, n)

		for _, shape := range []struct {
			name string
			in   []version.Version
		}{
			{"ascending", asc},
			{"shuffled", shuf},
		} {
			b.Run(fmt.Sprintf("n=%d/%s", n, shape.name), func(b *testing.B) {
				var fast, slow int
				b.ReportAllocs()
				for b.Loop() {
					fast, slow = 0, 0
					_ = Rank(pkg, shape.in, &countingPolicy{n: &fast})
					_ = rankBySort(pkg, shape.in, &countingPolicy{n: &slow})
				}
				b.ReportMetric(float64(fast), "less-fast")
				b.ReportMetric(float64(slow), "less-sort")
			})
		}
	}
}

// countingPolicy is Newest with a tally.
type countingPolicy struct{ n *int }

func (c *countingPolicy) Less(pkg index.PackageName, a, b version.Version) bool {
	*c.n++
	return Newest{}.Less(pkg, a, b)
}

// firstByScan finds the top-ranked element with a single linear pass and no
// allocation: the partial-selection idea, in its cheapest form.
//
// Under a strict weak ordering this returns exactly rankBySort(vs)[0] -- it keeps
// the earliest element that nothing later is STRICTLY before, which is the
// stable sort's minimum including its tie-break. It is here to be measured, not
// to be used.
func firstByScan(pkg index.PackageName, vs []version.Version, p Policy) (version.Version, bool) {
	if len(vs) == 0 {
		return version.Version{}, false
	}
	if p == nil {
		p = Newest{}
	}
	best := 0
	for i := 1; i < len(vs); i++ {
		if p.Less(pkg, vs[i], vs[best]) {
			best = i
		}
	}
	return vs[best], true
}

// BenchmarkSelectFirst compares the three ways of answering the question
// Provider.Candidates actually asks -- "which version do I try first?" -- at the
// sizes real packages have.
//
// # Why this exists
//
// The obvious reading of the profile is "a full sort to consume one element is
// wasteful, use a heap or a bounded selection". This measures that reading
// against the two alternatives, because it turns out to be the wrong lever:
//
//   - sort:   rankBySort then take [0]. What every call used to do.
//   - scan:   one linear pass, n-1 comparisons, zero allocation. The partial-
//     selection idea.
//   - memo:   the list is already ranked, so take [0]. ZERO comparisons, which
//     is what a per-package memo buys and what no per-call algorithm can match.
//
// Partial selection is dominated on the axis this measures: it costs O(n)
// COMPARISONS per call where the memo costs none, and the solver asks about the
// same package 2.4 to 4.8 times per resolution. It is worth measuring anyway,
// because "the memo beats it" is a claim about a ratio and the ratio is what
// decides whether the memo's retained memory is worth paying for.
//
// ⚠️ "O(1) versus O(n)" is a claim about RANKING, not about a Candidates call,
// and an earlier draft of this comment blurred them. Candidates does not break
// out of its walk -- rank is the in-range count, so every version is still tested
// against allowed on every call. The call stays O(n) in pep440set.Contains
// whichever selection strategy is used, which is precisely why Contains is 28.6%
// of the call in the profile that follows this change. What the memo removes is
// the comparisons, not the walk.
//
// ⚠️ scan answers a STRICTLY EASIER question than the other two. It finds the
// single best element and cannot produce the second-best, which Candidates needs
// whenever the best version turns out to be unusable. So its number here is a
// LOWER bound on what a real partial-selection implementation would cost, not an
// estimate of it.
func BenchmarkSelectFirst(b *testing.B) {
	const pkg = index.PackageName("x")

	for _, n := range []int{64, 512, 4096, 14000} {
		asc := ascending(b, n)
		ranked := rankBySort(pkg, asc, nil)

		b.Run(fmt.Sprintf("n=%d/sort", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = rankBySort(pkg, asc, nil)[0]
			}
		})
		b.Run(fmt.Sprintf("n=%d/scan", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = firstByScan(pkg, asc, nil)
			}
		})
		b.Run(fmt.Sprintf("n=%d/memo", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = ranked[0]
			}
		})
	}
}

// TestFirstByScanAgreesWithTheSort keeps the benchmark above honest: an
// alternative that got a different answer would be timing the wrong algorithm.
func TestFirstByScanAgreesWithTheSort(t *testing.T) {
	const pkg = index.PackageName("x")

	pool := []string{"1.0", "1.0.0", "2.0", "0.9", "1.0a1", "1!1.0", "1.0.post1", "3.4.5"}
	parsed := make([]version.Version, len(pool))
	for i, s := range pool {
		parsed[i] = mustV(t, s)
	}

	rng := rand.New(rand.NewSource(4))
	for iter := 0; iter < 20000; iter++ {
		in := make([]version.Version, rng.Intn(9))
		for i := range in {
			in[i] = parsed[rng.Intn(len(parsed))]
		}
		got, ok := firstByScan(pkg, in, nil)
		want := rankBySort(pkg, in, nil)
		if len(want) == 0 {
			if ok {
				t.Fatalf("iteration %d: scan found %v in an empty list", iter, got)
			}
			continue
		}
		if !ok || got.String() != want[0].String() {
			t.Fatalf("iteration %d over %v: scan = %v, sort[0] = %v", iter, in, got, want[0])
		}
	}
}
