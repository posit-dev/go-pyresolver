// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	"github.com/posit-dev/go-pyresolver/resolver"
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
// test: it asserts nothing on its own.
//
// The transcript carries the OBSERVABLE surface of a resolution and nothing
// else: the pins, the order the solver decided them in, the activated extras,
// and for a failure the full report text. Timings and call counts are
// deliberately absent -- those are expected to differ, and including them would
// make the diff useless for the question being asked.
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

	ctx := context.Background()

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

	for _, reqStrings := range cases {
		fmt.Fprintf(w, "=== %s\n", strings.Join(reqStrings, " "))

		reqs, err := requirementsOrNil(reqStrings)
		if err != nil {
			fmt.Fprintf(w, "unparseable: %v\n", err)
			continue
		}

		caseCtx, cancel := context.WithTimeout(ctx, deadline)
		res, err := resolver.Resolve(caseCtx, reqs, idx, testOptions(t))
		timedOut := caseCtx.Err() != nil
		cancel()
		if timedOut {
			fmt.Fprintln(w, "TIMEOUT")
			continue
		}

		var re *resolver.ResolutionError
		switch {
		case errors.As(err, &re):
			// The full report text, which is the user-facing artifact of a
			// failed resolve and the thing most likely to shift if search order
			// changed.
			fmt.Fprintf(w, "FAILED\n%s\n", re.Error())
		case err != nil:
			fmt.Fprintf(w, "ERROR %v\n", err)
		default:
			// Order is printed as the solver produced it, NOT sorted. Sorting it
			// would hide exactly the kind of change this is looking for: a
			// different search order that happens to reach the same pins.
			for _, name := range res.Order {
				fmt.Fprintf(w, "  %s %s", name, res.Pinned[name])
				if extras := res.Extras[name]; len(extras) > 0 {
					fmt.Fprintf(w, " [%s]", strings.Join(extras, ","))
				}
				fmt.Fprintln(w)
			}
		}
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
