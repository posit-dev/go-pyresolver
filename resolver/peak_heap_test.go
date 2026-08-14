// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// TestPeakHeapDuringOneResolve reports the PEAK heap a single resolution reaches,
// which is the number `-benchmem` cannot give and the one the ranked-list memo
// actually moves in the wrong direction.
//
// # Why peak and not allocation volume
//
// The memo trades churn for retention: it holds the parsed version list of every
// package in the closure for the whole resolve, where those were previously
// transient garbage. Total allocation falls sharply -- app-set goes from 1.44M
// allocations to 124k -- and `B/op` falls with it, but B/op is CUMULATIVE
// allocation, not high-water mark. Those two can move in opposite directions, and
// here they were expected to. A server sizing itself for concurrent resolutions
// cares about the high-water mark.
//
// So it is measured rather than left as a documented unknown.
//
// # How, and what the number is not
//
// A sampler goroutine polls runtime.ReadMemStats while one resolution runs and
// keeps the maximum HeapInuse, against a baseline taken after a forced GC. That
// is a SAMPLED maximum: ReadMemStats stops the world, so the sampling interval
// bounds how sharp a spike this can see, and a very short resolve yields few
// samples. It is a floor on the true peak, not the true peak.
//
// ⚠️ It also perturbs what it measures -- stopping the world repeatedly changes GC
// timing, so the number is not comparable to an unsampled run's wall time. Only
// the before/after comparison is meaningful, and both sides are sampled the same
// way.
//
// Skipped unless GPR_PEAK is set: it is slow, it is deliberately perturbing, and
// it asserts nothing.
func TestPeakHeapDuringOneResolve(t *testing.T) {
	if os.Getenv("GPR_PEAK") == "" {
		t.Skip("set GPR_PEAK=1 to measure peak heap")
	}

	file, excerpt := benchSnapshotT(t)
	ctx := context.Background()

	for _, entry := range benchCorpus {
		t.Run(entry.Name, func(t *testing.T) {
			reqs := mustRequirements(t, entry.Requirements...)
			opts := testOptions(t)

			idx, err := index.NewRSFIndex(file, "production")
			if err != nil {
				t.Fatalf("NewRSFIndex: %v", err)
			}

			// Warm the index, so what is measured is the resolution's own heap
			// rather than the snapshot decode it happens to trigger first.
			if _, err := resolver.Resolve(ctx, reqs, idx, opts); err != nil && !entry.WantFailure {
				if !excerpt {
					t.Fatalf("warm-up Resolve: %v", err)
				}
			}

			runtime.GC()
			var base runtime.MemStats
			runtime.ReadMemStats(&base)

			var peak atomic.Uint64
			peak.Store(base.HeapInuse)
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				var m runtime.MemStats
				for {
					select {
					case <-stop:
						return
					default:
					}
					runtime.ReadMemStats(&m)
					for {
						cur := peak.Load()
						if m.HeapInuse <= cur || peak.CompareAndSwap(cur, m.HeapInuse) {
							break
						}
					}
					time.Sleep(200 * time.Microsecond)
				}
			}()

			res, err := resolver.Resolve(ctx, reqs, idx, opts)
			close(stop)
			<-done

			if err != nil && !entry.WantFailure && !excerpt {
				t.Fatalf("Resolve: %v", err)
			}
			pins := 0
			if res != nil {
				pins = len(res.Pinned)
			}

			t.Logf("%-16s baseline %6.1f MB  peak %6.1f MB  delta %6.1f MB  (%d pins)",
				entry.Name,
				float64(base.HeapInuse)/(1<<20),
				float64(peak.Load())/(1<<20),
				float64(peak.Load()-base.HeapInuse)/(1<<20),
				pins)
		})
	}
}

// benchSnapshotT is benchSnapshot for a *testing.T rather than a *testing.B.
// Same contract, including that a missing file is a failure rather than a skip.
func benchSnapshotT(t *testing.T) (*pypirsf.File, bool) {
	t.Helper()

	path := os.Getenv(benchFileEnv)
	excerpt := path == ""
	if excerpt {
		path = benchFixturePath
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("cannot expand %q: %v", path, err)
		}
		path = filepath.Join(home, path[2:])
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, excerpt
}
