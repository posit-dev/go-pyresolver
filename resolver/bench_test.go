// SPDX-License-Identifier: Apache-2.0 OR MIT

// The Phase 3 exit-gate resolution benchmark for RFD 0001
// (rstudio/package-manager#18651): cold and warm resolution wall time,
// allocations, and MetadataIndex call counts over a fixed corpus.
//
// # What cold and warm mean here
//
// Cold is a fresh index.RSFIndex over an already-open snapshot: nothing
// decoded, so every version list and every version's dependency blob is read
// and decompressed during the resolution. Warm is the same requirements
// resolved again against the same RSFIndex, whose per-package decode cache is
// now populated. Both build a new provider and run the solver from scratch --
// nothing memoizes a resolution.
//
// Opening the snapshot is measured separately (BenchmarkOpenSnapshot) and is
// deliberately NOT part of cold. pypirsf.Open scans every record to build the
// name-to-offset table, so its cost is proportional to the corpus and belongs
// to process startup; charging it to one resolution would say more about how
// many packages PyPI has than about the resolver.
//
// # There is no CDN in this measurement, and that is not an omission
//
// RFD Rev 15 put requires_dist, requires_python and provides_extra in the PyPI
// RSF. Resolution reads that file in-process: no HTTP request, no database.
// Only index.Files() is CDN-backed, and RSFIndex does not serve it at all. So a
// "cold CDN" is not a variable this gate has, and inventing one would describe
// a system that does not exist.
//
// # Corpus, environment, vantage point
//
// Corpus: benchCorpus in bench_corpus_test.go -- committed, with the reason for
// each entry. Environment: CPython 3.11.4 on linux/x86_64 (testEnv), which
// fixes every marker evaluation. Snapshot: PYPIRSF_TEST_FILE, defaulting to the
// committed excerpt index/testdata/pypi-trimmed.rsf so this runs in CI.
//
// The published numbers were measured on:
//
//	Apple M4 Max (16 cores), macOS 26.6 (25G72), go1.26.4 darwin/arm64
//	snapshot ~/.cache/ppm-rsf/prod.rsf, 981 MB, 932,861 packages, dated 2026-08-04
//
//	PYPIRSF_TEST_FILE=~/.cache/ppm-rsf/prod.rsf \
//	  go test ./resolver/ -run '^$' -bench 'BenchmarkResolve' -benchtime 10x -benchmem
//
// A benchmark never runs under `go test ./...`, so the 981 MB snapshot is never
// required by CI; the excerpt is the default and needs nothing.
//
// # Measured, 2026-08-12, on the machine above
//
// Ten iterations per entry. Times are per resolution; idx is total MetadataIndex
// calls, of which meta is Metadata; cand is the number of candidate versions the
// resolution walked; pins is the size of the resulting closure.
//
//	entry            cold ms   warm ms     idx    meta    cand   pins    alloc/op
//	single-no-deps      4.35      4.63     133     131     130      1      4.4 MB
//	small-tree         25.27     25.59     313     290     984      7       29 MB
//	extras             47.95     48.92     695     661    1658      8       51 MB
//	app-set           712.99    710.89    4837    4750    6040     18      827 MB
//	wide-versions     531.43    532.95    4566    4549    7206      7      667 MB
//	backtracking       45.56     45.14     444     435     769      4       46 MB
//	unsatisfiable       4.31      4.04      39      37     124      0      5.3 MB
//
// Opening the snapshot: 233 ms, 141 MB, for 932,861 records.
//
// # Verdict against the Phase 3 exit gate
//
// The gate in RFD 0001 Section 9 is <100 ms cold and <1 ms warm, and the RFD
// records both as estimates.
//
//   - COLD: met by five of seven entries, missed by two -- app-set at 713 ms
//     (7.1x) and wide-versions at 531 ms (5.3x).
//   - WARM: missed by all seven, from 4x (unsatisfiable, 4.0 ms) to 711x
//     (app-set). Warm is not measurably faster than cold anywhere: every entry
//     is within 3% of its cold time, which is inside the run-to-run noise.
//
// # Why warm equals cold, and what would have to change
//
// The only cache in the path is RSFIndex's decoded-blob map, and it holds the
// RAW STRINGS the RSF carries. Nothing above it is memoized: Metadata re-parses
// its PEP 508 requirements and its Requires-Python on every call, and Versions
// re-parses and re-sorts the package's whole version list on every call. So a
// warm index skips the zstd decode and repeats everything else, and the decode
// is not where the time is.
//
// From `go tool pprof -top -cum` on BenchmarkResolveWarm/app-set (5.88 s of
// samples inside Resolve):
//
//	Candidates                     5.85 s   99.5% of Resolve
//	  usable                       4.54 s   77.6%
//	    Metadata                   2.78 s   47.3%
//	      requirement.Parse        2.41 s   41.0%   (100% called from Metadata)
//	      NewSpecifiers (req-py)   0.32 s    5.4%
//	    marker.Evaluate            1.03 s   17.5%
//	    pep440set.FromSpecifiers   0.73 s   12.4%
//	  Versions                     0.91 s   15.5%   (0.84 s of it re-sorting)
//	  candidate.Rank               0.34 s    5.8%
//
// Dependencies -- the work done for versions the solver actually decides -- does
// not appear: it is under 0.5% of the resolution. Essentially all of the cost is
// deciding usability for versions that are then discarded, exactly as
// Provider.Candidates' own "Cost" note anticipated.
//
// Two changes would move these numbers, and they are different in kind:
//
//   - The CONSTANT: memoize parsed metadata per (package, version), or parse at
//     decode time so the index caches parsed records instead of strings. That is
//     ~47% of resolution CPU, plus most of the 827 MB per resolve and the GC it
//     provokes. Provider.Candidates already names this ("a memo keyed by
//     (package, version) is the obvious next step if this ever shows up in a
//     profile"); it shows up -- app-set makes 4,750 Metadata calls to pin 18
//     packages, 264 per package pinned.
//
//   - The COUNT: solver.Provider requires Candidates to return a count that is
//     zero exactly when nothing satisfies, so an exact count means testing every
//     version in the allowed range, and usable tests by computing the version's
//     full dependencies. That is what makes index calls scale with candidate
//     versions rather than with the closure -- certifi has no dependencies at
//     all and still costs 131 Metadata calls, one per released version. No
//     amount of caching removes it: it is the interface contract, and changing
//     it is a go-pubgrub conversation, not a tuning exercise.
//
// The second is what the warm target turns on. At app-set's 4,837 index calls, a
// 1 ms warm resolution allows 207 ns per call end to end, which is less than one
// regexp-driven requirement.Parse. Caching alone cannot get there; the call
// count has to fall.
//
// # A second cost profile, when the index is INCOMPLETE
//
// The same benchmark against the committed 139-package excerpt puts the
// backtracking entry at 16.7 s, 24.7 GB and 556 million allocations for ONE
// resolution -- 370x its cost against the full snapshot, on a smaller index. It
// is not a fixture artifact worth dismissing: an index whose packages are
// present but whose transitive dependencies are not is exactly the shape of a
// curated or air-gapped Package Manager repository.
//
// The cost moves somewhere else entirely. `pprof -cum` on that run:
//
//	Resolve                       5.25 s   of 21.1 s of samples
//	  term.Intersect              5.09 s   97% of Resolve
//	    pep440set.Set.Intersect   4.79 s
//	      cmpBound                4.86 s
//	        releaseKey            3.35 s
//
// Index calls are 11,029 and barely register. The work is version-set algebra
// inside the solver's conflict resolution, and under it releaseKey, which
// re-derives its ordering key from version.BaseVersion() -- a string render --
// on every bound comparison. So the two regimes have two different dominant
// costs, and a fix aimed at one does nothing for the other.
//
// No optimization is included here deliberately. The deliverable of
// rstudio/package-manager#18651 is the measurement.
package resolver_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// benchFixturePath is the committed excerpt, and benchFileEnv is the SAME env
// var index's and pep440set's real-corpus tests read. One convention across the
// module: whatever snapshot you point those at, you point this at.
const (
	benchFixturePath = "../index/testdata/pypi-trimmed.rsf"
	benchFileEnv     = "PYPIRSF_TEST_FILE"
)

// benchSnapshot opens the snapshot once for the whole benchmark binary.
//
// ⚠️ A MISSING FILE IS A FAILURE, NOT A SKIP, following index/rsfindex_real_test.go:
// a skip would report a green run for a benchmark that measured nothing.
func benchSnapshot(b *testing.B) (*pypirsf.File, bool) {
	b.Helper()

	path := os.Getenv(benchFileEnv)
	excerpt := path == ""
	if excerpt {
		path = benchFixturePath
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			b.Fatalf("cannot expand %q: %v", path, err)
		}
		path = filepath.Join(home, path[2:])
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		b.Fatalf("pypirsf.Open(%s): %v\n\nThe committed excerpt at %s is what makes this "+
			"benchmark runnable anywhere. Set %s to a full snapshot for the real numbers.",
			path, err, benchFixturePath, benchFileEnv)
	}
	b.Cleanup(func() { _ = file.Close() })
	return file, excerpt
}

// freshIndex is a brand-new RSFIndex over an open file: same bytes, empty
// decode cache. This is what "cold" is.
func freshIndex(b *testing.B, file *pypirsf.File) *index.RSFIndex {
	b.Helper()
	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		b.Fatalf("NewRSFIndex: %v", err)
	}
	return idx
}

// checkOutcome fails the benchmark when a resolution did not do what the corpus
// says it does.
//
// Enforced only against a full snapshot. An entry that errors out after two
// index calls produces a beautifully fast number that measures nothing, and
// that is exactly what every entry does against the 139-package excerpt -- so
// there, the outcome is reported rather than asserted.
func checkOutcome(b *testing.B, entry benchEntry, res *resolver.Resolution, err error, excerpt bool) {
	b.Helper()

	if excerpt {
		return
	}
	var re *resolver.ResolutionError
	switch {
	case entry.WantFailure && !errors.As(err, &re):
		b.Fatalf("%s: want a ResolutionError, got err=%v", entry.Name, err)
	case !entry.WantFailure && err != nil:
		b.Fatalf("%s: Resolve: %v", entry.Name, err)
	case !entry.WantFailure && len(res.Pinned) == 0:
		b.Fatalf("%s: resolved to nothing; the benchmark would be timing an empty walk", entry.Name)
	}
}

// report attaches the counted work to the benchmark line.
//
// pinned/op is not a performance number: it is the check that the iteration
// actually resolved something, which matters most on the excerpt where the
// outcome is not asserted.
func report(b *testing.B, c *counts, iters int, pinned int) {
	b.Helper()

	n := float64(iters)
	b.ReportMetric(float64(c.total())/n, "idxcalls/op")
	b.ReportMetric(float64(c.versions)/n, "versions/op")
	b.ReportMetric(float64(c.metadata)/n, "metadata/op")
	b.ReportMetric(float64(c.candidates)/n, "candvers/op")
	b.ReportMetric(float64(c.errs)/n, "idxerrs/op")
	b.ReportMetric(float64(pinned), "pinned")
}

// BenchmarkResolveCold resolves each corpus entry against a freshly created
// index, so no dependency blob has been decoded before the timer starts.
//
// The RSFIndex is created inside the timed region on purpose. It is a struct
// and a map, nanoseconds, and stopping the timer around it per iteration would
// add more noise than it removes.
//
// ⚠️ Cold here is cold in the process, not cold in the kernel. The snapshot's
// pages are in the OS page cache after the first iteration, so this is the cost
// of decoding, not of reading from a disk that has never been read. That is the
// right measurement for a server that has the file open, and it is the wrong
// one for a cold boot; a cold-boot figure would need the page cache purged
// between iterations, which no portable Go benchmark can do.
func BenchmarkResolveCold(b *testing.B) {
	file, excerpt := benchSnapshot(b)
	ctx := context.Background()

	for _, entry := range benchCorpus {
		b.Run(entry.Name, func(b *testing.B) {
			reqs := mustRequirements(b, entry.Requirements...)
			opts := testOptions(b)

			var (
				c      counts
				iters  int
				pinned int
			)
			b.ReportAllocs()
			for b.Loop() {
				counting := newCountingIndex(freshIndex(b, file))
				res, err := resolver.Resolve(ctx, reqs, counting, opts)

				b.StopTimer()
				checkOutcome(b, entry, res, err, excerpt)
				if res != nil {
					pinned = len(res.Pinned)
				}
				c.add(counting)
				iters++
				b.StartTimer()
			}
			report(b, &c, iters, pinned)
		})
	}
}

// BenchmarkResolveWarm resolves each corpus entry repeatedly against ONE index,
// primed by a resolution that runs before the timer starts.
//
// What is warm is the index's per-package decode cache, which is the only cache
// in the path. The resolution itself is redone from scratch every iteration:
// nothing memoizes a solve, and nothing memoizes usability across resolutions,
// so the index call COUNT is expected to be identical to cold. Only the cost of
// each call falls. That gap -- same calls, cheaper calls -- is what the two
// benchmarks together are for.
func BenchmarkResolveWarm(b *testing.B) {
	file, excerpt := benchSnapshot(b)
	ctx := context.Background()

	for _, entry := range benchCorpus {
		b.Run(entry.Name, func(b *testing.B) {
			reqs := mustRequirements(b, entry.Requirements...)
			opts := testOptions(b)
			idx := freshIndex(b, file)

			// Prime it. Everything the corpus entry touches is decoded once
			// here, outside the timer, which is what makes the loop warm.
			warm, err := resolver.Resolve(ctx, reqs, idx, opts)
			checkOutcome(b, entry, warm, err, excerpt)

			var (
				c      counts
				iters  int
				pinned int
			)
			b.ReportAllocs()
			for b.Loop() {
				counting := newCountingIndex(idx)
				res, err := resolver.Resolve(ctx, reqs, counting, opts)

				b.StopTimer()
				checkOutcome(b, entry, res, err, excerpt)
				if res != nil {
					pinned = len(res.Pinned)
				}
				c.add(counting)
				iters++
				b.StartTimer()
			}
			report(b, &c, iters, pinned)
		})
	}
}

// BenchmarkOpenSnapshot measures pypirsf.Open plus NewRSFIndex: the once-per-
// process cost of making a snapshot resolvable.
//
// Reported separately because it is not part of any resolution, and because it
// is the one number in this file that scales with the size of the corpus rather
// than with the size of the request. Run it with -benchtime=1x against a full
// snapshot unless you want to wait.
func BenchmarkOpenSnapshot(b *testing.B) {
	path := os.Getenv(benchFileEnv)
	if path == "" {
		path = benchFixturePath
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			b.Fatalf("cannot expand %q: %v", path, err)
		}
		path = filepath.Join(home, path[2:])
	}

	b.ReportAllocs()
	var records int
	for b.Loop() {
		file, err := pypirsf.Open(path)
		if err != nil {
			b.Fatalf("pypirsf.Open(%s): %v", path, err)
		}
		if _, err := index.NewRSFIndex(file, "production"); err != nil {
			b.Fatalf("NewRSFIndex: %v", err)
		}
		records = file.Len()
		if err := file.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}
	b.ReportMetric(float64(records), "records")
}
