// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// BenchmarkCandidatesRuledOutOneByOne reproduces a solve that turns a
// package's versions down one per round: each round asks Candidates again with
// one more `!=` hole, newest first. One op is the whole sweep of n rounds.
func BenchmarkCandidatesRuledOutOneByOne(b *testing.B) {
	for _, n := range []int{250, 500, 1000, 2500} {
		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			idx := index.NewMockIndex("bench")
			vers := make([]string, n)
			for i := range vers {
				vers[i] = fmt.Sprintf("1.%d.%d", i/100, i%100)
				idx.AddVersion("pkg", vers[i])
			}
			opts := testOptions(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := provider.New(context.Background(), idx, opts)
				allowed := pep440set.All()
				for r := 0; r < n; r++ {
					best, found, _, err := p.Candidates(provider.Project("pkg"), allowed)
					if err != nil || !found {
						b.Fatalf("round %d: found=%v err=%v", r, found, err)
					}
					allowed = allowed.Intersect(best.Complement())
				}
			}
		})
	}
}
