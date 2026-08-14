// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"bufio"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/requirement"
)

// TestDumpResolutions writes a canonical transcript of what a set of
// requirements resolves to, so two builds of this module can be compared byte
// for byte.
//
// # Why a dump rather than an in-tree assertion
//
// The property under test -- "this optimization changes nothing an caller can
// observe" -- is a statement about two BUILDS, and no test that runs inside one
// build can make it. So this runs unchanged in both trees, writes to
// GPR_DUMP_OUT, and the comparison is a diff.
//
// It is skipped unless GPR_DUMP_OUT is set, because it is a tool rather than a
// test: it asserts nothing on its own. ⚠️ Which also means it never runs in CI,
// so a figure quoted from it is a figure from someone's laptop.
//
// # This is the FULL-SNAPSHOT mode, and it is the env-gated half on purpose
//
// The CI-reproducible half is TestResolutionTranscriptMatchesGolden in
// equivalence_test.go: same transcript, excerpt-sized, compared against a
// committed golden file, and it must not skip. This one keeps the production
// snapshot and the two-build diff, which cannot be made to run on a CI runner.
//
// Both call writeTranscript, so the two cannot drift into rendering the same
// resolution differently -- which would silently invalidate every published
// figure produced by this tool.
//
// # The invocation behind the published figures, so they are reproducible
//
// Neither the defaults below nor a bare run reproduce them; record the flags with
// the numbers or the numbers are not checkable:
//
//	PYPIRSF_TEST_FILE=~/.cache/ppm-rsf/prod.rsf GPR_SAMPLE=4000 GPR_SEED=1 \
//	  GPR_CASE_TIMEOUT=8s GPR_DUMP_OUT=<file> \
//	  go test ./resolver/ -run TestDumpResolutions -timeout 180m
//
// ⚠️ The committed default deadline is 10s and the published runs used 8s, so a
// default run produces a different exclusion set.
//
// # ⚠️ The excluded set is BIASED, and toward the interesting cases
//
// Exclusions are cases that hit the wall-clock deadline. The optimized side is
// several times faster, so a case near the deadline is far likelier to be
// excluded for timing out on the BASELINE than on the other side -- which means
// the excluded set skews toward the hardest, deepest resolutions, exactly where a
// divergence would be most likely to hide. It was 48 of 4,007 (1.2%) and 11 of
// 1,007 in the two published runs. Attributing them per side is worth doing:
// in the second run, 3 cases timed out on the baseline and finished on the
// optimized build, and none the other way.
//
// The transcript carries the OBSERVABLE surface of a resolution and nothing
// else: the pins, the order the solver decided them in, the activated extras,
// and for a failure the report. Timings and call counts are deliberately absent
// -- those are expected to differ, and including them would make the diff
// useless for the question being asked.
//
// ⚠️ The failure report is now recorded as a SHA-256 plus its conclusion line
// rather than inline, so a v2 transcript will differ from the v1 files behind
// the published 0.6.0 figures on every failing case. See renderFailure for why,
// and set GPR_TRANSCRIPT_REPORTS=full to get the old inline form back. Every
// transcript carries a format header on its first line so the two cannot be
// confused.
func TestDumpResolutions(t *testing.T) {
	out := os.Getenv("GPR_DUMP_OUT")
	if out == "" {
		t.Skip("set GPR_DUMP_OUT to write a resolution transcript")
	}

	path := os.Getenv("PYPIRSF_TEST_FILE")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("expand home: %v", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		path = "../index/testdata/pypi-trimmed.rsf"
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("create %s: %v", out, err)
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	defer func() { _ = w.Flush() }()

	// The committed corpus first: the seven entries the benchmark measures, so
	// the transcript covers the same inputs the numbers do.
	var cases [][]string
	for _, entry := range benchCorpus {
		cases = append(cases, entry.Requirements)
	}

	// Then a production sample: single-package requirements drawn from the
	// snapshot. One package each, which is a shallow resolve, but there are
	// thousands of them and between them they reach a far wider slice of real
	// metadata than seven hand-picked entries do.
	names := file.Packages()
	sort.Strings(names)
	sampleN := 0
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if sampleN, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	seed := int64(1)
	if s := os.Getenv("GPR_SEED"); s != "" {
		if seed, err = strconv.ParseInt(s, 10, 64); err != nil {
			t.Fatalf("GPR_SEED=%q: %v", s, err)
		}
	}
	if sampleN > 0 {
		rng := rand.New(rand.NewSource(seed))
		for i := 0; i < sampleN; i++ {
			cases = append(cases, []string{names[rng.Intn(len(names))]})
		}
	}

	// ⚠️ A per-case deadline, and it is NOT a flakiness workaround.
	//
	// A single-package requirement whose transitive dependencies are largely
	// absent from the snapshot puts the solver in the second cost regime
	// bench_test.go describes -- version-set algebra inside conflict resolution,
	// seconds to minutes for one resolve. Sampling thousands of real names hits
	// one of those every few hundred draws, and without a bound the run never
	// finishes.
	//
	// A wall-clock bound is not deterministic across two builds, so a case that
	// times out on either side is EXCLUDED from the comparison rather than
	// compared. That is what the marker line is for: the diff stays exact, and
	// the excluded count is reported instead of being quietly absorbed.
	deadline := 10 * time.Second
	if s := os.Getenv("GPR_CASE_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("GPR_CASE_TIMEOUT=%q: %v", s, err)
		}
		deadline = d
	}

	// The per-case rendering lives in writeTranscript, shared with the CI check.
	// The stats it returns are logged rather than asserted on: this is a tool,
	// and the assertions belong to the test that runs on every pull request.
	// haltOnTimeout stays false: here a TIMEOUT is the selection deadline doing
	// its job, and excluding that case is the point.
	stats := writeTranscript(t, w, transcriptRun{
		idx:      idx,
		cases:    cases,
		mode:     "dump",
		deadline: deadline,
	})
	t.Log(stats.String())

	// ⚠️ Flush explicitly and FAIL on its error. The deferred Flush above cannot
	// report one, and a transcript truncated by a write error would diff clean
	// against nothing or diff dirty for a reason that has nothing to do with the
	// resolver.
	if err := w.Flush(); err != nil {
		t.Fatalf("flush %s: %v", out, err)
	}
}

// requirementsOrNil parses without failing the test, so one unparseable sampled
// name does not end the run. mustRequirements calls t.Fatalf, which would.
func requirementsOrNil(ss []string) ([]requirement.Requirement, error) {
	out := make([]requirement.Requirement, 0, len(ss))
	for _, s := range ss {
		r, err := requirement.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
