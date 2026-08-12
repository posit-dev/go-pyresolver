// SPDX-License-Identifier: Apache-2.0 OR MIT

// This differential is an EXTERNAL test package (pep440set_test) on purpose: it
// imports index, and an in-package test importing index would make any future
// index -> pep440set dependency an import cycle rather than a design decision.
package pep440set_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// corpusFixturePath is the committed excerpt this test reads by default. It
// lives in index/testdata because that is where the fixture generator writes
// it; see index/testdata/README.md.
const corpusFixturePath = "../index/testdata/pypi-trimmed.rsf"

// corpusFileEnv is the SAME env var index's producer-output tests use. Pointing
// it at a full snapshot runs this differential over the whole corpus:
//
//	PYPIRSF_TEST_FILE=~/.cache/ppm-rsf/prod.rsf \
//	  go test ./pep440set/ -run TestDifferentialAgainstRealCorpus -v
const corpusFileEnv = "PYPIRSF_TEST_FILE"

// corpusPackagesEnv bounds the walk. The default is sized so the run against
// the committed excerpt finishes in well under a minute on every machine; a
// large sweep is an explicit request.
const corpusPackagesEnv = "PEP440SET_CORPUS_PACKAGES"

const (
	defaultCorpusPackages = 400
	maxVersionsPerPackage = 12
	maxReportedMismatches = 25
)

// corpusIndex opens the snapshot, following index's convention exactly.
//
// ⚠️ A MISSING FILE IS A FAILURE, NOT A SKIP, for the reason spelled out in
// index/rsfindex_real_test.go: a skip would hide a deleted fixture behind a
// green run, and this is the only test in the package that sees the specifier
// shapes real publishers write.
func corpusIndex(t *testing.T) *index.RSFIndex {
	t.Helper()

	path := os.Getenv(corpusFileEnv)
	if path == "" {
		path = corpusFixturePath
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("cannot expand %q: %v", path, err)
		}
		path = filepath.Join(home, path[2:])
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cannot stat %s: %v\n\nThe committed excerpt at %s is what makes this test "+
			"run in CI. If it is missing, restore it from git or regenerate it: see "+
			"index/testdata/README.md. Set %s to run against a full snapshot instead.",
			path, err, corpusFixturePath, corpusFileEnv)
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	return idx
}

// sample takes at most n items spread evenly across the whole slice.
//
// Evenly rather than the first n: package names are sorted, and versions come
// back in the index's own order, so a prefix would sample one corner of the
// alphabet and one end of a release history. The point of a real-corpus run is
// the shapes nobody thought to write down, and those are not clustered.
func sample[T any](items []T, n int) []T {
	if n <= 0 || len(items) <= n {
		return items
	}
	out := make([]T, 0, n)
	stride := float64(len(items)) / float64(n)
	for i := range n {
		out = append(out, items[int(float64(i)*stride)])
	}
	return out
}

// TestDifferentialAgainstRealCorpus runs the Contains-versus-Check differential
// over the specifiers real packages actually publish.
//
// The generated grids in differential_test.go are the readable specification of
// the mapping, and they can only contain shapes someone thought to type. A PyPI
// snapshot carries the ones nobody would: `>=1.dev0`, `~=0.0.1a1.post2`, epochs
// in the wild, four-segment wildcards, operands with local labels, and
// specifier sets a dozen clauses long. construct.go does hand-rolled string
// arithmetic on those operands (splitting on ".", incrementing the last
// segment), so the corpus is where an operand shape the arithmetic mishandles
// would first appear.
//
// Each requirement is compared against every version of the package it names --
// the versions a resolver would actually be choosing between -- plus the
// operands of the specifier itself, since a boundary bug shows up AT the
// boundary and the dep's own release list need not contain it.
func TestDifferentialAgainstRealCorpus(t *testing.T) {
	idx := corpusIndex(t)
	ctx := context.Background()

	limit := defaultCorpusPackages
	if raw := os.Getenv(corpusPackagesEnv); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s=%q: %v", corpusPackagesEnv, raw, err)
		}
		limit = n
	}

	all := idx.Packages()
	packages := sample(all, limit)

	// Version lists are reused across every requirement naming the same
	// package, and each miss decompresses a record.
	versionsOf := map[index.PackageName][]version.Version{}
	depVersions := func(pkg index.PackageName) []version.Version {
		if vs, ok := versionsOf[pkg]; ok {
			return vs
		}
		vs, err := idx.Versions(ctx, pkg)
		if err != nil {
			// A requirement naming a package this snapshot does not carry is
			// ordinary: direct URL labels, private indexes, deletions. The
			// operands still get compared below.
			vs = nil
		}
		versionsOf[pkg] = vs
		return vs
	}

	var (
		pairs           int64
		specifiersSeen  int
		unrepresentable int
		unconstrained   int
		metadataErrs    int
		mismatches      int
		reported        int
	)

	start := time.Now()
	for _, raw := range packages {
		pkg := index.NewPackageName(raw)

		vers, err := idx.Versions(ctx, pkg)
		if err != nil {
			continue
		}
		versionsOf[pkg] = vers

		for _, ver := range sample(vers, maxVersionsPerPackage) {
			meta, err := idx.Metadata(ctx, pkg, ver)
			if err != nil {
				// Unusable or unavailable metadata is a data condition the
				// index already reports; it is not this test's subject.
				metadataErrs++
				continue
			}

			for _, req := range meta.RequiresDist {
				ss := req.Specifiers
				if ss.String() == "" {
					// An unconstrained requirement compares nothing: every
					// version matches under both answers by construction.
					unconstrained++
					continue
				}
				specifiersSeen++

				set, err := pep440set.FromSpecifiers(ss)
				if err != nil {
					if errors.Is(err, pep440set.ErrUnrepresentable) {
						// `===` and the `||` extension, refused deliberately.
						unrepresentable++
						continue
					}
					t.Errorf("%s %s requires %q: FromSpecifiers(%s): %v",
						pkg, ver, req.Name, ss.String(), err)
					continue
				}

				candidates := depVersions(index.NewPackageName(req.Name))
				// The operands themselves: the boundary is where a bound that
				// is off by one position shows up, and the dep's release list
				// will not always contain it.
				probes := make([]version.Version, 0, len(candidates)+2*len(ss.List()))
				probes = append(probes, candidates...)
				for _, sp := range ss.List() {
					operand := strings.TrimSuffix(strings.TrimSpace(sp.Version()), ".*")
					if operand == "" {
						continue
					}
					if v, err := version.Parse(operand); err == nil {
						probes = append(probes, v)
					}
				}

				for _, v := range probes {
					want := ss.Check(v)
					got := set.Contains(v)
					pairs++
					if got == want {
						continue
					}
					mismatches++
					if reported < maxReportedMismatches {
						reported++
						t.Errorf("MISMATCH: specifier %q vs version %q: Contains=%v Check=%v "+
							"(from %s %s requiring %s)",
							ss.String(), v.Original(), got, want, pkg, ver, req.Name)
					}
				}
			}
		}
	}

	elapsed := time.Since(start)
	t.Logf("corpus: %d of %d packages, %d specifier sets, %d (specifier, version) pairs in %s",
		len(packages), len(all), specifiersSeen, pairs, elapsed)
	t.Logf("skipped: %d unrepresentable, %d unconstrained; %d metadata lookups refused",
		unrepresentable, unconstrained, metadataErrs)
	if mismatches > reported {
		t.Errorf("%d disagreements total; %d reported above", mismatches, reported)
	}

	// A run that compared nothing would be green and meaningless -- the failure
	// mode the producer-output tests exist to prevent.
	if pairs < 1000 {
		t.Errorf("only %d pairs compared over %d packages; the walk is not reaching real "+
			"requirements", pairs, len(packages))
	}
	if specifiersSeen < 100 {
		t.Errorf("only %d constrained requirements seen; this is not a corpus run", specifiersSeen)
	}
}
