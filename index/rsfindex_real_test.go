// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// TestRSFIndexAgainstRealFile walks a real transitive dependency chain through
// the whole metadata layer.
//
// Skipped unless PYPIRSF_TEST_FILE names a decompressed RSF. The synthetic
// fixtures prove the index handles what this package's own tests write; this
// proves it handles what the producer emits, and that the pieces compose — a
// requirement parsed out of one package's metadata can be used to look up the
// next package. That is the actual precondition for a solver, and it is not
// something unit fixtures can establish.
//
//	curl --compressed -o /tmp/pypi.rsf \
//	  https://rspm-sync.rstudio.com/pypi/manifest/v2/1/rsf/<checkpoint>.rsf
//	PYPIRSF_TEST_FILE=/tmp/pypi.rsf go test ./index/ -run TestRSFIndexAgainstRealFile -v
func TestRSFIndexAgainstRealFile(t *testing.T) {
	path := os.Getenv("PYPIRSF_TEST_FILE")
	if path == "" {
		t.Skip("set PYPIRSF_TEST_FILE to a decompressed PyPI RSF")
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open: %v", err)
	}
	defer func() { _ = file.Close() }()

	idx, err := NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	ctx := context.Background()

	// Walk outward from a root, resolving nothing but confirming every
	// discovered dependency name is present with usable metadata. A desynced
	// reader or a bad parse shows up here as a name that does not exist.
	const root = "flask"
	seen := map[PackageName]bool{}
	frontier := []PackageName{NewPackageName(root)}

	start := time.Now()
	lookups := 0
	var maxDepth int

	for depth := 0; len(frontier) > 0 && depth < 4; depth++ {
		maxDepth = depth
		var next []PackageName

		for _, pkg := range frontier {
			if seen[pkg] {
				continue
			}
			seen[pkg] = true

			versions, err := idx.Versions(ctx, pkg)
			lookups++
			if err != nil {
				t.Errorf("depth %d: Versions(%q): %v", depth, pkg, err)
				continue
			}
			if len(versions) == 0 {
				continue
			}

			// Highest version by PEP 440 ordering, which is the comparison a
			// resolver would use.
			newest := versions[0]
			for _, v := range versions[1:] {
				if v.GreaterThan(newest) {
					newest = v
				}
			}

			meta, err := idx.Metadata(ctx, pkg, newest)
			lookups++
			if err != nil {
				t.Errorf("depth %d: Metadata(%q, %s): %v", depth, pkg, newest, err)
				continue
			}

			for _, req := range meta.RequiresDist {
				dep := NewPackageName(req.Name)
				if !seen[dep] {
					next = append(next, dep)
				}
			}
		}

		frontier = next
	}

	elapsed := time.Since(start)
	t.Logf("walked %d packages to depth %d via %d index calls in %s",
		len(seen), maxDepth, lookups, elapsed)

	if len(seen) < 5 {
		t.Errorf("only reached %d packages from %q; the chain should fan out further", len(seen), root)
	}

	// Every discovered name must exist in the corpus. A requirement naming a
	// package the index cannot find would mean either a bad parse or a
	// normalization mismatch, both of which would break resolution.
	missing := 0
	for pkg := range seen {
		if _, err := idx.Versions(ctx, pkg); err != nil {
			t.Logf("discovered but not resolvable: %q (%v)", pkg, err)
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d discovered packages could not be looked up", missing, len(seen))
	}
}
