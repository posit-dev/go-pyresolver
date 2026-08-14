// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// This file turns the equivalence evidence behind the 0.5.0 and 0.6.0
// performance changes into something CI runs.
//
// # What the evidence was, and why it did not survive review
//
// Those changes were justified by a 4,007-resolution transcript diffed between
// two builds: the optimized tree and the baseline produced byte-identical
// output, so nothing a caller can observe had changed. The method is sound. The
// problem is that it ran once, on a laptop, against a 981 MB snapshot nobody
// else has, and nothing in this repository re-runs it. A regression landing
// tomorrow would meet no check at all.
//
// ⚠️ The second problem is subtler and it bit while this file was being written.
// An ad hoc measurement is not just unrepeatable, it is UNDETECTABLY corruptible:
// concurrent work on the same machine overwrote three of five baseline rounds
// belonging to a measurement running alongside this one, and the surviving files
// still existed, still parsed, and would still have produced a plausible median.
// Nothing about the output said it was wrong.
//
// A CI check has neither problem. It reruns on demand, and it derives its inputs
// from the committed tree rather than from whatever happens to be in a temporary
// directory -- which is why the harness below reads a committed fixture and
// compares against a committed golden file, and why the figures quoted in this
// file are runner measurements from an API rather than local files.
//
// # What can and cannot be made reproducible, stated plainly
//
// ⚠️ The full-snapshot differential CANNOT be reproduced in CI, and this file
// does not pretend otherwise. It needs the production snapshot, it needs two
// builds, and at 4,007 cases it took hours. TestDumpResolutions remains the tool
// for it and remains env-gated, because a tool that asserts nothing is not a
// test.
//
// What CAN be reproduced is the same property, at excerpt scale, converted from
// a two-build diff into a build-against-golden diff:
//
//	TestResolutionTranscriptMatchesGolden
//
// It resolves the committed benchmark corpus plus the packages in
// index/testdata/pypi-trimmed.rsf, and compares the transcript byte for byte
// against testdata/excerpt-transcript.txt. A change to pins, to search order, to
// activated extras or to a failure report fails the build and shows exactly
// what moved. That is strictly weaker than the production differential in
// COVERAGE -- 139 packages rather than 932,861 -- and exactly as strong in
// SENSITIVITY, which is the half that was missing.
//
// Two subsets of the excerpt are held out, for two different reasons that should
// not be conflated: slowExcerptPackages (cost, run nightly instead) and
// unboundedExcerptPackages (no deterministic result exists at any deadline).
//
// # It must not skip
//
// The producer-output tests in the index package were env-var gated for months,
// so CI never ran the only tests that read what the manifest tooling emits: the
// suite was green and the property was unchecked
// (rstudio/package-manager#19466). This test is built the same way that one was
// fixed -- it FAILS rather than skips if the fixture is missing -- and CI has a
// step that fails if it skips anyway.

// excerptFixture is the committed RSF excerpt. Missing is a FAILURE, not a
// skip: see the note above.
const excerptFixture = "../index/testdata/pypi-trimmed.rsf"

// The golden files. Regenerate with
//
//	GPR_UPDATE_TRANSCRIPT=1 go test ./resolver/ -run TestResolutionTranscriptMatchesGolden
//	GPR_UPDATE_TRANSCRIPT=1 GPR_TRANSCRIPT_FULL=1 go test ./resolver/ -run TestResolutionTranscriptMatchesGolden
//
// and READ THE DIFF before committing. Regenerating is how a golden file stops
// being an assertion, so the diff is the review.
const (
	goldenFast = "testdata/excerpt-transcript.txt"
	goldenFull = "testdata/excerpt-transcript-full.txt"
)

// transcriptFormat is written as the first line of every transcript, by
// writeTranscript, so the golden files and TestDumpResolutions' output both
// carry it.
//
// ⚠️ It exists so a transcript from this build cannot be silently diffed against
// one produced before the failure-report format changed (see renderFailure). The
// published 4,007-case runs behind the 0.6.0 changelog figures are v1: full
// report text inline, no header. Diffing a v1 file against a v2 file would show
// a difference on every failing case and mean nothing.
//
// Written by writeTranscript rather than by its callers precisely because
// TestDumpResolutions is where the v1 files came from: a header the CI check
// emits and the tool does not would mark the one artifact that never needed
// marking.
const transcriptFormat = "# gpr-transcript v2"

// slowExcerptPackages are the packages held out of the per-pull-request sweep,
// with the cost that earned each one its place.
//
// # Why a split at all
//
// The full sweep -- 145 cases, every excerpt package except the one in
// unboundedExcerptPackages, plus the benchmark corpus -- takes 95-110 s on an
// M4 Max. The 125-case subset the pull-request job runs takes about 6 s, so the
// twenty packages below are essentially the entire cost. Adding them to every
// pull request would take the transcript check itself from 6 s to 95-110 s
// locally, so about 89-104 s of extra sweep.
//
// ⚠️ Scaling that to the runner needs a ratio measured on THIS workload, and two
// earlier drafts each got it wrong in a different direction. The first added the
// LAPTOP delta straight onto a RUNNER baseline and said "four or five times". The
// second scaled it by 3.2x -- but that ratio came from `go test ./...`, 7.1 s
// locally against 17-23 s on the runner, which is substantially a COMPILE-cost
// ratio, and the marginal cost of twenty more resolutions contains no compile at
// all. Applying a compile-inclusive ratio to compile-free work is the same
// species of error as the first draft, and it produced "about 13x".
//
// The like-for-like number is available and is now used: the equivalence step
// runs with -v, so CI logs the test BODY. Across four runs it is 11.2-14.1 s,
// against 5.6-6.0 s locally -- a ratio of 1.9x to 2.5x. So the extra sweep is
// 169-260 s on the runner, taking `go test ./...` from 17-23 s to roughly
// 190-280 s: an order of magnitude, stated as such rather than to two figures.
//
// So the default run is everything else, and
// GPR_TRANSCRIPT_FULL=1 -- which CI runs nightly against its own golden file --
// adds them back.
//
// # How the costs below were measured
//
// One methodology for all twenty, re-measured together rather than patched
// entry by entry: a COLD single-package resolve, with a freshly opened fixture
// and a fresh RSFIndex each time, on an M4 Max. Cold is the honest number for
// the decision this list encodes -- "is this package worth holding out?" -- and
// it is an UPPER bound on what the package adds to a sweep, because a sweep
// warms the index memo as it goes and a package measured after its neighbours
// costs slightly less.
//
// They do approximately account for the sweep, and the like-for-like comparison
// is against the sweep's MARGINAL cost rather than its total: the twenty sum to
// 106 s, against (full 95-110 s minus fast 6 s) = 89-104 s of marginal cost. So
// cold over-counts by roughly 2-19%, which is the warming. Comparing 106 s
// against the full total instead would understate that gap.
//
// ⚠️ Do not read two of them summed as one of them doubled -- an earlier draft of
// this file quoted `ipykernel` + `ipython` as "38 s each" when 38 s is the pair.
//
// # Why these are slow, and why it is not a resolver problem
//
// A single-package requirement whose transitive dependencies are largely absent
// from a 139-package excerpt puts the solver into the second cost regime
// bench_test.go describes: version-set algebra inside conflict resolution. That
// is a property of the EXCERPT rather than of these packages, all of which
// resolve quickly against a full snapshot.
//
// # Why exclusion by NAME rather than a wall-clock deadline
//
// TestDumpResolutions bounds each case with a wall clock and prints TIMEOUT past
// it. That is right for a two-build diff run by one person on one machine, and
// fatal for a golden file: whether a case trips the deadline depends on the
// machine, so the transcript would differ between a laptop and a CI runner for
// reasons that have nothing to do with the resolver.
//
// ⚠️ Every name is checked against the fixture, so this list cannot rot silently
// into an exclusion of nothing.
var slowExcerptPackages = map[string]string{
	"ipykernel":          "21.5s",
	"ipython":            "20.2s",
	"prompt-toolkit":     "11.3s",
	"jupyter-ai":         "11.1s",
	"virtualenv":         "10.0s",
	"docrepr":            "4.6s",
	"matplotlib":         "4.6s",
	"keyring":            "3.5s",
	"jaraco-tidelift":    "3.5s",
	"sphinx-tabs":        "3.1s",
	"sphinx":             "2.4s",
	"myst-parser":        "1.9s",
	"flake8":             "1.6s",
	"pandas":             "1.5s",
	"domdf-python-tools": "1.3s",
	"xmlschema":          "1.2s",
	"pytest-xdist":       "916ms",
	"trio":               "684ms",
	"nbclient":           "600ms",
	"pytest-flake8":      "577ms",
}

// unboundedExcerptPackages are excluded from BOTH modes, which is a different
// claim from slowExcerptPackages and deserves its own list.
//
// ⚠️ `hypothesis` does not finish. Not "takes a while": on an M4 Max it has now
// been measured against bounds of twenty seconds, sixty seconds, three minutes
// and ten minutes, and exceeded every one of them, while the slowest package that
// DOES finish takes 21.5 s. It is not on the same scale as the list above; it is
// off the end of it. There is no deadline at which its transcript entry is a fact
// about the resolver rather than a fact about the machine, so there is no golden
// file it can honestly appear in -- and the first version of this test committed
// a `TIMEOUT` line for it, which is precisely the machine-dependent assertion
// slowExcerptPackages exists to avoid, smuggled in through the nightly.
//
// It is the excerpt that does this, not hypothesis: a package whose transitive
// dependencies are nearly all absent from a 139-package file sends the solver
// into unbounded version-set algebra proving they cannot be satisfied. hypothesis
// resolves in milliseconds against a full snapshot.
//
// ⚠️ Do NOT "fix" this by raising caseTimeout. That trades a fast red build for a
// slow one and still commits a wall-clock claim. If this needs covering, it needs
// a bounded solver or a fixture that carries its dependencies.
var unboundedExcerptPackages = map[string]string{
	"hypothesis": "did not finish under 20s, 60s, 3min or 10min bounds; no deterministic entry exists",
}

// caseTimeout bounds a single resolve so a hang fails loudly instead of eating
// the job's whole budget. It is a HANG GUARD, not the case-selection deadline
// TestDumpResolutions uses: a case that hits it means something has gone badly
// wrong, not that the corpus needs trimming.
//
// It is bounded from BOTH sides, and getting either wrong disarms it.
//
// ⚠️ ABOVE, AND THE MARGIN MUST BE COMPUTED ON THE RUNNER. A TIMEOUT line is
// machine-dependent, so a bound near the real cost would fail the golden on a
// slow machine for a reason that has nothing to do with the resolver -- the exact
// fragility slowExcerptPackages exists to avoid, through the back door. An
// earlier draft called two minutes "about six times above the worst measured
// case", which is 120 s over a 21.5 s figure measured on an M4 Max: a laptop
// number weighed against a bound that has to hold on a runner. Scaled by the
// like-for-like ratio for this workload -- the equivalence step's test BODY, which
// CI logs because the step runs with -v: 11.2-14.1 s across four runs against
// 5.6-6.0 s locally, so 1.9x to 2.5x -- the worst case is 41-54 s and that margin
// was between 2.2x and 2.9x.
//
// Five minutes is 5.5x to 7.3x that runner-adjusted worst case, which occurs only
// in the NIGHTLY -- the one job that resolves the expensive packages at all.
//
// ⚠️ That the pull-request job is nowhere near this bound is load-bearing and not
// obvious. It runs the fast set under `-race`, which costs a further 4.5x on an
// idle machine and 7.7x on a loaded one (this test body: 5.6 s -> 25.2 s, and
// 6.8 s -> 51.9 s under load) -- and it survives that only because
// slowExcerptPackages holds the expensive cases out. The slowest fast-set case is
// `pandas numpy<1.26` at about 1.6 s locally, so seconds even with the detector.
//
// Move a slow package back into the fast set and the margin does not just narrow,
// it STRADDLES the bound: ipykernel at 21.5 s x 1.9-2.5 (runner) x 4.5-7.7 (race)
// is 184-414 s against a 300 s guard. Which side it lands on depends on how busy
// the runner is, which is the worst of both worlds -- an intermittently poisoned
// golden rather than an honest failure.
//
// ⚠️ Two earlier drafts each landed on one side of that by picking a convenient
// multiplier: "~4x" for race gave 275 s and implied the guard would never fire,
// and a 3.2x compile-inclusive runner ratio gave 310-530 s and implied it always
// would. The band above is wide because the inputs genuinely are, and the
// conclusion that survives is the one that does not depend on where in it you
// land: do not move a slow package into the fast set.
//
// ⚠️ BELOW: the per-case guard must fire before the PACKAGE timeout, or it is
// decorative. This constant WAS 10 minutes, which is exactly `go test`'s default,
// and that disarmed it everywhere it mattered: a hung case would trip the package
// timer and panic the binary before writeTranscript could record a TIMEOUT and
// before stats.Timeouts was evaluated. The assertion existed and could not run.
//
// That is now belt-and-braces from both ends. ci.yml passes an explicit -timeout
// on all three invocations that reach this test, so the package budget no longer
// depends on a default; and five minutes is still under the 10-minute default, so
// a bare `go test ./...` on a laptop is armed too.
const caseTimeout = 5 * time.Minute

// transcriptStats counts what a transcript run actually EXERCISED, so a run can
// be rejected for covering nothing even when it matches its golden file.
//
// # ⚠️ Why "cases compared" is not the number to assert on
//
// A previous differential in this project reported full agreement while
// covering none of its own subject. It counted packages where neither side
// errored -- which included every package both sides reported as unavailable,
// because "unavailable" is an agreement about the index rather than about the
// resolver. The count was large, the coverage was zero, and nothing in the
// output distinguished the two.
//
// So the fields below split cases by WHAT A BROKEN IMPLEMENTATION WOULD HAVE TO
// GET WRONG to change them, and the assertions are on the discriminating
// classes only. Vacuous is counted and printed rather than dropped, because the
// ratio is the thing that was invisible last time.
type transcriptStats struct {
	// Cases is every entry written. Reported, never asserted on.
	Cases int

	// Deep is a successful resolution with two or more pins: at least one
	// dependency edge was walked, which means a requirement was parsed, mapped
	// to a version set, intersected, and a version chosen from what survived.
	// Getting any of that wrong changes the transcript.
	Deep int

	// Shallow is a successful resolution pinning exactly one package. The
	// solver still ranked that package's versions and applied Requires-Python,
	// the pre-release policy and marker evaluation, so a broken ranker shows
	// up here -- but no dependency edge was crossed. Weaker, counted
	// separately, and deliberately NOT summed into the discriminating total.
	Shallow int

	// Extras is a successful resolution where some pin carries an activated
	// extra, which is the only shape that exercises the virtual-package
	// expansion in the provider.
	Extras int

	// Derivations are failures whose report carries more than one line: the
	// solver derived a conflict from real metadata, and the report text -- the
	// user-facing artifact most likely to shift under a search-order change --
	// is in the transcript.
	Derivations int

	// Vacuous is the class the earlier differential mistook for coverage: a
	// failure whose whole report is that a package is absent from the index.
	// Any implementation that opens the file at all reproduces it.
	Vacuous int

	// Unparseable requirements, reported so a corpus typo cannot hide as a
	// quietly-skipped case.
	Unparseable int

	// Errors is cases where Resolve returned something that is NOT a
	// *ResolutionError.
	//
	// ⚠️ NOT a cancelled context: writeTranscript tests caseCtx.Err() first, so
	// every context error routes to Timeouts and can never land here. The
	// realistically reachable case is the solver giving up -- go-pubgrub's
	// "solver: gave up after N rounds without settling", returned unwrapped
	// through resolver/error.go -- which is a real answer about a hard instance
	// rather than a harness fault, and is the reason this class needs a name
	// instead of being described as "something went wrong".
	//
	// Expected to be zero and asserted so. The golden comparison would catch it
	// too, since an ERROR line is text like any other; what the counter adds is
	// a message that names the class, and cover for a golden regenerated while
	// cases were erroring.
	Errors int

	// Timeouts is cases that hit caseTimeout. Expected to be zero -- that bound
	// is a hang guard several times above the worst real case, not a selection
	// deadline -- and asserted to be zero so the failure names the actual
	// problem. The golden comparison would fail on such a run anyway, since a
	// TIMEOUT line is text like any other; what this adds is a message that says
	// "a machine-dependent line reached the transcript" rather than a byte diff
	// the reader has to interpret, and cover for a golden regenerated with
	// GPR_UPDATE_TRANSCRIPT=1 while a case was timing out -- which is exactly
	// how one got committed.
	Timeouts int

	// Mode is "fast" or "full", printed so a run cannot be mistaken for the
	// other one. The nightly job greps for it: without it, a nightly that lost
	// GPR_TRANSCRIPT_FULL would run the 125 cases the pull-request job already
	// ran, match the fast golden, print a plausible summary and pass -- while
	// covering none of the 20 packages it exists for.
	Mode string
}

// Discriminating is the count the two class minimums are taken from. Extras has
// a minimum of its own; it is not summed in here because a resolution that
// activates an extra is already counted as Deep, and adding it would count the
// same case twice.
func (s transcriptStats) Discriminating() int { return s.Deep + s.Derivations }

// String renders the summary CI greps for and a reader sees.
//
// The partition is rendered as an explicit SUM so the reader can check it by
// eye, and the one overlay count is rendered outside it and labelled.
//
// ⚠️ An earlier version listed extras in the middle of the class run --
// "72 shallow, 2 extras, 8 vacuous, ..." -- and carried a comment claiming it was
// "rendered after the semicolon with the other non-class counts", of which there
// were none: everything after the semicolon except extras is a partition class,
// and the two genuine non-class counts were before it. The rendering did the
// opposite of what its own rationale claimed, and invited exactly the misreading
// the rationale was there to prevent.
func (s transcriptStats) String() string {
	return fmt.Sprintf(
		"transcript[%s]: %d cases = %d deep + %d shallow + %d derivations + %d vacuous "+
			"+ %d unparseable + %d errors + %d timeouts; %d discriminating; "+
			"%d extras (overlay on deep, not a class)",
		s.Mode, s.Cases, s.Deep, s.Shallow, s.Derivations, s.Vacuous,
		s.Unparseable, s.Errors, s.Timeouts, s.Discriminating(), s.Extras)
}

// transcriptRun is one transcript-writing job. It is a struct rather than seven
// positional parameters because the last of them is a bare bool that reads as
// noise at a call site.
type transcriptRun struct {
	idx      index.MetadataIndex
	cases    [][]string
	mode     string
	deadline time.Duration

	// haltOnTimeout stops the sweep at the FIRST case to hit deadline.
	//
	// ⚠️ Without it, the guard's reach depends on how many cases hang. The loop
	// used to continue past a timeout, so N hung cases cost N x deadline: at a
	// 5-minute bound, ONE hang fits inside a bare `go test`'s 10-minute package
	// budget and two do not, and the binary panics instead of failing with a
	// message that names the problem.
	//
	// That matters because hangs come in classes, not singly. The cost regime
	// this file's slow list exists for is a property of the SHAPE of a
	// resolution, so a regression that trips it trips it for many packages at
	// once -- 14 of the 125 fast cases already sit above a 200 ms bound.
	// Halting makes the worst case one deadline regardless.
	//
	// ⚠️ It is not free, and an earlier draft of this comment claimed it was.
	// What it costs is the CLASS SIZE: the "14 of 125" figure above was only
	// obtainable by letting a 200 ms sweep run to completion, and after a halt
	// you learn "case N hung" rather than "a fifth of them did". That is a real
	// loss of exactly the diagnostic the paragraph above says matters. The trade
	// is still right -- 14 x 5 minutes does not fit in any budget CI would
	// tolerate -- but the way to recover the class size is to re-run locally with
	// a small deadline and this flag false, not to pretend it was not lost.
	//
	// TestDumpResolutions leaves this false, because there a TIMEOUT is a
	// SELECTION deadline doing its job rather than a hang.
	haltOnTimeout bool
}

// writeTranscript renders one entry per case and classifies it.
//
// It is shared with TestDumpResolutions so the CI check and the full-snapshot
// tool cannot drift into producing different text for the same resolution --
// which would quietly invalidate every published figure that came from the tool.
//
// run.deadline is per case, and what a TIMEOUT means depends on which caller set
// it:
//
//   - TestDumpResolutions sets a SELECTION deadline. A case that hits it is
//     excluded from that tool's two-build diff, because a wall clock is not
//     deterministic across two builds, and the sweep carries on.
//   - TestResolutionTranscriptMatchesGolden sets caseTimeout, a hang guard
//     several times above the worst real case, and halts on it. ⚠️ A TIMEOUT
//     line there is NOT excluded from anything -- it is written into the buffer
//     and compared byte for byte like every other line, so a golden file
//     containing one is a committed assertion about wall clock on whatever
//     machine generated it. That is why stats.Timeouts is asserted to be zero
//     rather than merely reported.
func writeTranscript(t testing.TB, w io.Writer, run transcriptRun) transcriptStats {
	idx, cases, deadline, mode := run.idx, run.cases, run.deadline, run.mode
	t.Helper()

	stats := transcriptStats{Mode: mode}
	ctx := context.Background()
	opts := testOptions(t)

	// Errors are deferred to the caller's Flush, exactly as the dump tool did:
	// branching at five call sites on a bufio.Writer's return value carries no
	// information.
	pf := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	pf("%s\n", transcriptFormat)

	for _, reqStrings := range cases {
		stats.Cases++
		pf("=== %s\n", strings.Join(reqStrings, " "))

		reqs, err := requirementsOrNil(reqStrings)
		if err != nil {
			stats.Unparseable++
			pf("unparseable: %v\n", err)
			continue
		}

		caseCtx, cancel := context.WithTimeout(ctx, deadline)
		res, err := resolver.Resolve(caseCtx, reqs, idx, opts)
		timedOut := caseCtx.Err() != nil
		cancel()
		if timedOut {
			stats.Timeouts++
			pf("TIMEOUT\n")
			if run.haltOnTimeout {
				// Written into the transcript so a truncated run is obviously
				// truncated rather than mysteriously short.
				pf("HALTED after the first timeout; %d case(s) not attempted\n",
					len(cases)-stats.Cases)
				break
			}
			continue
		}

		var re *resolver.ResolutionError
		switch {
		case errors.As(err, &re):
			// A report with one line is "package X is not here" and nothing
			// else. See transcriptStats.Vacuous.
			if re.Report != nil && len(re.Report.Lines) > 1 {
				stats.Derivations++
			} else {
				stats.Vacuous++
			}
			pf("%s\n", renderFailure(re))
		case err != nil:
			// Counted, and asserted to be zero, but credited to NEITHER
			// discriminating class: an error that is not a resolution failure is
			// a harness or index problem, and counting it as coverage is how a
			// broken run looks healthy.
			stats.Errors++
			pf("ERROR %v\n", err)
		default:
			if len(res.Pinned) >= 2 {
				stats.Deep++
			} else {
				stats.Shallow++
			}
			withExtras := false
			// Order is printed as the solver produced it, NOT sorted. Sorting
			// would hide exactly the kind of change this is looking for: a
			// different search order reaching the same pins.
			for _, name := range res.Order {
				pf("  %s %s", name, res.Pinned[name])
				if extras := res.Extras[name]; len(extras) > 0 {
					withExtras = true
					pf(" [%s]", strings.Join(extras, ","))
				}
				pf("\n")
			}
			if withExtras {
				stats.Extras++
			}
		}
	}

	return stats
}

// renderFailure renders a resolution failure into the transcript.
//
// # ⚠️ Why the report is HASHED rather than written out
//
// The report text is the thing most likely to shift under a search-order change
// and the thing a user actually sees, so the transcript has to be sensitive to
// every byte of it. It does not have to CONTAIN every byte of it. Written out in
// full (GPR_TRANSCRIPT_REPORTS=full, GPR_TRANSCRIPT_FULL=1), the excerpt produces
// a 1,465,675-byte golden file, of which 1.18 MB is nine failure reports
// enumerating whole version sets -- ipykernel alone is 293 KB and prompt-toolkit
// 227 KB. That is a larger artifact than the 1,010,960-byte RSF fixture it is
// derived from, committed to a public repository, for a diff nobody would read.
//
// A SHA-256 over the exact Error() text keeps the sensitivity exactly: any
// change to any byte fails the comparison. What it costs is localization -- a
// mismatch says which CASE moved, not which character -- so the conclusion line
// is kept verbatim (truncated), because that sentence is the human-readable
// summary of the whole report and usually the first thing to change.
//
// Set GPR_TRANSCRIPT_REPORTS=full to write reports out in full, which is what to
// do when a hash mismatch needs investigating: regenerate both sides that way
// and diff.
func renderFailure(re *resolver.ResolutionError) string {
	text := re.Error()

	if os.Getenv("GPR_TRANSCRIPT_REPORTS") == "full" {
		return "FAILED\n" + text
	}

	lines := 0
	if re.Report != nil {
		lines = len(re.Report.Lines)
	}
	sum := sha256.Sum256([]byte(text))

	// The conclusion, which report renders as the LAST line: every preceding
	// line is a derivation step leading to it.
	conclusion := ""
	if re.Report != nil && lines > 0 {
		conclusion = re.Report.Lines[lines-1].Text
	}

	return fmt.Sprintf("FAILED lines=%d bytes=%d sha256=%s\n  %s",
		lines, len(text), hex.EncodeToString(sum[:]), truncate(conclusion, 200))
}

// truncate bounds one line and says how much it dropped, so a change in the
// elided tail's LENGTH is visible in the diff even though its content is not.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s…+%d", s[:n], len(s)-n)
}

// excerptCases is the committed corpus followed by every package in the
// excerpt, one bare requirement each, in sorted order.
//
// Sorted and exhaustive rather than sampled: TestDumpResolutions samples because
// it runs against 932,861 packages and cannot do otherwise, and it takes a seed
// so a run is repeatable. Here the whole population is 139, so there is nothing
// to sample and no seed to get wrong.
//
// ⚠️ `flask` appears twice -- once as the benchmark corpus's `small-tree` entry
// and once from the sweep -- and that is left alone deliberately. Filtering the
// sweep against the corpus would couple two committed lists whose only real
// relationship is that both happen to mention flask, and the duplicate is
// harmless: it resolves identically both times, and it is counted twice in
// Cases and Deep, which are compared against thresholds rather than published.
func excerptCases(t testing.TB, file *pypirsf.File, full bool) [][]string {
	t.Helper()

	var cases [][]string
	for _, entry := range benchCorpus {
		cases = append(cases, entry.Requirements)
	}

	names := file.Packages()
	sort.Strings(names)

	present := make(map[string]bool, len(names))
	for _, n := range names {
		present[n] = true
	}
	for _, held := range []map[string]string{slowExcerptPackages, unboundedExcerptPackages} {
		for name, why := range held {
			if !present[name] {
				t.Fatalf("a held-out list names %q (%s) but the excerpt no longer carries it. "+
					"Either the fixture was regenerated, in which case re-measure and update "+
					"the list, or the name is a typo and this exclusion has been excluding "+
					"nothing.", name, why)
			}
		}
	}

	for _, n := range names {
		if _, never := unboundedExcerptPackages[n]; never {
			continue
		}
		if _, slow := slowExcerptPackages[n]; slow && !full {
			continue
		}
		cases = append(cases, []string{n})
	}
	return cases
}

// TestResolutionTranscriptMatchesGolden is the equivalence check, run on every
// pull request against the committed excerpt.
//
// GPR_TRANSCRIPT_FULL=1 adds slowExcerptPackages and compares against the other
// golden file. That mode is what CI runs nightly; see the note on
// slowExcerptPackages for why it is not on every pull request.
//
// ⚠️ It FAILS rather than skips when the fixture is missing. A skipped
// equivalence check is worse than an absent one, because it is green.
func TestResolutionTranscriptMatchesGolden(t *testing.T) {
	full := os.Getenv("GPR_TRANSCRIPT_FULL") != ""
	golden, mode := goldenFast, "fast"
	if full {
		golden, mode = goldenFull, "full"
	}

	file, err := pypirsf.Open(excerptFixture)
	if err != nil {
		t.Fatalf("open %s: %v. This fixture is committed and this test must not be "+
			"skipped when it is absent; see the note at the top of this file.",
			excerptFixture, err)
	}
	defer func() { _ = file.Close() }()

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	cases := excerptCases(t, file, full)

	var buf strings.Builder
	stats := writeTranscript(t, &buf, transcriptRun{
		idx:           idx,
		cases:         cases,
		mode:          mode,
		deadline:      caseTimeout,
		haltOnTimeout: true,
	})
	got := buf.String()

	// Printed unconditionally, and matched by the CI step, so a run that
	// compared nothing cannot look like a run that compared everything.
	t.Log(stats.String())

	assertTranscriptIsDiscriminating(t, stats)

	if os.Getenv("GPR_UPDATE_TRANSCRIPT") != "" {
		// ⚠️ REFUSE to write when the run already failed, and this is not
		// belt-and-braces. assertTranscriptIsDiscriminating uses t.Errorf rather
		// than Fatalf, so without this guard a regeneration whose sweep timed out
		// would write the TIMEOUT line straight into the golden file and signal
		// it only through the exit status. That is how one got committed. The
		// counters can then honestly be said to PREVENT a poisoned golden rather
		// than merely to report one afterwards.
		if t.Failed() {
			t.Fatalf("refusing to write %s: this run failed its own checks above, so the "+
				"transcript it produced is not a baseline. Fix the failure first.", golden)
		}
		// ⚠️ And refuse when the reports are inline, which t.Failed() CANNOT
		// catch: GPR_TRANSCRIPT_REPORTS=full changes every FAILED line into a
		// full report and moves no counter at all, so the run passes and writes
		// a golden 20x larger (240 KB fast, 1.47 MB full -- bigger than the RSF
		// fixture, in a public repository).
		//
		// This is reachable by FOLLOWING THE INSTRUCTIONS, not by misusing them:
		// renderFailure's doc and the mismatch message below both tell the reader
		// to re-run with GPR_TRANSCRIPT_REPORTS=full to investigate, and anyone
		// who does that with GPR_UPDATE_TRANSCRIPT still exported from the
		// previous command silently rewrites the baseline from a green run.
		//
		// It is also why the guard above cannot be described as PREVENTING a
		// poisoned golden in general. It prevents the poisonings a counter sees;
		// this one needed its own check.
		if os.Getenv("GPR_TRANSCRIPT_REPORTS") == "full" {
			t.Fatalf("refusing to write %s with GPR_TRANSCRIPT_REPORTS=full: that mode "+
				"inlines every failure report, which is a diagnostic view rather than a "+
				"baseline, and it would grow this file about 20x. Unset it to regenerate.",
				golden)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", golden, err)
		}
		t.Logf("wrote %s (%d bytes); READ THE DIFF -- regenerating a golden file is "+
			"how it stops being an assertion", golden, len(got))
		return
	}

	wantBytes, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v. Regenerate with GPR_UPDATE_TRANSCRIPT=1.", golden, err)
	}
	if want := string(wantBytes); got != want {
		t.Errorf("the resolution transcript differs from %s.\n\n%s\n\n"+
			"If this is an intended behaviour change, regenerate with "+
			"GPR_UPDATE_TRANSCRIPT=1 and put the diff in the changelog. If it is not, "+
			"an optimization has changed what a caller observes. A sha256 mismatch on a "+
			"FAILED line means the report text moved: re-run both sides with "+
			"GPR_TRANSCRIPT_REPORTS=full to see how.",
			golden, firstDifference(want, got))
	}
}

// Minimums for the discriminating classes.
//
// ⚠️ These exist because the excerpt is MACHINE-REGENERABLE and the generator
// does not select for these properties ON PURPOSE -- it selects for its own, and
// this test is a downstream consumer with no say in them.
//
// index/fixture_gen_test.go keeps flask's closure to fixtureDepth = 4, plus seven
// packages chosen for awkward SHAPES. The depth is why Deep is 31 rather than 0,
// so dependency depth is not unrepresented -- but it is incidental to this test:
// the generator would satisfy its own contract with a different root, a smaller
// depth, or a set of shape extras that resolve to themselves, and none of its
// assertions mention activated extras or derivable conflicts at all. Regenerate
// from a newer snapshot and the transcript could collapse to something that still
// matches its regenerated golden file and asserts nothing -- which is the failure
// this project has already had once.
//
// The thresholds are this test stating its own requirements rather than inheriting
// someone else's.
//
// The 2026-08-04 excerpt produces, in the per-pull-request set: 125 cases, 31
// deep, 14 derivations, 2 extras. minDeepResolutions and minDerivations are set
// at roughly half of theirs, not at the actual count, so that ordinary churn in
// a regenerated fixture does not fail the build while a collapse does.
//
// The other two are not halves, and neither is an oversight:
//
//   - minCases is 90 of 125, because case COUNT is the one number that should
//     not have generous slack. It is not a coverage measure -- that is what the
//     class minimums are for -- it is a tripwire for a sweep that stopped
//     sweeping, and halving it would let the corpus lose a third of itself
//     silently.
//   - minExtras is set AT the actual count, because there is no headroom to
//     give. Exactly two cases in the whole sweep activate an extra --
//
// the committed flask[async] corpus entry, and pytest-cov, which requests one
// transitively. Halving that to 1 would leave the corpus entry alone satisfying
// it, and a corpus entry cannot detect that the excerpt stopped covering extras.
// If a regenerated fixture trips this, the right fix is to add a package that
// exercises the virtual-package path, not to lower the number.
const (
	minDeepResolutions = 15
	minDerivations     = 7
	minExtras          = 2
	minCases           = 90
)

func assertTranscriptIsDiscriminating(t *testing.T, s transcriptStats) {
	t.Helper()

	// ⚠️ A halted sweep has no coverage to judge, and judging it anyway produces
	// four confident errors with the wrong cause. Halting stops at the first
	// timeout, so Cases, Deep, Derivations and Extras are all truncated: a single
	// hang at case 6 of 125 otherwise reports "the excerpt or the corpus has
	// shrunk", "only 2 resolutions pinned two or more packages", "only 0 failures
	// carried a derived report" and "only 1 resolutions activated an extra" --
	// none of which happened. Report the one thing that did and stop.
	//
	// The timeout assertion is deliberately BELOW this and still runs, so the run
	// fails; what is suppressed is only the misdiagnosis.
	if s.Timeouts > 0 {
		assertNoTimeouts(t, s)
		return
	}

	if s.Cases < minCases {
		t.Errorf("%d cases, want at least %d: the excerpt or the corpus has shrunk",
			s.Cases, minCases)
	}
	if s.Deep < minDeepResolutions {
		t.Errorf("only %d resolutions pinned two or more packages, want at least %d. "+
			"A transcript of single-package resolutions restates the index; it does not "+
			"exercise requirement mapping, set intersection or dependency ordering.",
			s.Deep, minDeepResolutions)
	}
	if s.Derivations < minDerivations {
		t.Errorf("only %d failures carried a derived report, want at least %d. Failures "+
			"whose whole report is \"this package is absent\" (%d of them here) are "+
			"agreement about the INDEX, not about the resolver -- counting those as "+
			"coverage is the specific mistake this threshold exists to prevent.",
			s.Derivations, minDerivations, s.Vacuous)
	}
	if s.Extras < minExtras {
		t.Errorf("only %d resolutions activated an extra, want at least %d. Nothing else "+
			"in the transcript exercises the provider's virtual-package expansion.",
			s.Extras, minExtras)
	}
	if s.Unparseable > 0 {
		t.Errorf("%d case(s) had unparseable requirements and were written to the "+
			"transcript without resolving anything", s.Unparseable)
	}
	// Zero, for the same reason as Timeouts. An ERROR is not a resolution the
	// resolver decided -- most likely the solver gave up -- and it is credited to
	// neither discriminating class, so a run that accumulated them would have less
	// coverage than its other counts suggest.
	if s.Errors > 0 {
		t.Errorf("%d case(s) returned an error that was not a *ResolutionError, most likely "+
			"the solver giving up. That is credited to neither discriminating class, so "+
			"this run covers less than its other counts suggest.", s.Errors)
	}
	// The classes must account for every case, so that adding an outcome to
	// writeTranscript and forgetting to count it is caught.
	//
	// ⚠️ It is a RUNTIME check and its reach is narrower than it looks: it fires
	// only when an uncounted class is also NON-EMPTY. ERROR went uncounted here
	// for two commits with zero erroring cases, and this assertion would have sat
	// green throughout -- it catches the co-occurrence, not the omission. The
	// omission is caught by reading writeTranscript against this list, which is
	// what the comment on each field is for.
	if got := s.Deep + s.Shallow + s.Derivations + s.Vacuous + s.Unparseable + s.Errors + s.Timeouts; got != s.Cases {
		t.Errorf("the classes sum to %d but %d cases were written: %d case(s) unaccounted "+
			"for. A NEGATIVE number means some outcome increments two counters; a positive "+
			"one means an outcome increments none. (Extras cannot cause either -- it is an "+
			"overlay and is deliberately left out of this sum.)",
			got, s.Cases, s.Cases-got)
	}
	assertNoTimeouts(t, s)
}

// assertNoTimeouts is the one check that still runs on a halted sweep.
//
// ⚠️ Zero, not a minimum. caseTimeout is a hang guard several times above the
// worst real case, and a TIMEOUT line is written into the transcript and compared
// like any other -- so tolerating one would commit an assertion about wall clock
// to a golden file and fail on whichever machine disagreed. If a case genuinely
// belongs over that bound it belongs in unboundedExcerptPackages, which is
// deterministic. This is the assertion that would have caught `hypothesis` being
// committed as a TIMEOUT line in the first version of this file.
func assertNoTimeouts(t *testing.T, s transcriptStats) {
	t.Helper()

	if s.Timeouts > 0 {
		t.Errorf("%d case(s) hit the %v hang guard, and the sweep halted at case %d. A "+
			"TIMEOUT line is machine-dependent and is compared byte for byte, so it must "+
			"not reach a golden file; move the case to unboundedExcerptPackages or find "+
			"out why it got that slow. ⚠️ The coverage minimums are NOT reported for this "+
			"run: halting truncates every class, so they would all read as collapsed "+
			"coverage rather than as one hang.",
			s.Timeouts, caseTimeout, s.Cases)
	}
}

// firstDifference renders the first differing line with a little context, which
// is what a golden-file failure needs and what a full diff of a 200 KB file
// actively obstructs.
func firstDifference(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")

	// The case header the difference falls under, so a failure names the
	// requirement rather than only a line number.
	//
	// Updated AFTER the inequality test, not before: when the differing line is
	// itself a case header, reporting it as its own context says nothing, and
	// the previous case is the useful bearing.
	header := "(before the first case)"
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("first difference at line %d, under %s:\n  golden: %q\n  got:    %q",
				i+1, header, w, g)
		}
		if strings.HasPrefix(w, "=== ") {
			header = w
		}
	}
	return "(no line differs; the files differ only in trailing bytes)"
}
