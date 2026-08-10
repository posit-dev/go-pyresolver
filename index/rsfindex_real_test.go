// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// realFile opens the snapshot the tests in this file run against, and reports
// whether it is the committed excerpt.
//
// ⚠️ A MISSING FILE IS A FAILURE, NOT A SKIP. These tests used to skip unless
// PYPIRSF_TEST_FILE named a ~1 GB download, which meant the only tests exercising
// PRODUCER output never ran in CI — the suite was green while the one property no
// synthetic fixture can establish went unchecked for months
// (rstudio/package-manager#19466). A committed excerpt removed the reason to skip,
// so skipping is now a defect: it would hide a deleted or corrupted fixture
// behind a passing run.
func realFile(t *testing.T) (*pypirsf.File, bool) {
	t.Helper()

	path := os.Getenv("PYPIRSF_TEST_FILE")
	excerpt := path == ""
	if excerpt {
		path = trimmedFixturePath
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cannot stat %s: %v\n\nThe committed excerpt at %s is what makes these tests "+
			"run in CI. If it is missing, restore it from git or regenerate it: see "+
			"testdata/README.md. Set %s to run against a full snapshot instead.",
			path, err, trimmedFixturePath, "PYPIRSF_TEST_FILE")
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, excerpt
}

// realIndex is realFile wrapped in an RSFIndex.
func realIndex(t *testing.T) (*RSFIndex, *pypirsf.File, bool) {
	t.Helper()

	file, excerpt := realFile(t)
	idx, err := NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	return idx, file, excerpt
}

// TestRSFIndexAgainstRealFile walks a real transitive dependency chain through
// the whole metadata layer.
//
// The synthetic fixtures prove the index handles what this package's own tests
// write; this proves it handles what the PRODUCER emits, and that the pieces
// compose — a requirement parsed out of one package's metadata can be used to
// look up the next package. That is the actual precondition for a solver, and it
// is not something unit fixtures can establish, since they are encoded by the
// same code under test.
//
// Runs against testdata/pypi-trimmed.rsf by default. Set PYPIRSF_TEST_FILE to a
// full decompressed snapshot to run the same assertions over the whole corpus:
//
//	curl --compressed -o /tmp/pypi.rsf \
//	  https://rspm-sync.rstudio.com/pypi/manifest/v2/1/rsf/<checkpoint>.rsf
//	PYPIRSF_TEST_FILE=/tmp/pypi.rsf go test ./index/ -run TestRSFIndexAgainstRealFile -v
func TestRSFIndexAgainstRealFile(t *testing.T) {
	idx, _, _ := realIndex(t)
	ctx := context.Background()

	// Walk outward from a root, resolving nothing but confirming every
	// discovered dependency name is present with usable metadata. A desynced
	// reader or a bad parse shows up here as a name that does not exist.
	seen := map[PackageName]bool{}
	frontier := []PackageName{NewPackageName(fixtureRoot)}

	start := time.Now()
	lookups := 0
	var maxDepth int

	for depth := 0; len(frontier) > 0 && depth < fixtureDepth; depth++ {
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
		t.Errorf("only reached %d packages from %q; the chain should fan out further",
			len(seen), fixtureRoot)
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

// TestRealFileContentIsDecodedCorrectly asserts on recognizable content, which is
// what makes the walk above meaningful.
//
// A desynchronized reader does not usually fail: it yields plausible-looking
// strings assembled from the wrong bytes, and a walk over those still "succeeds".
// flask requiring werkzeug is a stable fact about the ecosystem rather than about
// any release, and the dependency NAMES are dictionary-compressed in the blob, so
// getting them back intact exercises the shared name table too.
func TestRealFileContentIsDecodedCorrectly(t *testing.T) {
	idx, _, _ := realIndex(t)
	ctx := context.Background()

	pkg := NewPackageName("flask")
	versions, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions(flask): %v", err)
	}
	newest := versions[0]
	for _, v := range versions[1:] {
		if v.GreaterThan(newest) {
			newest = v
		}
	}

	meta, err := idx.Metadata(ctx, pkg, newest)
	if err != nil {
		t.Fatalf("Metadata(flask, %s): %v", newest, err)
	}

	want := map[string]bool{"werkzeug": false, "jinja2": false, "click": false, "itsdangerous": false}
	for _, req := range meta.RequiresDist {
		if _, ok := want[req.Name]; ok {
			want[req.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("flask %s does not require %q; the reader is probably desynchronized. "+
				"Requirements: %v", newest, name, meta.RequiresDist)
		}
	}

	// A marker-conditional requirement must keep its marker: dropping one turns a
	// conditional dependency into an unconditional one.
	conditional := false
	for _, req := range meta.RequiresDist {
		if !req.Marker.IsEmpty() {
			conditional = true
			break
		}
	}
	if !conditional {
		t.Errorf("no requirement of flask %s carries an environment marker, though the record "+
			"declares extras %v", newest, meta.ProvidesExtra)
	}

	if len(meta.ProvidesExtra) == 0 {
		t.Errorf("flask %s declares no extras; PEP 685 normalization is untested here", newest)
	}
	if meta.RequiresPython.String() == "" {
		t.Errorf("flask %s declares no Requires-Python", newest)
	}
}

// TestRealFileAwkwardShapes asserts the states the synthetic fixtures construct
// by hand, against the producer's own bytes for the same states.
//
// ⚠️ THIS IS THE HALF OF THE COVERAGE GAP THAT SURVIVED WIRING THE TEST INTO CI.
// The walk above passes on a real snapshot because its root is flask, whose
// closure is well-behaved: rstudio/package-manager#19466 measured 507 roots for
// which it would have failed. Reaching only well-behaved data is how a real-data
// test can be both green and uninformative, so each case below is a shape that
// was an actual defect, named with the package it was found on.
//
// When run against a user-supplied snapshot rather than the committed excerpt, a
// missing package is reported and skipped: PyPI deletions and yanks are real, and
// a newer snapshot legitimately may not carry one of these. Against the excerpt
// there is no such escape — the packages are in it by construction.
func TestRealFileAwkwardShapes(t *testing.T) {
	idx, file, excerpt := realIndex(t)
	ctx := context.Background()

	requirePkg := func(t *testing.T, name string) bool {
		t.Helper()
		if file.Has(NewPackageName(name).String()) {
			return true
		}
		if excerpt {
			t.Fatalf("%q is missing from %s; the excerpt is meant to carry it (see "+
				"fixtureExtras in fixture_gen_test.go)", name, trimmedFixturePath)
		}
		t.Logf("%q is not in this snapshot, so this shape is unchecked here", name)
		return false
	}

	// A package whose ONLY stored version key is one PEP 440 rejects. Versions is
	// empty, which is indistinguishable from "nothing was captured" unless
	// UnparseableVersionKeys is consulted -- and the record holds a real
	// dependency, so reporting no data would send someone hunting for it.
	t.Run("every version key unparseable", func(t *testing.T) {
		if !requirePkg(t, "holygrail") {
			return
		}
		pkg := NewPackageName("holygrail")

		vers, err := idx.Versions(ctx, pkg)
		if err != nil {
			t.Fatalf("Versions: %v", err)
		}
		if len(vers) != 0 {
			t.Errorf("Versions = %v, want empty: every stored key is rejected by PEP 440", vers)
		}

		bad, err := idx.UnparseableVersionKeys(ctx, pkg)
		if err != nil {
			t.Fatalf("UnparseableVersionKeys: %v", err)
		}
		if len(bad) == 0 {
			t.Fatal("UnparseableVersionKeys is empty, so an empty Versions is indistinguishable " +
				"from a package with nothing captured")
		}

		raw, err := file.Deps(pkg.String())
		if err != nil {
			t.Fatalf("Deps: %v", err)
		}
		for _, key := range bad {
			if len(raw[key].RequiresDist) > 0 {
				return
			}
		}
		t.Errorf("none of the rejected keys %v carries dependency data, so this no longer "+
			"demonstrates present-but-unreportable data", bad)
	})

	// Metadata that EXISTS and does not conform. The distinction from
	// "unavailable" is load-bearing: a resolver should try another version, and a
	// traversal should report the package and carry on rather than discard
	// everything it has learned.
	t.Run("requirement PEP 508 rejects", func(t *testing.T) {
		if !requirePkg(t, "aad-token-verify") {
			return
		}
		pkg := NewPackageName("aad-token-verify")

		bad, err := version.Parse("0.1.1")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		_, err = idx.Metadata(ctx, pkg, bad)
		if !errors.Is(err, ErrMetadataUnusable) {
			t.Errorf("Metadata(%s) error = %v, want ErrMetadataUnusable", bad, err)
		}
		if errors.Is(err, ErrMetadataUnavailable) {
			t.Errorf("Metadata(%s) also reports ErrMetadataUnavailable; the record is PRESENT, "+
				"and collapsing the two states is what made a data condition look like a "+
				"missing package", bad)
		}

		// A conforming version of the same package must still resolve, or the
		// refusal is not scoped to the version that earned it.
		good, err := version.Parse("0.2.0")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if _, err := idx.Metadata(ctx, pkg, good); err != nil {
			t.Errorf("Metadata(%s) = %v, want success: one unusable version must not make its "+
				"siblings unusable", good, err)
		}
	})

	// Two stored keys that are PEP 440-equal with CONTRADICTORY dependencies.
	// Offering both would hand a resolver candidates it cannot select between,
	// and the pick would decide the dependency graph.
	t.Run("PEP 440-equal keys with different dependencies", func(t *testing.T) {
		if !requirePkg(t, "database-connector") {
			return
		}
		pkg := NewPackageName("database-connector")

		raw, err := file.Deps(pkg.String())
		if err != nil {
			t.Fatalf("Deps: %v", err)
		}
		one, oneOK := raw["1.0"]
		two, twoOK := raw["1.0.0"]
		if !oneOK || !twoOK {
			t.Fatalf("expected stored keys 1.0 and 1.0.0, got %d keys", len(raw))
		}
		if len(one.RequiresDist) == len(two.RequiresDist) {
			t.Fatalf("the two spellings no longer disagree about dependencies (%v vs %v), so "+
				"this case no longer demonstrates the hazard", one.RequiresDist, two.RequiresDist)
		}

		vers, err := idx.Versions(ctx, pkg)
		if err != nil {
			t.Fatalf("Versions: %v", err)
		}
		for i := range vers {
			for j := i + 1; j < len(vers); j++ {
				if vers[i].Equal(vers[j]) {
					t.Errorf("Versions returned %s and %s, which compare EQUAL", vers[i], vers[j])
				}
			}
		}

		// The representative must resolve to ONE record, the same one every time.
		var rep version.Version
		for _, v := range vers {
			if v.String() == "1.0" {
				rep = v
			}
		}
		if rep.String() != "1.0" {
			t.Fatalf("Versions = %v, want the class to be represented by 1.0 (both spellings are "+
				"canonical, so the lexicographically smaller one wins)", vers)
		}
		first, err := idx.Metadata(ctx, pkg, rep)
		if err != nil {
			t.Fatalf("Metadata(%s): %v", rep, err)
		}
		for range 50 {
			again, err := idx.Metadata(ctx, pkg, rep)
			if err != nil {
				t.Fatalf("Metadata(%s): %v", rep, err)
			}
			if len(again.RequiresDist) != len(first.RequiresDist) {
				t.Fatalf("Metadata(%s) answered %v and then %v on the same file",
					rep, first.RequiresDist, again.RequiresDist)
			}
		}
	})

	// The same class where NEITHER spelling is canonical, so the answer rests
	// entirely on the tiebreak. This is the case the review caught being decided
	// by Go's randomized map order: 500 calls returned two different dependency
	// sets, 317 to 183.
	t.Run("ambiguous spelling resolves deterministically", func(t *testing.T) {
		if !requirePkg(t, "anpy") {
			return
		}
		pkg := NewPackageName("anpy")

		raw, err := file.Deps(pkg.String())
		if err != nil {
			t.Fatalf("Deps: %v", err)
		}
		lower, lowerOK := raw["0.1.0dev"]
		upper, upperOK := raw["0.1dev"]
		if !lowerOK || !upperOK {
			t.Fatalf("expected stored keys 0.1.0dev and 0.1dev, got %d keys", len(raw))
		}
		if len(lower.RequiresDist) == len(upper.RequiresDist) {
			t.Fatalf("the two spellings no longer disagree (%v vs %v), so a nondeterministic "+
				"choice between them would be invisible", lower.RequiresDist, upper.RequiresDist)
		}

		rep, err := version.Parse("0.1.0dev")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		// Neither key equals the request's canonical spelling, so this goes through
		// the PEP 440-equality fallback -- the path that was iterating a map.
		answers := map[string]int{}
		for range 200 {
			meta, err := idx.Metadata(ctx, pkg, rep)
			if err != nil {
				t.Fatalf("Metadata(%s): %v", rep, err)
			}
			names := ""
			for _, r := range meta.RequiresDist {
				names += r.Name + " "
			}
			answers[names]++
		}
		if len(answers) != 1 {
			t.Errorf("200 calls on ONE index returned %d different dependency sets: %v. "+
				"RSFIndex documents that the same file resolves the same way forever",
				len(answers), answers)
		}
	})

	// A direct reference: the name is a local label for whatever the URL
	// provides, not a lookup key. 87 of 98 such labels collide with an unrelated
	// PyPI project.
	t.Run("direct URL requirement", func(t *testing.T) {
		if !requirePkg(t, "memery") {
			return
		}

		ver, err := version.Parse("0.15.0")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		meta, err := idx.Metadata(ctx, NewPackageName("memery"), ver)
		if err != nil {
			t.Fatalf("Metadata: %v", err)
		}

		for _, req := range meta.RequiresDist {
			if req.Name != "clip" {
				continue
			}
			if req.URL == "" {
				t.Errorf("the %q requirement lost its URL, which is the only part that "+
					"identifies it: %s", req.Name, req.String())
			}
			return
		}
		t.Errorf("memery %s no longer carries the direct reference this case is about: %v",
			ver, meta.RequiresDist)
	})

	// An interpreter constraint the specification rejects. Treated as
	// unconstrained deliberately -- pip does the same -- but the decision is
	// RECORDED, because "we chose to ignore it" is not "there was nothing to
	// ignore".
	t.Run("unreadable Requires-Python", func(t *testing.T) {
		if !requirePkg(t, "admobilize-malos") {
			return
		}

		ver, err := version.Parse("0.0.2")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		meta, err := idx.Metadata(ctx, NewPackageName("admobilize-malos"), ver)
		if err != nil {
			t.Fatalf("Metadata: %v", err)
		}

		if !meta.RequiresPythonUnreadable {
			t.Errorf("RequiresPythonUnreadable = false for %q; an unreadable constraint and an "+
				"absent one would then be indistinguishable", meta.RequiresPythonRaw)
		}
		if meta.RequiresPythonRaw == "" {
			t.Error("RequiresPythonRaw is empty, so the string the publisher declared is lost")
		}
		target, err := version.Parse("3.11")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if !meta.SupportsPython(target) {
			t.Errorf("an unreadable Requires-Python (%q) excluded %s; the policy is to be "+
				"permissive, and inverting it silently makes every such version unusable",
				meta.RequiresPythonRaw, target)
		}
	})
}

// TestRealFileErrorTaxonomy pins the two sentinels against producer data, because
// the distinction is what a consumer branches on.
func TestRealFileErrorTaxonomy(t *testing.T) {
	idx, _, _ := realIndex(t)
	ctx := context.Background()

	t.Run("unknown package", func(t *testing.T) {
		ver, err := version.Parse("1.0")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		_, err = idx.Metadata(ctx, NewPackageName("no-such-package-anywhere-19466"), ver)
		if !errors.Is(err, ErrPackageNotFound) {
			t.Errorf("error = %v, want ErrPackageNotFound", err)
		}
	})

	// An unknown VERSION of a KNOWN package is ErrMetadataUnavailable, not
	// ErrPackageNotFound: the package was found, and reporting otherwise invites
	// a resolver to treat a present package as a typo.
	t.Run("unknown version of a known package", func(t *testing.T) {
		ver, err := version.Parse("99999.0")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		_, err = idx.Metadata(ctx, NewPackageName(fixtureRoot), ver)
		if !errors.Is(err, ErrMetadataUnavailable) {
			t.Errorf("error = %v, want ErrMetadataUnavailable", err)
		}
		if errors.Is(err, ErrPackageNotFound) {
			t.Errorf("error = %v also reports ErrPackageNotFound for a package that IS present", err)
		}
	})

	// An RSF carries no distribution files at all, and says so without inspecting
	// what it was asked about.
	t.Run("files are never served", func(t *testing.T) {
		ver, err := version.Parse("1.0")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		_, err = idx.Files(ctx, NewPackageName(fixtureRoot), ver)
		if !errors.Is(err, ErrFilesUnavailable) {
			t.Errorf("error = %v, want ErrFilesUnavailable", err)
		}
	})
}

// TestRealFileCarriesATrainedDictionary guards the fixture's own premise.
//
// Dependency blobs in a production file are zstd-compressed against a dictionary
// carried on the file's first record. An excerpt that lost it would still open
// and would still decode any STORED blob, so the zstd path could quietly stop
// being exercised while every other test here kept passing.
func TestRealFileCarriesATrainedDictionary(t *testing.T) {
	file, _ := realFile(t)

	names := file.Dict().Names()
	if len(names) == 0 {
		t.Fatal("the dependency dictionary has no names; this file cannot be exercising the " +
			"dictionary-compressed path")
	}
	t.Logf("dependency dictionary: %d names", len(names))
}
