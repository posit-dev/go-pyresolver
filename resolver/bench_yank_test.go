// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/requirement"
)

// BenchmarkResolveYankSnapshot times cold resolves against a snapshot that records
// yanks (YANK_BENCH_RSF), with YankExemptTransitivePins off and on.
func BenchmarkResolveYankSnapshot(b *testing.B) {
	path := os.Getenv("YANK_BENCH_RSF")
	if path == "" {
		b.Skip("YANK_BENCH_RSF not set")
	}
	file, err := pypirsf.Open(path)
	if err != nil {
		b.Fatalf("pypirsf.Open(%s): %v", path, err)
	}
	b.Cleanup(func() { _ = file.Close() })
	if !index.YanksCaptured(freshIndex(b, file)) {
		b.Fatalf("%s does not record yanks; the numbers would mean nothing", path)
	}

	sets := [][]string{
		{"django"},
		{"pandas", "scikit-learn"},
		{"apache-airflow"},
		{"boto3", "requests"},
	}
	for _, set := range sets {
		reqs := mustRequirements(b, set...)
		b.Run(strings.Join(set, "+"), func(b *testing.B) {
			b.Run("off", func(b *testing.B) { benchYankResolve(b, file, reqs, false) })
			b.Run("on", func(b *testing.B) { benchYankResolve(b, file, reqs, true) })
		})
	}
}

// benchYankResolve is one cold resolve per iteration, reporting solves per op.
func benchYankResolve(b *testing.B, file *pypirsf.File, reqs []requirement.Requirement, exempt bool) {
	ctx := context.Background()
	opts := testOptions(b)
	opts.YankExemptTransitivePins = exempt
	count, restore := resolver.CountSolves()
	defer restore()

	var iters, pinned int
	logged := false
	b.ReportAllocs()
	for b.Loop() {
		fresh := freshIndex(b, file)
		res, err := resolver.Resolve(ctx, reqs, fresh, opts)

		b.StopTimer()
		if n, ok := yankOutcome(b, res, err); ok {
			pinned = n
		} else if !logged {
			b.Logf("did not resolve: %v", err)
			logged = true
		}
		iters++
		b.StartTimer()
	}
	b.ReportMetric(float64(count())/float64(iters), "solves/op")
	b.ReportMetric(float64(pinned), "pinned")
}

// yankOutcome returns the pinned count, or false on a ResolutionError; any other error fails.
func yankOutcome(b *testing.B, res *resolver.Resolution, err error) (int, bool) {
	var re *resolver.ResolutionError
	switch {
	case errors.As(err, &re):
		return 0, false
	case err != nil:
		b.Fatalf("Resolve: %v", err)
	}
	return len(res.Pinned), true
}
