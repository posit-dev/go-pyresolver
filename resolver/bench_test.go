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
// is why cold improved 1.1x to 2.3x alongside warm's 2.3x to 4.3x.
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
// Every figure below is the mean of TWO full runs; the two agreed within 3%
// except where noted.
//
//	entry            cold ms          warm ms         idx    meta    cand   pins
//	                 before  after    before  after
//	single-no-deps     4.14   3.00      4.25   1.89    133     131     130      1
//	small-tree        23.83  12.07     24.24   6.86    313     290     984      7
//	extras            46.45  20.33     46.74  13.60    695     661    1658      8
//	app-set          670.77 302.48    677.60 261.14   4837    4750    6040     18
//	wide-versions    530.27 396.00    531.95 182.90   4566    4549    7206      7
//	backtracking      44.40  25.03     44.37  10.22    444     435     769      4
//	unsatisfiable      3.33   3.15      3.18   0.90     39      37     124      0
//
//	entry            warm B/op         warm allocs/op
//	                 before    after   before      after
//	single-no-deps     3.9 MB   2.1 MB    104,384     46,165
//	small-tree        27.1 MB  12.7 MB    511,426    144,260
//	extras            48.4 MB  21.5 MB    971,659    290,753
//	app-set          755.1 MB 443.8 MB 12,291,462  5,114,833
//	wide-versions    661.1 MB 357.5 MB 10,649,006  3,960,988
//	backtracking      44.1 MB  17.3 MB    822,959    214,226
//	unsatisfiable      3.9 MB   2.1 MB     70,828     21,405
//
// Index call counts are IDENTICAL before and after, which is the point: the memo
// makes each call cheaper and removes none. See the COUNT paragraph below.
//
// # What the defensive copy costs, measured rather than assumed
//
// Metadata copies RequiresDist, ProvidesExtra and each requirement's Extras
// before handing them back (see index/rsfindex.go's cloneMetadata). The Extras
// copy is the one with a per-element cost that varies by corpus, so it was
// counted rather than waved through: one allocation per requirement carrying a
// bracketed extra, and across the corpus that is 0% of requirements on five
// entries, 0.95% on app-set (630 per resolution) and 14.4% on wide-versions
// (2,430 per resolution). Those are 0.012% and 0.061% of each entry's warm
// allocations, and they account for the whole warm allocs/op delta against a
// build without the copy, to the allocation: app-set +628 observed against +630
// predicted, wide-versions +2,430 against +2,430.
//
// Opening the snapshot: 233 ms, 141 MB, for 932,861 records. Retained heap
// attributable to one resolve rose from 0.40 MB to 2.54 MB (app-set) and from
// 1.36 MB to 5.31 MB (wide-versions), against a ~64 MB post-open baseline --
// allocation churn and retained heap moved in opposite directions, so both are
// reported.
//
// # Verdict against the Phase 3 exit gate
//
// The gate in RFD 0001 Section 9 is <100 ms cold and <1 ms warm, and the RFD
// records both as estimates.
//
//   - COLD: met by five of seven entries, missed by two -- app-set at 302 ms
//     (3.0x, was 6.7x) and wide-versions at 396 ms (4.0x, was 5.3x).
//   - WARM: met by one of seven, and only just. unsatisfiable STRADDLES the
//     line: 0.90 ms here, and 0.97-1.03 ms across six runs of 500 in an earlier
//     session with a median near 0.98 -- one of those six is over. Call it "at
//     the line", not "passed". The other six miss by 1.9x (single-no-deps), 6.9x
//     (small-tree), 10.2x (backtracking), 13.6x (extras), 183x (wide-versions)
//     and 261x (app-set), against 3.2x to 678x before.
//
// ⚠️ Warm is 2.3x to 4.3x faster THAN IT WAS, which is not the same claim as
// warm being that much faster than cold, and an earlier draft of this note
// conflated the two. Warm against cold, after: 1.16x (app-set) to 3.5x
// (unsatisfiable). What changed is that the two benchmarks now measure
// different things at all -- before, warm was within 3% of cold everywhere.
//
// # Where the cost went, after the memo
//
// From `go tool pprof -top -cum` on BenchmarkResolveWarm/app-set, 2.49 s of
// samples inside Resolve. Percentages are OF RESOLVE, not of total samples:
//
//	Candidates                     2.49 s  100.0% of Resolve
//	  usable                       1.91 s   76.7%
//	    expandRequirements         1.32 s   53.0%
//	      marker.Evaluate          1.07 s   43.0%
//	      pep440set.FromSpecifiers 0.71 s   28.5%
//	    interpreterDependency      0.49 s   19.7%   (Specifiers.Check)
//	    Metadata                   0.10 s    4.0%
//	  candidate.Rank               0.48 s    19.3%  (sort.SliceStable)
//
// Metadata was 47.3% of Resolve and requirement.Parse alone was 41.0%. Metadata
// is now 4.0% and requirement.Parse does not appear in the top 250 nodes at all.
//
// ⚠️ An earlier draft of this note said Metadata was "below the profiler's
// cutoff", which was true only of the node count that draft happened to ask for.
// It is a real 4.0%, and a claim of the form "does not appear" is a claim about
// the flag, not about the code.
//
// What dominates instead is the work expandRequirements does WITH the parsed
// requirements: evaluating each one's PEP 508 marker against the target
// environment, and converting its specifiers into a pep440set. Both re-run per
// candidate version, and both are pure functions of (memoized requirement, fixed
// environment) -- so the next constant-factor win is a memo of the PROJECTION,
// keyed by (package, version, extra), not of the metadata. version.Parse is
// still 34% of Resolve, but it is now parsing SPECIFIER OPERANDS inside
// FromSpecifiers rather than version keys.
//
// ⚠️ THAT NEXT MEMO WOULD RE-OPEN THE HAZARD THIS ONE AVOIDS. A pep440set.Set
// holds bounds, a bound holds a version.Version and a *posKey whose pub is
// another, and Set is copied BY VALUE -- so a memoized Set shares parsed
// versions between every goroutine that reads it, and Set.Singleton() hands
// sp.lo.v straight out. Stressing it under -race today comes back clean, but
// only incidentally: after #33 the sole surviving Compare call site is reached
// only once the release lengths already match, which makes Padding a no-op.
// Reordering cmpBound's discriminators brings the race back. Whoever builds
// that memo owns the question, and "it was clean when I tried it" is not the
// answer.
//
// Two upstream costs are now visible that the parse used to hide, and both are
// in go-python-packaging's dependency rather than in this module:
// version.Version.Compare is 26% of Resolve, and reflect.DeepEqual -- called as
// a fast path from go-version's part.Parts.Compare -- is 15% on its own.
//
// # What no amount of caching will fix
//
// The COUNT: this provider returns an EXACT count, so it calls usable on every
// version in the allowed range, and usable computes the version's full
// dependencies. That is what makes index calls scale with candidate versions
// rather than with the closure -- certifi has no dependencies at all and still
// costs 131 Metadata calls, one per released version. Caching cannot remove it,
// because it is a call count rather than a constant.
//
// ⚠️ What forces the exact count is NOT the zero-exactness rule, and an earlier
// version of this comment said it was ("a count that is zero exactly when
// nothing satisfies, SO an exact count means testing every version"). That does
// not follow. Zero-exactness is an EXISTENCE requirement, and existence is
// settled by finding one usable version and stopping. It is the MAGNITUDE that
// requires testing everything -- and go-pubgrub consumes the magnitude only in
// its version-choice heuristic, which its own documentation and the prose sources
// agree is tunable rather than correctness-bearing. So the expensive half of this
// contract is the half that is explicitly not correctness-bearing.
//
// This is what the warm target turns on. At app-set's 4,837 index calls, a 1 ms
// warm resolution allows 207 ns per call end to end; the memo brought the cost
// per call down by a factor of 2.6 and it needs another factor of 261. Caching
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
