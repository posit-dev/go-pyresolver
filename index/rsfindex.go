// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
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
func (idx *RSFIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

	out := make([]version.Version, 0, len(decoded))
	for raw := range decoded {
		v, parseErr := version.Parse(raw)
		if parseErr != nil {
			// A version key PEP 440 rejects is skipped rather than failing the
			// package. Real corpora carry a few non-conforming keys, and one of
			// them must not make every other version unreachable.
			continue
		}
		out = append(out, v)
	}

	return out, nil
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
		bestKey, found := "", false
		bestCanonical := false
		for key := range decoded {
			parsed, parseErr := version.Parse(key)
			if parseErr != nil || !parsed.Equal(ver) {
				continue
			}
			canonical := parsed.String() == key
			switch {
			case !found:
			case canonical && !bestCanonical:
			case canonical == bestCanonical && key < bestKey:
			default:
				continue
			}
			bestKey, bestCanonical, found = key, canonical, true
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
				return PackageMetadata{}, fmt.Errorf(
					"index %q: %q %s: parsing requirement %q: %w",
					idx.origin, pkg, ver, rawReq, reqErr)
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
