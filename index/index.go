// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"

	"github.com/posit-dev/go-python-packaging/version"
)

// Sentinel errors returned by MetadataIndex implementations. Callers must test
// with errors.Is, since implementations are expected to wrap these with
// context about which index and which package failed.
var (
	// ErrPackageNotFound means the index has no such package at all.
	//
	// Distinct from "the package exists but has no matching versions", which
	// is an empty slice and a nil error. A resolver reports the two very
	// differently: an unknown name is probably a typo, whereas a known name
	// with no acceptable version is a constraint conflict worth explaining.
	ErrPackageNotFound = errors.New("package not found")

	// ErrMetadataUnavailable means the package and version exist but their
	// dependency metadata cannot be produced without work this index will not
	// do -- in practice, an sdist-only release whose metadata requires
	// executing a build.
	//
	// This is deliberately not folded into ErrPackageNotFound. Treating an
	// unbuildable sdist as "not found" would let the resolver silently choose
	// an older version, which looks like a successful resolution and is not.
	ErrMetadataUnavailable = errors.New("metadata unavailable")
)

// MetadataIndex is the architectural seam between the resolver and storage.
//
// The resolver core makes no HTTP request and touches no database. It asks this
// interface what versions exist, what they require, and what files represent
// them; the implementation decides where those bytes come from. That is what
// lets one resolver serve connected PPM, air-gapped PPM, local Python sources,
// and tests, and it is the invariant most worth protecting in this module.
//
// # Implementations
//
// Per RFD 0001 Section 6: RSFIndex + CachedJSONIndex (connected PPM),
// OfflineIndex (air-gapped), DBIndex (local Python sources), MockIndex (tests),
// plus the composable FilteredIndex and MultiIndex wrappers.
//
// A note on where the bytes actually come from, because it is easy to assume
// wrongly: dependency metadata is resident in the PyPI RSF and is read
// in-process with no network call. Only Files() is CDN-backed. RFD Rev 15
// reversed the carrier for the dependency fieldset precisely so that
// resolution works air-gapped, so an implementation that fetches dependencies
// over the network is not just slower, it breaks the offline case.
//
// # Contract
//
// Implementations must be safe for concurrent use by multiple goroutines. A
// resolver is free to look ahead, and every wrapper here composes by holding a
// reference to another index rather than by copying it.
//
// All three methods must honor ctx cancellation.
type MetadataIndex interface {
	// Versions returns every version of pkg the index knows about, in NO
	// guaranteed order. Callers that need an order must sort.
	//
	// NO TWO RETURNED VERSIONS MAY COMPARE EQUAL. PEP 440 equality is coarser
	// than string equality, so a source carrying both "1.0" and "1.0.0" holds one
	// version under two spellings, and an implementation must return a single
	// representative rather than both. Returning both is not merely redundant: a
	// resolver cannot select between candidates that compare equal, so the choice
	// silently falls to iteration order, and the two underlying records can
	// disagree about dependencies.
	//
	// An implementation that collapses a class must make Metadata resolve the
	// representative to the same record it treated as authoritative, or a caller
	// can hold a version whose dependencies came from the spelling that lost.
	//
	// Returns ErrPackageNotFound if pkg is unknown. A known package with no
	// versions is an empty slice and a nil error.
	Versions(ctx context.Context, pkg PackageName) ([]version.Version, error)

	// Metadata returns the dependency information for one (pkg, ver).
	//
	// Returns ErrPackageNotFound if pkg or ver is unknown, and
	// ErrMetadataUnavailable for a release whose metadata would require a
	// build (sdist-only).
	Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error)

	// Files returns the distribution files -- wheels and sdists -- for one
	// (pkg, ver).
	//
	// Per the settled decision in RFD 0001 Section 16, this returns ALL
	// wheels without platform filtering; deciding which are compatible is the
	// consumer's job. That keeps the interface free of any notion of a target
	// platform, which is what makes multi-environment resolution (resolving
	// for a platform other than the one you are running on) expressible at
	// all.
	//
	// Returns ErrPackageNotFound if pkg or ver is unknown. A known version
	// with no files is an empty slice and a nil error -- which does happen,
	// since a release can have every file deleted.
	Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error)
}
