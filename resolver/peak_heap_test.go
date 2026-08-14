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

// TestIndexRetainedHeapAfterResolve reports how much heap ONE warmed
// index.RSFIndex holds onto after a resolution has finished, which is the number
// the parsed-version memo moves and the number no benchmark reports.
//
// # Why this is the number that matters, and TestPeakHeapDuringOneResolve is not
//
// The peak test above measures the high-water mark DURING a resolve. This
// measures what survives it. They answer different questions and the parsed
// memo moves them in opposite directions: churn falls sharply (warm allocs/op
// drops 55.4% on app-set and 94.4% on wide-versions, measured at base 11da678)
// while the index's steady-state footprint rises, because parsed versions that
// used to be transient garbage now live in versionList for the life of the index.
//
// For every caller in this module that is a rounding error, because each builds
// an RSFIndex per resolve and drops it. For a long-lived server holding one index
// over a 932,861-package corpus it is the whole question, and index/rsfindex.go
// says plainly that a bound is a prerequisite for that integration. This test is
// what makes that claim a measured one.
//
// # How
//
// Warm the index with a full resolution, drop the result, force GC and read
// HeapAlloc with the index still reachable; then drop the index, force GC and
// read again. The difference is what the index alone was keeping alive.
//
// runtime.KeepAlive is what makes the first reading mean anything: without it the
// compiler is entitled to consider idx dead the moment the last method call
// returns, and both readings would measure the same thing.
//
// ⚠️ It is a LIVE-HEAP difference, not an allocator footprint. It excludes the
// mmap'd snapshot itself, which is not Go heap, and it includes whatever the
// resolution left reachable from the index -- the decoded blob cache and both
// parsed memos, not just the one under test. That is deliberate: the question a
// server operator has is "what does holding this index cost me", not "what does
// this one field cost me".
//
// Two GC cycles are forced per reading, because one is not guaranteed to reclaim
// everything that became unreachable during it -- an object finalized or
// re-queued by the first pass is only freed by the second. That is belt and
// braces rather than a fix for an observed problem, and it is cheap. The readings
// it produces are stable: five interleaved rounds against the production snapshot
// agreed to 0.01 MB on every corpus entry, on both builds.
//
// Skipped unless GPR_RETAIN is set: it is slow, it forces GC, and it asserts
// nothing. It is a measurement, run deliberately and diffed between two builds.
func TestIndexRetainedHeapAfterResolve(t *testing.T) {
	if os.Getenv("GPR_RETAIN") == "" {
		t.Skip("set GPR_RETAIN=1 to measure retained heap")
	}

	file, excerpt := benchSnapshotT(t)
	ctx := context.Background()

	// ⚠️ Say WHICH corpus, loudly, before printing a single megabyte figure.
	// Without PYPIRSF_TEST_FILE this measures the 139-package committed excerpt
	// and produces numbers nothing like the published table -- silently, because
	// every other test here treats the excerpt as a legitimate fixture and
	// benchSnapshotT returns the flag without insisting anyone read it. A
	// retention figure whose corpus is unstated is not a figure.
	corpus := "PRODUCTION snapshot from PYPIRSF_TEST_FILE"
	if excerpt {
		corpus = "COMMITTED EXCERPT -- set PYPIRSF_TEST_FILE for figures comparable to the CHANGELOG's"
	}
	t.Logf("corpus: %s, %d packages", corpus, file.Len())

	for _, entry := range benchCorpus {
		t.Run(entry.Name, func(t *testing.T) {
			reqs := mustRequirements(t, entry.Requirements...)
			opts := testOptions(t)

			idx, err := index.NewRSFIndex(file, "production")
			if err != nil {
				t.Fatalf("NewRSFIndex: %v", err)
			}

			res, err := resolver.Resolve(ctx, reqs, idx, opts)
			if err != nil && !entry.WantFailure && !excerpt {
				t.Fatalf("Resolve: %v", err)
			}
			pins := 0
			if res != nil {
				pins = len(res.Pinned)
			}
			res = nil //nolint:ineffassign,wastedassign // dropped so only the index is measured

			withIndex := liveHeap()
			runtime.KeepAlive(idx)
			idx = nil //nolint:ineffassign,wastedassign // the point of the next reading
			withoutIndex := liveHeap()

			retained := float64(withIndex) - float64(withoutIndex)
			t.Logf("%-16s retained by the index %7.2f MB  (live %6.1f -> %6.1f MB, %d pins)",
				entry.Name,
				retained/(1<<20),
				float64(withIndex)/(1<<20),
				float64(withoutIndex)/(1<<20),
				pins)
		})
	}
}

// liveHeap returns HeapAlloc after two forced collections. See the note above on
// why one is not enough.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
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
