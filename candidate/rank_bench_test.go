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
