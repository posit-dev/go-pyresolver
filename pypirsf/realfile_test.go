// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealRSF validates the reader against a real, production-sized RSF.
//
// Skipped unless PYPIRSF_TEST_FILE names a decompressed RSF, because the file is
// ~1 GB and cannot be committed. It is kept as a test rather than thrown away
// because the synthetic fixtures in file_test.go are written by this same
// package's structs — they prove self-consistency, not that the reader handles
// what the producer actually emits.
//
// To get a file:
//
//	curl --compressed -o /tmp/pypi.rsf \
//	  https://rspm-sync.rstudio.com/pypi/manifest/v2/1/rsf/<checkpoint>.rsf
//	PYPIRSF_TEST_FILE=/tmp/pypi.rsf go test ./pypirsf/ -run TestRealRSF -v
//
// Checkpoint ids come from .../pypi/manifest/v2/1/checkpoints.json. Note that
// those files are licensed for use by Posit Package Manager customers.
func TestRealRSF(t *testing.T) {
	path := os.Getenv("PYPIRSF_TEST_FILE")
	if path == "" {
		t.Skip("set PYPIRSF_TEST_FILE to a decompressed PyPI RSF")
	}

	start := time.Now()
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	t.Logf("indexed %d records in %s", f.Len(), time.Since(start))

	if f.Len() < 100_000 {
		t.Errorf("only %d records; a production PyPI RSF has hundreds of thousands", f.Len())
	}
	if len(f.Dict().Names()) == 0 {
		t.Error("dictionary is empty; a production file has a trained name table")
	}

	// Well-known packages with dependencies that are stable facts about the
	// ecosystem rather than about any particular release. A desynced reader
	// yields garbled strings, so asserting on recognizable content is what makes
	// this test meaningful.
	for _, tc := range []struct {
		pkg         string
		wantDepName string
	}{
		{"flask", "werkzeug"},
		{"requests", "urllib3"},
		{"django", "asgiref"},
		{"boto3", "botocore"},
	} {
		deps, err := f.Deps(tc.pkg)
		if err != nil {
			t.Errorf("Deps(%q): %v", tc.pkg, err)
			continue
		}
		if len(deps) == 0 {
			t.Errorf("Deps(%q) returned no versions", tc.pkg)
			continue
		}

		found := false
		for _, vd := range deps {
			for _, req := range vd.RequiresDist {
				if strings.Contains(strings.ToLower(req), tc.wantDepName) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Errorf("no version of %q requires %q; the reader is probably desynchronized",
				tc.pkg, tc.wantDepName)
		}
	}

	// Lookups must not degrade with corpus size: they seek by offset rather
	// than scanning. A regression to scanning would show up here as seconds.
	lookupStart := time.Now()
	if _, err := f.Deps("flask"); err != nil {
		t.Fatalf("Deps(flask): %v", err)
	}
	if elapsed := time.Since(lookupStart); elapsed > 100*time.Millisecond {
		t.Errorf("a single lookup took %s; expected microseconds, so this is scanning rather than seeking", elapsed)
	}
}
