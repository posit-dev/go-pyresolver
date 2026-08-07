// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/posit-dev/go-python-packaging/extras"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// ErrFilesUnavailable means this index cannot report distribution files at all,
// as opposed to a particular version having none.
//
// A distinct sentinel rather than reusing ErrMetadataUnavailable, because the
// two call for different handling: ErrMetadataUnavailable says "this version
// needs a build, choose another", while this says "ask a different source". A
// composing index needs to tell those apart to know whether falling back is
// even sensible.
var ErrFilesUnavailable = errors.New("distribution files unavailable from this index")

// RSFIndex is a MetadataIndex backed by a local Repository Snapshot Format file.
//
// This is the standalone path: one file on disk, no network, no database. It is
// what makes a resolution reproducible — the file is a dated artifact, so the
// same file resolves the same way forever.
//
// # Files is not served
//
// An RSF carries dependency metadata, not distribution files: there is no
// filename, hash, upload time, or yanked flag anywhere in the record. Files
// therefore always returns ErrFilesUnavailable. That is a property of the data,
// not a gap in this code, and it is why a tool built on this emits resolved
// version pins rather than download URLs. Pair it with a file-aware index, or
// hand the pins to something that knows how to fetch.
//
// # Versions reported
//
// The versions this reports are those with CAPTURED dependency metadata, not
// every version that ever existed. For resolution that is the right set: a
// version whose requirements are unknown cannot be resolved through, so
// offering it as a candidate could only produce a silently incomplete answer.
type RSFIndex struct {
	file   *pypirsf.File
	origin string

	// mu guards decoded. Lookups are otherwise concurrent, and pypirsf.File is
	// itself safe for concurrent use.
	mu      sync.RWMutex
	decoded map[PackageName]map[string]pypirsf.VersionDeps
}

// NewRSFIndex wraps an open pypirsf.File.
//
// The caller retains ownership of file and is responsible for closing it; an
// index does not own the file it was handed, since one file can back several.
//
// origin labels which index answered, surfaced as PackageMetadata.Origin. Empty
// means "rsf".
func NewRSFIndex(file *pypirsf.File, origin string) (*RSFIndex, error) {
	if file == nil {
		return nil, errors.New("index: NewRSFIndex requires a non-nil file")
	}
	if origin == "" {
		origin = "rsf"
	}

	return &RSFIndex{
		file:    file,
		origin:  origin,
		decoded: make(map[PackageName]map[string]pypirsf.VersionDeps),
	}, nil
}

// deps returns the decoded dependency map for pkg, caching it.
//
// Caching matters more than it looks: a resolver calls Versions once and then
// Metadata per candidate version, and without this every one of those calls
// would re-read and re-decompress the same package blob.
//
// The cache is unbounded, which is deliberate for this shape of consumer. A
// resolution touches the packages in its closure, so the cache is bounded by the
// work actually requested rather than by the corpus. A long-lived server process
// resolving arbitrary requests would want a bound; that is not this.
//
// The returned map is shared with the cache and MUST NOT be mutated or handed
// to a caller. The exported methods copy what they return out of it.
func (idx *RSFIndex) deps(pkg PackageName) (map[string]pypirsf.VersionDeps, error) {
	idx.mu.RLock()
	cached, ok := idx.decoded[pkg]
	idx.mu.RUnlock()
	if ok {
		return cached, nil
	}

	decoded, err := idx.file.Deps(pkg.String())
	if err != nil {
		if errors.Is(err, pypirsf.ErrPackageNotFound) {
			return nil, fmt.Errorf("index %q: %q: %w", idx.origin, pkg, ErrPackageNotFound)
		}
		return nil, fmt.Errorf("index %q: %q: %w", idx.origin, pkg, err)
	}

	idx.mu.Lock()
	// Another goroutine may have decoded this in the meantime; keep whichever
	// landed first so all callers share one map and the copy-on-return
	// contract holds for a single object.
	if existing, raced := idx.decoded[pkg]; raced {
		decoded = existing
	} else {
		idx.decoded[pkg] = decoded
	}
	idx.mu.Unlock()

	return decoded, nil
}

// Versions implements MetadataIndex.
//
// # PEP 440-equal keys are collapsed to one version
//
// The producer records whatever version string a publisher used, so one package
// can carry both "1.0" and "1.0.0" as separate stored keys. Those are the SAME
// version under PEP 440, and returning both hands a resolver two candidates it
// cannot tell apart: they compare equal, so no constraint can select between
// them, and the choice falls to whatever order the caller happens to iterate.
//
// Worse, the two stored records can disagree about dependencies — measured on a
// production snapshot as 59 equality classes across 56 packages, 10 of which
// disagree. A resolver offered both would produce a different dependency graph
// depending on which it picked, with nothing in the data to justify either.
//
// So one representative is returned per equality class, chosen by preferKey.
// Metadata uses that same function, which is what makes the pair coherent: the
// version handed out here resolves to the record dedup treated as authoritative.
// It does NOT make the underlying data unambiguous; which spelling the publisher
// meant is unknowable from the snapshot, and a caller still cannot detect that a
// class was collapsed.
func (idx *RSFIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

	type candidate struct {
		key       string
		parsed    version.Version
		canonical bool
	}

	candidates := make([]candidate, 0, len(decoded))
	for raw := range decoded {
		v, parseErr := version.Parse(raw)
		if parseErr != nil {
			// A version key PEP 440 rejects is skipped rather than failing the
			// package. Real corpora carry a few non-conforming keys, and one of
			// them must not make every other version unreachable.
			//
			// ⚠️ Skipping is silent HERE by design, but it must not be silent to a
			// human. When EVERY key of a package is rejected this returns an empty
			// slice, which is indistinguishable from a package for which nothing
			// was captured at all — and those are different facts. See
			// UnparseableVersionKeys, which exists so a diagnostic caller can tell
			// them apart.
			continue
		}
		candidates = append(candidates, candidate{key: raw, parsed: v, canonical: v.String() == raw})
	}

	// Sorting is not about the returned order, which the interface does not
	// promise — it is what puts the members of a PEP 440 equality class next to
	// each other so one representative can be chosen per class.
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].parsed.Equal(candidates[j].parsed) {
			return candidates[i].parsed.LessThan(candidates[j].parsed)
		}
		// Within a class, order by the same rule that picks the representative, so
		// the winner is simply the first member.
		return preferKey(candidates[i].key, candidates[i].canonical,
			candidates[j].key, candidates[j].canonical)
	})

	out := make([]version.Version, 0, len(candidates))
	for i, c := range candidates {
		if i > 0 && candidates[i-1].parsed.Equal(c.parsed) {
			// A later member of a class already represented. See the dedup note in
			// the method doc.
			continue
		}
		out = append(out, c.parsed)
	}

	return out, nil
}

// preferKey reports whether key a is the better representative of a PEP 440
// equality class than key b.
//
// Canonical spellings win, then the lexicographically smallest. Shared by
// Versions and Metadata deliberately: Versions decides which spelling a caller
// ever sees, and Metadata decides which stored record that spelling resolves to.
// If those two used separate implementations of "the same rule" they would agree
// only by coincidence, and a resolver would be able to hold a version that
// resolves to a different package's dependency set than the one dedup considered
// authoritative.
//
// canonical means the key round-trips through PEP 440 normalization, which is the
// best available evidence of what the publisher actually wrote. The lexicographic
// tail exists only to make the outcome total, since two non-canonical spellings
// can both compare equal.
// UnparseableVersionKeys returns the stored version keys for pkg that PEP 440
// rejects, sorted. It is empty when every key parses.
//
// # Why this exists
//
// Versions skips a key it cannot parse, which is the right behaviour for a
// resolver: a few non-conforming keys are normal in a real corpus and one of them
// must not make every other version of that package unreachable.
//
// ⚠️ But when EVERY key of a package is rejected, Versions returns an empty slice,
// and that is indistinguishable from a package for which nothing was captured at
// all. Those are different facts and they call for different responses. Reporting
// the second when the first is true sends someone looking for missing data that is
// actually present, just recorded under a string the specification does not
// accept — the snapshot holds `holygrail` with one key, "0.2.1.Perceval", carrying
// a real dependency on sqlobject.
//
// Deliberately NOT on the MetadataIndex interface. A resolver has no use for it;
// it exists for diagnostics, and widening the resolver seam for a reporting
// concern would oblige every implementation to answer a question none of them are
// asked.
func (idx *RSFIndex) UnparseableVersionKeys(ctx context.Context, pkg PackageName) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

	var bad []string
	for raw := range decoded {
		if _, parseErr := version.Parse(raw); parseErr != nil {
			bad = append(bad, raw)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

func preferKey(a string, aCanonical bool, b string, bCanonical bool) bool {
	if aCanonical != bCanonical {
		return aCanonical
	}
	return a < b
}

// Metadata implements MetadataIndex.
func (idx *RSFIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return PackageMetadata{}, err
	}

	raw, ok := decoded[ver.String()]
	if !ok {
		// The producer writes whatever version string the publisher used, so
		// "1.0" and "1.0.0" can both appear and neither is wrong. Fall back to
		// PEP 440 equality before giving up.
		//
		// ⚠️ MORE THAN ONE KEY CAN QUALIFY, so the choice must not be made by map
		// iteration order. Go randomizes it, and a package carrying two
		// equal-comparing spellings with different dependencies then answers
		// differently from one call to the next on the same index — measured on a
		// production snapshot as 500 calls returning two distinct results. That is
		// a wrong answer delivered with total confidence, and it falsifies this
		// type's documented guarantee that the same file resolves the same way
		// forever.
		//
		// The rule: among keys that compare equal, prefer the one whose spelling
		// is already canonical, then the lexicographically smallest. The first
		// clause is the principled half — a key that round-trips through
		// normalization is the best available evidence of what the publisher
		// meant. The second exists only to make the outcome total, since two
		// non-canonical spellings can both compare equal.
		//
		// Neither clause makes the underlying data unambiguous: which spelling is
		// authoritative is unknowable from the snapshot, and the caller cannot
		// currently detect that it happened. Surfacing the ambiguity needs an API
		// this interface does not have yet; determinism is the part that can be
		// fixed here.
		//
		// The rule itself lives in preferKey, shared with Versions, so the version
		// Versions hands out and the record Metadata resolves for it cannot drift
		// apart. Two separate implementations of "the same rule" would agree only
		// by coincidence.
		bestKey, found := "", false
		bestCanonical := false
		for key := range decoded {
			parsed, parseErr := version.Parse(key)
			if parseErr != nil || !parsed.Equal(ver) {
				continue
			}
			canonical := parsed.String() == key
			if !found || preferKey(key, canonical, bestKey, bestCanonical) {
				bestKey, bestCanonical, found = key, canonical, true
			}
		}
		if found {
			raw = decoded[bestKey]
		} else {
			// The package exists but this version has no captured metadata.
			// Unavailable rather than not-found: reporting not-found would
			// invite a resolver to treat it as a typo and give up on a package
			// that is genuinely present.
			return PackageMetadata{}, fmt.Errorf("index %q: %q %s: %w",
				idx.origin, pkg, ver, ErrMetadataUnavailable)
		}
	}

	meta := PackageMetadata{
		Name:    pkg,
		Version: ver,
		Origin:  idx.origin,
	}

	if len(raw.RequiresDist) > 0 {
		meta.RequiresDist = make([]requirement.Requirement, 0, len(raw.RequiresDist))
		for _, rawReq := range raw.RequiresDist {
			req, reqErr := requirement.Parse(rawReq)
			if reqErr != nil {
				// A requirement this module cannot parse is a hard error, not a
				// skip. Dropping it silently would hand the resolver an
				// incomplete dependency set and produce a confident wrong
				// answer -- the one failure mode worth failing loudly for.
				//
				// Wrapped in ErrMetadataUnusable so a caller can CLASSIFY the
				// refusal rather than only observe that something failed. The
				// policy is unchanged: this version is still refused. What changes
				// is that a caller can now tell "this one version is unusable"
				// apart from "the index is broken", and respond in proportion --
				// a resolver by trying another version, a diagnostic traversal by
				// reporting the package and continuing. Returning an opaque error
				// forced every caller to choose between aborting and swallowing
				// everything, and the CLI chose to abort, discarding an entire
				// walk over one bad entry.
				//
				// The original parse error stays in the chain, so the specific
				// malformed string is still recoverable for diagnostics.
				return PackageMetadata{}, fmt.Errorf(
					"index %q: %q %s: parsing requirement %q: %w: %w",
					idx.origin, pkg, ver, rawReq, ErrMetadataUnusable, reqErr)
			}
			meta.RequiresDist = append(meta.RequiresDist, req)
		}
	}

	if raw.RequiresPython != "" {
		specs, specErr := version.NewSpecifiers(raw.RequiresPython)
		if specErr != nil {
			// Left unconstrained rather than fatal, unlike RequiresDist. An
			// unreadable interpreter constraint over-admits a candidate, which
			// surfaces later as an install-time failure; an unreadable
			// requirement would silently under-constrain the graph and change
			// the resolution itself. pip draws the line the same way: it catches
			// InvalidSpecifier on Requires-Python and treats the candidate as
			// compatible.
			//
			// ⚠️ The empty set only MEANS unconstrained if callers ask through
			// PackageMetadata.SupportsPython. Specifiers.Check answers false for
			// every version when it holds no groups, so a caller reaching for
			// Check directly gets the exact inverse of this policy. See
			// SupportsPython.
			meta.RequiresPython = version.Specifiers{}
		} else {
			meta.RequiresPython = specs
		}
	}

	if len(raw.ProvidesExtra) > 0 {
		meta.ProvidesExtra = make([]string, 0, len(raw.ProvidesExtra))
		for _, extra := range raw.ProvidesExtra {
			// Normalized per PEP 685 so a request for pkg[Test-Suite] matches a
			// declared "test_suite".
			meta.ProvidesExtra = append(meta.ProvidesExtra, extras.Normalize(extra))
		}
	}

	return meta, nil
}

// Files implements MetadataIndex by always reporting ErrFilesUnavailable.
//
// See the type documentation: an RSF carries no filename, hash, upload time, or
// yanked flag, so there is nothing to report. This is the data's shape, not a
// missing feature.
func (idx *RSFIndex) Files(_ context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	return nil, fmt.Errorf("index %q: %q %s: an RSF carries dependency metadata only: %w",
		idx.origin, pkg, ver, ErrFilesUnavailable)
}

// Len reports how many packages the underlying file carries.
func (idx *RSFIndex) Len() int { return idx.file.Len() }

// Packages returns every canonical name in the underlying file, sorted.
func (idx *RSFIndex) Packages() []string { return idx.file.Packages() }

// Compile-time assertion that RSFIndex satisfies the interface.
var _ MetadataIndex = (*RSFIndex)(nil)
