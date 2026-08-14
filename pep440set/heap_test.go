// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"runtime"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// TestRetainedHeapPerSet measures the LIVE heap a population of Sets holds
// after GC, as distinct from the churn FromSpecifiers allocates building them.
// The resolver holds one Set per term for the life of a solve, so retained
// size matters independently of allocation volume. It asserts nothing; the
// number is read from -v output.
func TestRetainedHeapPerSet(t *testing.T) {
	shapes := []string{
		">=1.2.3",
		">=1.0,<2.0",
		"==1.2.3",
		"~=2.31.0",
		"!=1.5.*,>=1.2,<3",
		">=1.21.0,!=2.0.0,!=2.0.1,<3",
	}
	const n = 30000

	specs := make([]version.Specifiers, len(shapes))
	for i, s := range shapes {
		ss, err := version.NewSpecifiers(s)
		if err != nil {
			t.Fatalf("NewSpecifiers(%q): %v", s, err)
		}
		specs[i] = ss
	}

	keep := make([]Set, 0, n)
	runtime.GC()
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	for i := 0; i < n; i++ {
		s, err := FromSpecifiers(specs[i%len(specs)])
		if err != nil {
			t.Fatal(err)
		}
		keep = append(keep, s)
	}

	runtime.GC()
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	// Signed arithmetic on purpose: the live heap can shrink across the run
	// (other tests' garbage collected between the two reads), and a uint64
	// subtraction would log that as ~1.8e19 rather than as negative.
	//
	// ⚠️ The reading is GLOBAL MemStats, so anything else allocating in the
	// process -- parallel tests, a loaded machine's timer-driven work --
	// lands in the delta. The number is only meaningful from a solo run on
	// an otherwise idle machine (`-run TestRetainedHeapPerSet -count=1`);
	// do not quote it from a full-suite run.
	delta := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	t.Logf("live heap for %d Sets: %d bytes (%.1f B/Set)",
		n, delta, float64(delta)/n)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(specs)
}
