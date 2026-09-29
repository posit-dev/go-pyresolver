// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldFileEnv names a real, pre-yank-history RSF snapshot too large to commit.
// Skipped when unset, unlike index/rsfindex_real_test.go's mandatory real-file
// tests: those have a committed excerpt to fall back to, and these do not --
// the property under test (an old file with no sentinel record still opens and
// reports no yanks) has no synthetic substitute that would make skipping wrong.
const oldFileEnv = "PYPIRSF_OLD_FILE"

// openOldFile resolves oldFileEnv, expanding a leading "~/" the same way
// resolver/bench_test.go's benchSnapshot does.
func openOldFile(t *testing.T) *File {
	t.Helper()

	path := os.Getenv(oldFileEnv)
	if path == "" {
		t.Skipf("%s unset; set it to a real pre-yank-history RSF to run this check "+
			"(e.g. ~/.cache/ppm-rsf/prod.rsf)", oldFileEnv)
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("cannot expand %q: %v", path, err)
		}
		path = filepath.Join(home, path[2:])
	}

	file, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// TestOldFileHasNoYankHistory is the brief's mandatory trap: a real snapshot
// predating this feature must still open, must report YanksCaptured() false
// (no sentinel record exists to say otherwise), and must decode dependency
// data for every record exactly as v0.13.0 did -- since no sentinel is
// present, this reader never has a history to apply, so "matches v0.13.0's
// output" reduces to "no version anywhere reports Yanked = true".
//
// Run explicitly (too large to commit, and not needed for CI to pass):
//
//	PYPIRSF_OLD_FILE=~/.cache/ppm-rsf/prod.rsf \
//	  go test ./pypirsf/ -run TestOldFileHasNoYankHistory -v -timeout 900s
//	PYPIRSF_OLD_FILE=~/.cache/ppm-rsf/1790566985.rsf \
//	  go test ./pypirsf/ -run TestOldFileHasNoYankHistory -v -timeout 900s
func TestOldFileHasNoYankHistory(t *testing.T) {
	file := openOldFile(t)

	if file.YanksCaptured() {
		t.Error("YanksCaptured() = true for a file with no yank-history sentinel")
	}

	packages := file.Packages()
	if len(packages) == 0 {
		t.Fatal("Packages() is empty; this is not the corpus the trap is meant to run over")
	}

	var versions, yanked int
	for _, name := range packages {
		deps, err := file.Deps(name)
		if err != nil {
			t.Fatalf("Deps(%q): %v", name, err)
		}
		for key, d := range deps {
			versions++
			if d.Yanked {
				yanked++
				t.Errorf("%s %s: Yanked = true, want false (no sentinel exists in this file)",
					name, key)
			}
		}
	}

	t.Logf("%s: %d packages, %d versions, %d reported yanked (want 0)",
		os.Getenv(oldFileEnv), len(packages), versions, yanked)
}
