// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/posit-dev/go-python-packaging/version"
)

// MultiIndex serves one MetadataIndex out of several ordered sources.
//
// # Versions unions, Metadata and Files do not
//
// The asymmetry is deliberate and is the whole design. Which versions EXIST is
// naturally a union: that is what makes "a local source plus upstream" work,
// each contributing what it has. But the metadata or the files for one specific
// version must come from ONE source, because two sources can disagree about a
// release and merging their answers would produce a record no publisher ever
// made. So those two take the first source that can answer, in order, and
// PackageMetadata.Origin names which one did.
//
// # Error taxonomy across sources
//
// This is where composition earns its bugs, so the rules are explicit. A
// source's ErrPackageNotFound is that source's answer about itself, not a fact
// about the composed index.
//
//	Versions   ErrPackageNotFound only when NO source knows the name. One
//	           source knowing it and carrying no versions is an empty slice
//	           and a nil error.
//
//	Metadata   first success wins. Otherwise, in order of how much was
//	           learned: a malformed record somewhere (ErrMetadataUnusable)
//	           outranks no record anywhere (ErrMetadataUnavailable), which
//	           outranks nobody having heard of the package
//	           (ErrPackageNotFound).
//
//	Files      first success wins, INCLUDING an empty list. Otherwise
//	           ErrMetadataUnavailable if any source knows the package and
//	           cannot serve this version's files, OR if a file-serving source
//	           denied the name while a fileless source was also present;
//	           ErrPackageNotFound when every source that can speak to files
//	           denied the name and none was fileless; ErrFilesUnavailable only
//	           when EVERY source is fileless.
//
// ⚠️ The case worth stating plainly, because getting it wrong is a reported
// defect waiting to happen: source A knows the package but not the version and
// source B knows neither. The answer is ErrMetadataUnavailable, NOT
// ErrPackageNotFound. The package was found. A caller branching on not-found
// there reports a missing package for a present one, which is the defect
// rstudio/package-manager#19466's F12 chased through three implementations.
//
// ⚠️ Note the asymmetry in Files: ErrFilesUnavailable is the WEAKEST evidence,
// not the strongest, because a fileless source emits it for every lookup without
// inspecting pkg or ver. Letting it win made ErrPackageNotFound unreachable
// whenever an RSF was in the composition and made the sentinel useless to a
// FilteredIndex above. See Files for the full account.
//
// # Which sentinels each method tolerates from a source
//
// A source may itself be a FilteredIndex, so a method can receive a sentinel the
// interface does not list for it. Rather than aborting the whole lookup over one
// source's choice of error, each method tolerates and skips what it can:
//
//	Versions   ErrPackageNotFound and ErrFilesUnavailable are skipped. Neither
//	           marks the package as known.
//	Metadata   ErrPackageNotFound skipped; ErrMetadataUnavailable and
//	           ErrFilesUnavailable both count as "this source supplied no
//	           metadata"; ErrMetadataUnusable is remembered and reported only
//	           if no source can answer.
//	Files      all four sentinels are tolerated per the precedence above.
//
// # A real failure is never masked
//
// Any error that is none of the four sentinels -- an I/O failure, a cancelled
// context, a caller bug such as an uninitialized version -- is returned
// immediately rather than skipped in favour of a later source's success. A union
// silently missing one source's contribution is a confident wrong answer, and a
// cancelled context that looked like a successful narrow resolution would be
// worse.
//
// # Cross-source spelling is not bridged
//
// ⚠️ PEP 440 equality is coarser than string equality, so two sources can hold
// one version under two spellings ("1.0" and "1.0.0"). Versions collapses the
// class to the EARLIEST source's spelling, and Metadata consults sources in that
// same order, so the representative resolves to the record this type treated as
// authoritative. But if the earliest source can list the version and not supply
// its metadata, a later source holding the same version under a different
// spelling will NOT be found: it is asked for the earliest source's spelling and
// its own lookup is by string. Bridging that would cost a Versions call per
// Metadata miss on every source, to rescue a case a real corpus produces rarely,
// so it is documented rather than paid for.
//
// ⚠️ THE CONSEQUENCE IS LARGER THAN A METADATA MISS, and this paragraph used to
// undersell it. Under a FilteredIndex carrying a file-level policy, the file
// lookup goes through the same string-keyed path, so a version whose file
// evidence lives in another source under another spelling is not merely awkward
// to fetch -- it is DROPPED FROM THE VERSION LIST. A package can therefore appear
// to have no usable versions at all. "One version resolves oddly" and "this
// package looks empty" are very different things to debug, and only the second
// is what actually happens.
//
// A MultiIndex is immutable after construction and therefore safe for concurrent
// use whenever its sources are.
type MultiIndex struct {
	sources []MetadataIndex
}

// NewMultiIndex returns a MultiIndex over sources, consulted in the given order.
//
// It panics on a nil source. That is a programming error with no data behind it,
// and panicking at construction points at the line that built the index rather
// than at whichever lookup first dereferenced it.
//
// A MultiIndex over no sources is legal and knows nothing: every lookup reports
// ErrPackageNotFound, which is the honest answer for an index with nothing in
// it, not an error state.
func NewMultiIndex(sources ...MetadataIndex) *MultiIndex {
	for i, src := range sources {
		if src == nil {
			panic(fmt.Sprintf("index.NewMultiIndex: source %d is nil", i))
		}
	}
	// Copy so a caller mutating its own slice afterwards cannot change what this
	// index consults, which would break the concurrency guarantee.
	return &MultiIndex{sources: append([]MetadataIndex(nil), sources...)}
}

// Versions implements MetadataIndex with the union of every source's versions.
//
// ⚠️ NO TWO RETURNED VERSIONS MAY COMPARE EQUAL, and a union across sources is
// the most likely way to violate that: PEP 440 equality is coarser than string
// equality, so two sources spelling one version "1.0" and "1.0.0" hold one
// version twice. Returning both is not merely redundant -- a resolver cannot
// select between candidates that compare equal, so the choice silently falls to
// iteration order, and the two records can disagree about dependencies.
//
// The class collapses to the EARLIEST source's spelling, matching the order
// Metadata and Files consult, which is what makes the representative resolve to
// the record this type treated as authoritative. The mechanism mirrors
// RSFIndex.Versions deliberately: sort so members of a class land adjacent, then
// keep the first. Two implementations of "the same rule" would agree only by
// coincidence.
//
// Deduping here also masks a source that violated the contract by returning two
// equal versions itself. That is accepted: this method's own contract is
// absolute, and it cannot both honour it and surface someone else's breach.
func (m *MultiIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type candidate struct {
		parsed version.Version
		source int
	}

	var candidates []candidate
	known := false

	for i, src := range m.sources {
		versions, err := src.Versions(ctx, pkg)
		switch {
		case err == nil:
			known = true
		case errors.Is(err, ErrPackageNotFound):
			// This source's answer about itself, not about the union.
			continue
		case errors.Is(err, ErrFilesUnavailable):
			// A source that refuses at the files layer contributes nothing here
			// and must not abort the union. Reachable through a per-source
			// FilteredIndex carrying a file-level policy, which is a natural
			// shape. Deliberately does NOT set known: this says nothing about
			// whether the package exists.
			continue
		default:
			return nil, fmt.Errorf("multi index: source %d of %d: %w", i+1, len(m.sources), err)
		}

		for _, ver := range versions {
			candidates = append(candidates, candidate{parsed: ver, source: i})
		}
	}

	if !known {
		return nil, fmt.Errorf("multi index (%d sources): %q: %w",
			len(m.sources), pkg, ErrPackageNotFound)
	}

	// Sorting is not about the returned order, which the interface does not
	// promise -- it is what puts members of a PEP 440 equality class next to each
	// other so one representative can be chosen per class. Within a class the
	// earliest source wins; the string tail only makes the order total when one
	// source contributed two equal spellings.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.parsed.Equal(b.parsed) {
			return a.parsed.LessThan(b.parsed)
		}
		if a.source != b.source {
			return a.source < b.source
		}
		return a.parsed.String() < b.parsed.String()
	})

	out := make([]version.Version, 0, len(candidates))
	for i, c := range candidates {
		if i > 0 && candidates[i-1].parsed.Equal(c.parsed) {
			// A later member of a class already represented.
			continue
		}
		out = append(out, c.parsed)
	}
	return out, nil
}

// Metadata implements MetadataIndex, returning the first source's answer.
//
// See MultiIndex for the error precedence and why ErrPackageNotFound requires
// that NO source knew the package.
func (m *MultiIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	// The first malformed record seen, kept for its message: the offending
	// string is the only thing that makes such a failure actionable.
	var unusable error
	sawUnavailable := false

	for i, src := range m.sources {
		meta, err := src.Metadata(ctx, pkg, ver)
		switch {
		case err == nil:
			// Origin is left exactly as the source set it. It is the only way to
			// tell which source answered.
			return meta, nil
		case errors.Is(err, ErrPackageNotFound):
			continue
		case errors.Is(err, ErrMetadataUnavailable), errors.Is(err, ErrFilesUnavailable):
			// ErrFilesUnavailable is out of contract for Metadata, but a source
			// that is itself a FilteredIndex under a file-level policy can emit
			// it, and the operative meaning is the same: this source did not
			// supply metadata for this version. Tolerated rather than aborting
			// the whole lookup over one source's choice of sentinel.
			sawUnavailable = true
		case errors.Is(err, ErrMetadataUnusable):
			// A later source may still hold a usable record for this version, so
			// keep looking -- "ordered sources" means the first that CAN answer.
			if unusable == nil {
				unusable = err
			}
		default:
			return PackageMetadata{}, fmt.Errorf("multi index: source %d of %d: %w", i+1, len(m.sources), err)
		}
	}

	switch {
	case unusable != nil:
		// A record that EXISTS and is malformed is the more specific truth, and
		// folding it into "unavailable" would lose the fact that makes it
		// actionable.
		return PackageMetadata{}, fmt.Errorf("multi index (%d sources): %w", len(m.sources), unusable)
	case sawUnavailable:
		return PackageMetadata{}, fmt.Errorf("multi index (%d sources): %q %s: %w",
			len(m.sources), pkg, ver, ErrMetadataUnavailable)
	default:
		return PackageMetadata{}, fmt.Errorf("multi index (%d sources): %q: %w",
			len(m.sources), pkg, ErrPackageNotFound)
	}
}

// Files implements MetadataIndex, returning the first source's answer.
//
// ⚠️ An EMPTY list with a nil error is an ANSWER, not a miss. The interface says
// so explicitly, because a release can have every file deleted. Treating empty
// as "keep looking" would let a stale mirror resurrect files the authoritative
// source has removed, which is the kind of wrong answer that looks like a
// success.
//
// ErrFilesUnavailable, by contrast, means "this source cannot answer, ask
// another" -- the one sentinel about a source's capability rather than about a
// package's data. Pairing an RSF, which serves no files at all, with a
// file-serving source is the composition RFD 0001 calls for, and it works
// because of this.
//
// # ErrFilesUnavailable is only returned when NO source serves files
//
// ⚠️ This is the load-bearing part, and it did not hold at first. A fileless
// source sets its flag for EVERY lookup, without inspecting pkg or ver --
// RSFIndex is documented as doing exactly that -- so letting that flag win
// meant ErrFilesUnavailable came back whenever an RSF was in the composition,
// even though a file-serving source had been asked and had answered. Two things
// broke:
//
//   - ErrPackageNotFound became unreachable, so a caller could not tell a
//     typo'd package name from "nobody serves files".
//   - A FilteredIndex above could no longer read the sentinel as a statement
//     about the whole inner index, because it no longer was one. A legitimate
//     partial mirror hard-errored with advice to compose the very thing it had
//     composed.
//
// So a fileless source's answer is now the WEAKEST evidence, not the strongest:
// it is consulted only when no other source said anything.
//
// # What each residual answer claims
//
// A fileless source's ErrFilesUnavailable says nothing about whether the package
// exists, so the residual cases are ordered by which claim is actually
// supportable:
//
//	ErrMetadataUnavailable   some source knows the package and cannot serve
//	                         files for this version.
//	ErrPackageNotFound       every source that CAN speak to files denied the
//	                         name, and no fileless source was involved -- so
//	                         absence is a claim this index can actually make.
//	ErrFilesUnavailable      every source is fileless. A true capability
//	                         statement, and the only case a caller should read
//	                         as "compose differently".
//
// ⚠️ The MIXED case -- a file-serving source denied the name while a fileless
// source was also present -- reports ErrMetadataUnavailable, not
// ErrPackageNotFound. Absence cannot be confirmed there: the fileless source may
// well hold the package and simply cannot say so through Files. Claiming
// not-found would send someone hunting a typo for a package that exists, which
// is the F12 failure in a new costume. ErrMetadataUnavailable's operative
// meaning -- "this index cannot give you files for this version" -- is true in
// every one of these cases, which is why it is the fallback.
func (m *MultiIndex) Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sawUnavailable := false
	sawNotFound := false
	sawFilesUnavailable := false
	var unusable error

	for i, src := range m.sources {
		files, err := src.Files(ctx, pkg, ver)
		switch {
		case err == nil:
			return files, nil
		case errors.Is(err, ErrPackageNotFound):
			// This source CAN speak to files and says the name is absent.
			sawNotFound = true
		case errors.Is(err, ErrMetadataUnavailable):
			sawUnavailable = true
		case errors.Is(err, ErrMetadataUnusable):
			// A file record that exists and is malformed. Tolerated here rather
			// than aborting, since a later source may serve the same version
			// cleanly; see the type doc on which sentinels each method accepts.
			if unusable == nil {
				unusable = err
			}
		case errors.Is(err, ErrFilesUnavailable):
			sawFilesUnavailable = true
		default:
			return nil, fmt.Errorf("multi index: source %d of %d: %w", i+1, len(m.sources), err)
		}
	}

	switch {
	case unusable != nil:
		return nil, fmt.Errorf("multi index (%d sources): %w", len(m.sources), unusable)
	case sawUnavailable:
		return nil, fmt.Errorf("multi index (%d sources): %q %s: %w",
			len(m.sources), pkg, ver, ErrMetadataUnavailable)
	case sawNotFound && sawFilesUnavailable:
		// Mixed. A file-serving source denied the name, but a fileless source
		// was present and cannot speak to existence either way, so absence is
		// not a claim this index may make. See the method doc.
		return nil, fmt.Errorf(
			"multi index (%d sources): %q %s: no source that serves files has it, "+
				"and a fileless source cannot confirm whether the package exists: %w",
			len(m.sources), pkg, ver, ErrMetadataUnavailable)
	case sawNotFound:
		return nil, fmt.Errorf("multi index (%d sources): %q: %w",
			len(m.sources), pkg, ErrPackageNotFound)
	case sawFilesUnavailable:
		return nil, fmt.Errorf("multi index (%d sources): %q %s: no source serves distribution files: %w",
			len(m.sources), pkg, ver, ErrFilesUnavailable)
	default:
		// No sources at all.
		return nil, fmt.Errorf("multi index (%d sources): %q: %w",
			len(m.sources), pkg, ErrPackageNotFound)
	}
}

// Compile-time assertion that MultiIndex satisfies the interface.
var _ MetadataIndex = (*MultiIndex)(nil)
