// SPDX-License-Identifier: Apache-2.0 OR MIT

// The Phase 3 exit-gate resolution benchmark for RFD 0001
// (rstudio/package-manager#18651): cold and warm resolution wall time,
// allocations, and MetadataIndex call counts over a fixed corpus.
//
// # What cold and warm mean here
//
// Cold is a fresh index.RSFIndex over an already-open snapshot: nothing decoded
// and nothing parsed, so every version list and every version's dependency blob
// is read, decompressed and parsed during the resolution. Warm is the same
// requirements resolved again against the same RSFIndex, whose decode cache and
// parsed memos are now populated. Both build a new provider and run the solver
// from scratch -- nothing memoizes a resolution.
//
// Cold is not "warm plus I/O". The memos pay off WITHIN one resolution too,
// because a backtracking solver asks about the same version repeatedly, which
// is why cold improved 1.3x to 2.3x alongside warm's 2.4x to 4.5x.
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
// # Measured, 2026-08-13, on the machine above
//
// Ten iterations per entry, cold and warm in one run, before and after the
// parsed memo added to index.RSFIndex. Times are per resolution; idx is total
// MetadataIndex calls, of which meta is Metadata; cand is the number of
// candidate versions the resolution walked; pins is the size of the closure.
// Both columns of each pair were measured in the same session on the same
// machine; two repeat runs of each reproduced every figure within 3%.
//
//	entry            cold ms          warm ms         idx    meta    cand   pins
//	                 before  after    before  after
//	single-no-deps     4.31   3.05      4.51   1.89    133     131     130      1
//	small-tree        24.29  12.51     24.04   6.78    313     290     984      7
//	extras            47.35  20.59     47.31  13.78    695     661    1658      8
//	app-set          675.98 299.48    681.62 261.58   4837    4750    6040     18
//	wide-versions    535.00 399.86    536.81 186.70   4566    4549    7206      7
//	backtracking      44.64  25.23     45.80  10.10    444     435     769      4
//	unsatisfiable      3.16   3.25      3.28   0.94     39      37     124      0
//
//	entry            warm B/op         warm allocs/op
//	                 before    after   before      after
//	single-no-deps     3.9 MB   2.1 MB    104,212     46,162
//	small-tree        27.1 MB  12.7 MB    512,130    144,247
//	extras            48.2 MB  21.6 MB    968,202    290,761
//	app-set          754.8 MB 443.8 MB 12,276,480  5,114,188
//	wide-versions    661.0 MB 357.4 MB 10,647,428  3,958,498
//	backtracking      44.1 MB  17.3 MB    824,251    213,325
//	unsatisfiable      3.9 MB   2.1 MB     70,718     21,405
//
// Index call counts are IDENTICAL before and after, which is the point: the memo
// makes each call cheaper and removes none. See the COUNT paragraph below.
//
// Opening the snapshot: 233 ms, 141 MB, for 932,861 records. Retained heap
// attributable to one resolve rose from 0.4 MB to 2.5 MB (app-set) and from
// 1.4 MB to 5.4 MB (wide-versions), against a ~64 MB post-open baseline --
// allocation churn and retained heap moved in opposite directions, so both are
// reported.
//
// # Verdict against the Phase 3 exit gate
//
// The gate in RFD 0001 Section 9 is <100 ms cold and <1 ms warm, and the RFD
// records both as estimates.
//
//   - COLD: met by five of seven entries, missed by two -- app-set at 299 ms
//     (3.0x, was 6.8x) and wide-versions at 400 ms (4.0x, was 5.4x).
//   - WARM: met by one of seven, and only just. unsatisfiable STRADDLES the
//     line: 0.94 ms over ten iterations, and 0.97-1.03 ms across six runs of
//     500 with a median near 0.98 -- one of those six is over. Call it "at the
//     line", not "passed". The other six miss by 1.9x (single-no-deps), 6.8x
//     (small-tree), 10.1x (backtracking), 13.8x (extras), 187x (wide-versions)
//     and 262x (app-set), against 4x to 208x before.
//
// Warm is now 2.4x to 4.5x faster than cold rather than indistinguishable from
// it, so the two benchmarks finally measure different things.
//
// # Where the cost went, after the memo
//
// From `go tool pprof -top -cum` on BenchmarkResolveWarm/app-set (1.87 s of
// samples inside Resolve, down from 5.85 s):
//
//	Candidates                     1.86 s   99.5% of Resolve
//	  usable                       1.32 s   70.6%
//	    expandRequirements         0.94 s   50.3%
//	      marker.Evaluate          0.79 s   42.2%
//	      pep440set.FromSpecifiers 0.45 s   24.1%
//	    interpreterDependency      0.31 s   16.6%   (Specifiers.Check)
//	    Metadata                     --       --    below the 0.03 s cutoff
//	  candidate.Rank               0.47 s   25.1%   (sort.SliceStable)
//
// Metadata was 47.3% and requirement.Parse alone was 41.0%; neither now clears
// the profiler's cutoff. What dominates instead is the work expandRequirements
// does with the parsed requirements: evaluating each one's PEP 508 marker
// against the target environment, and converting its specifiers into a
// pep440set. Both re-run per candidate version, and both are pure functions of
// (memoized requirement, fixed environment) -- so the next constant-factor win
// is a memo of the PROJECTION, keyed by (package, version, extra), not of the
// metadata. version.Parse is still 30% of Resolve, but it is now parsing
// SPECIFIER OPERANDS inside FromSpecifiers rather than version keys.
//
// Two upstream costs are now visible that the parse used to hide, and both are
// in go-python-packaging's dependency rather than in this module:
// version.Version.Compare is 30% of Resolve, and reflect.DeepEqual -- called as
// a fast path from go-version's part.Parts.Compare -- is 17% on its own.
//
// # What no amount of caching will fix
//
// The COUNT: solver.Provider requires Candidates to return a count that is zero
// exactly when nothing satisfies, so an exact count means testing every version
// in the allowed range, and usable tests by computing the version's full
// dependencies. That is what makes index calls scale with candidate versions
// rather than with the closure -- certifi has no dependencies at all and still
// costs 131 Metadata calls, one per released version.
//
// This is what the warm target turns on. At app-set's 4,837 index calls, a 1 ms
// warm resolution allows 207 ns per call end to end; the memo brought the cost
// per call down by a factor of 2.6 and it needs another factor of 262. Caching
// cannot get there. The call count has to fall, and that is a go-pubgrub
// interface conversation rather than a tuning exercise.
//
// Note which entry cleared the warm bar: unsatisfiable, the one that resolves
// nothing, at 39 index calls. The bar is a function of the call count, and only
// the entry with the fewest calls is anywhere near it.
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
// ⚠️ Those figures are the PRE-FIX baseline: they were taken before
// rstudio/package-manager#19713 derived a bound's key once, which took the same
// resolution to 2.1 s and 4.2 GB. They are kept because the point they make --
// that an incomplete index has a completely different dominant cost from a
// complete one, so the memo above does nothing for it -- survives the fix.
//
// The measurement remains the deliverable of rstudio/package-manager#18651. The
// two optimizations now in the module (#19713's set algebra, and the parsed memo
// this file re-baselines) were each landed because this benchmark named them,
// and each is measured here before and after rather than asserted.
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
// What is warm is the index's caches: the per-package decode cache, and the
// parsed memos above it. The resolution itself is redone from scratch every
// iteration: nothing memoizes a solve, and nothing memoizes usability across
// resolutions, so the index call COUNT is expected to be identical to cold, and
// is. Only the cost of each call falls. That gap -- same calls, cheaper calls --
// is what the two benchmarks together are for, and it is what was missing when
// the index cached raw strings only.
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
