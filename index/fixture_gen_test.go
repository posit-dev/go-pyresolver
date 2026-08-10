// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// This file GENERATES testdata/pypi-trimmed.rsf, the committed excerpt of a real
// production PyPI snapshot that TestRSFIndexAgainstRealFile runs against. It is a
// test rather than a program under testdata/ so that it is compiled, vetted and
// linted like everything else -- the go tool ignores testdata directories
// entirely, so a generator living there would rot unnoticed.
//
// See testdata/README.md for provenance and the regeneration command.

// trimSrcEnv names the full snapshot to cut the fixture from.
const trimSrcEnv = "PYPIRSF_TRIM_SRC"

// trimmedFixturePath is the committed excerpt, relative to this package.
const trimmedFixturePath = "testdata/pypi-trimmed.rsf"

// fixtureExtras are packages included for the SHAPES they carry rather than
// because the walk reaches them. Each is a state the synthetic fixtures assert
// against by construction; having the producer's own bytes for the same state is
// what proves the two agree.
//
// ⚠️ Every entry needs a test that asserts something about it. An unasserted
// package is dead weight in a file that is permanent repository weight.
var fixtureExtras = []string{
	// One stored key, "0.2.1.Perceval", which PEP 440 rejects -- so Versions is
	// empty while the record carries a real dependency on sqlobject.
	"holygrail",
	// Three of its four versions carry "cryptography (>=3.3.2<4)", which PEP 508
	// rejects: ErrMetadataUnusable, with 0.2.0 usable beside them.
	"aad-token-verify",
	// Stored keys "1.0" and "1.0.0" are PEP 440-EQUAL with different dependency
	// sets, plus an unrelated 2.2.3. The class must collapse to one version.
	"database-connector",
	// Keys "0.05" and "0.5" are equal, and the sibling is permanently
	// unreachable -- the same class as above with a different spelling.
	"guessproj",
	// 0.15.0 requires "clip @ git+https://github.com/openai/CLIP@main": a direct
	// reference whose label collides with an unrelated PyPI project.
	"memery",
	// Requires-Python ">=3.6.*", which PEP 440 rejects: the record declared a
	// constraint that cannot be read, which is not the same as declaring none.
	"admobilize-malos",
	// Two keys, "0.1.0dev" and "0.1dev", that are equal to each other and
	// canonical as neither -- the ambiguous-spelling path.
	"anpy",
}

// fixtureRoot and fixtureDepth must match the walk in
// TestRSFIndexAgainstRealFile, or the trimmed file will not contain everything
// that walk reaches.
const (
	fixtureRoot  = "flask"
	fixtureDepth = 4
)

// TestGenerateTrimmedFixture writes testdata/pypi-trimmed.rsf from a full
// snapshot.
//
// Skipped unless PYPIRSF_TRIM_SRC names one, so an ordinary run does not need a
// ~1 GB file. Regenerating is a deliberate act: the fixture is committed, and a
// regeneration that changes it changes what CI is asserting against.
//
//	PYPIRSF_TRIM_SRC=/path/to/pypi.rsf go test ./index/ -run TestGenerateTrimmedFixture -v
func TestGenerateTrimmedFixture(t *testing.T) {
	src := os.Getenv(trimSrcEnv)
	if src == "" {
		t.Skipf("set %s to a full decompressed PyPI RSF to regenerate %s",
			trimSrcEnv, trimmedFixturePath)
	}

	keep, err := fixturePackages(src)
	if err != nil {
		t.Fatalf("choosing packages: %v", err)
	}
	t.Logf("keeping %d packages (%s closure to depth %d, plus %d shape extras)",
		len(keep), fixtureRoot, fixtureDepth, len(fixtureExtras))

	if err := os.MkdirAll(filepath.Dir(trimmedFixturePath), 0o755); err != nil {
		t.Fatalf("creating testdata: %v", err)
	}
	written, err := trimRSF(src, trimmedFixturePath, keep)
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	info, err := os.Stat(trimmedFixturePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	t.Logf("wrote %s: %d records, %d bytes", trimmedFixturePath, written, info.Size())

	// The excerpt has to satisfy the test that consumes it, and finding that out
	// here beats finding it out in CI.
	file, err := pypirsf.Open(trimmedFixturePath)
	if err != nil {
		t.Fatalf("reopening the trimmed file: %v", err)
	}
	defer func() { _ = file.Close() }()
	idx, err := NewRSFIndex(file, "trimmed")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	got, err := walkClosure(idx, fixtureRoot, fixtureDepth)
	if err != nil {
		t.Fatalf("re-walking the trimmed file: %v", err)
	}
	full, err := fixtureClosure(src)
	if err != nil {
		t.Fatalf("re-walking the source: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(full, ",") {
		t.Errorf("the walk over the excerpt reaches %d packages but reaches %d in the source; "+
			"the excerpt is missing something the walk needs", len(got), len(full))
	}
	for _, name := range fixtureExtras {
		if !file.Has(name) {
			t.Errorf("shape extra %q did not make it into the excerpt", name)
		}
	}
}

// fixturePackages returns every canonical name the excerpt must carry.
func fixturePackages(src string) ([]string, error) {
	closure, err := fixtureClosure(src)
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool, len(closure)+len(fixtureExtras))
	for _, n := range closure {
		set[n] = true
	}
	for _, n := range fixtureExtras {
		set[NewPackageName(n).String()] = true
	}

	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// fixtureClosure opens src and returns the closure the real-file test walks.
func fixtureClosure(src string) ([]string, error) {
	file, err := pypirsf.Open(src)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	idx, err := NewRSFIndex(file, "source")
	if err != nil {
		return nil, err
	}
	return walkClosure(idx, fixtureRoot, fixtureDepth)
}

// walkClosure reproduces TestRSFIndexAgainstRealFile's traversal and returns the
// names it visited, sorted.
//
// Deliberately a copy of that walk rather than a shared helper it calls: the
// generator's job is to satisfy the test as written, so if the test's traversal
// changes, this must be updated on purpose and the fixture regenerated. A shared
// helper would let the two drift silently in the direction that matters least --
// the test narrowing while the fixture keeps satisfying it.
func walkClosure(idx *RSFIndex, root string, maxDepth int) ([]string, error) {
	ctx := context.Background()
	seen := map[PackageName]bool{}
	frontier := []PackageName{NewPackageName(root)}

	for depth := 0; len(frontier) > 0 && depth < maxDepth; depth++ {
		var next []PackageName
		for _, pkg := range frontier {
			if seen[pkg] {
				continue
			}
			seen[pkg] = true

			versions, err := idx.Versions(ctx, pkg)
			if err != nil {
				if errors.Is(err, ErrPackageNotFound) {
					continue
				}
				return nil, fmt.Errorf("Versions(%q): %w", pkg, err)
			}
			if len(versions) == 0 {
				continue
			}
			newest := versions[0]
			for _, v := range versions[1:] {
				if v.GreaterThan(newest) {
					newest = v
				}
			}
			meta, err := idx.Metadata(ctx, pkg, newest)
			if err != nil {
				// A version whose metadata does not conform is a real state of the
				// corpus, and the test tolerates it; the walk simply stops there.
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

	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg.String())
	}
	sort.Strings(out)
	return out, nil
}

// trimRSF copies the schema header, the source's FIRST record, and every record
// named in keep into dst, byte for byte.
//
// # Why the bytes are copied rather than re-encoded
//
// The point of the fixture is to exercise what the PRODUCER emits: the schema it
// declares, the snapshots array this reader deliberately skips wholesale (three
// element layouts have existed, and reading one wrong desynchronizes every
// following record), and dependency blobs compressed against a trained zstd
// dictionary. Re-encoding with this module's own writer would produce a file that
// proves only self-consistency, which the synthetic fixtures already do.
//
// # Why the first record is always kept
//
// The global dependency dictionary is carried on the first record of a file and
// nowhere else, so the excerpt's first record must be the source's first record
// or nothing that uses the dictionary can be decoded.
func trimRSF(src, dst string, keep []string) (int, error) {
	wanted := make(map[string]bool, len(keep))
	for _, n := range keep {
		wanted[n] = true
	}

	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	type span struct {
		offset int64
		size   int64
	}
	var headerLen int64
	var spans []span

	buf := bufio.NewReaderSize(f, 1<<20)
	r := rsf.NewReader()
	if _, err := r.ReadIndex(buf); err != nil {
		return 0, fmt.Errorf("reading schema: %w", err)
	}
	headerLen = int64(r.Pos())

	first := true
	for {
		recordStart := r.Pos()
		recordSize, err := r.ReadSizeField(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("record size at %d: %w", recordStart, err)
		}
		recordEnd := recordStart + recordSize

		if err := r.AdvanceTo(buf, "cname"); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, fmt.Errorf("advancing to cname at %d: %w", recordStart, err)
		}
		cname, err := r.ReadStringField(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("cname at %d: %w", recordStart, err)
		}

		if first || wanted[cname] {
			spans = append(spans, span{offset: int64(recordStart), size: int64(recordSize)})
		}
		first = false

		remaining := recordEnd - r.Pos()
		if err := r.Discard(remaining, buf, rsf.Top); err != nil {
			return 0, fmt.Errorf("skipping past %d: %w", recordStart, err)
		}
	}

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, io.NewSectionReader(f, 0, headerLen)); err != nil {
		return 0, fmt.Errorf("copying schema: %w", err)
	}
	for _, s := range spans {
		if _, err := io.Copy(out, io.NewSectionReader(f, s.offset, s.size)); err != nil {
			return 0, fmt.Errorf("copying record at %d: %w", s.offset, err)
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return len(spans), nil
}
