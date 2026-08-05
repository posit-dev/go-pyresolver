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

	// ErrMetadataUnusable means the metadata for a package version EXISTS but
	// cannot be used, because something in it does not conform to the
	// specification the resolver relies on -- in practice a Requires-Dist entry
	// PEP 508 rejects.
	//
	// # Why this is a separate sentinel
	//
	// Refusing such a version is deliberate and must stay that way: silently
	// dropping a requirement the module cannot parse would hand the resolver an
	// incomplete dependency set and produce a confident wrong answer. But a
	// caller has to be able to tell that refusal apart from an I/O failure or a
	// programming error, and before this sentinel existed it could not -- the
	// failure arrived as an opaque error, so every caller had to either abort or
	// swallow everything.
	//
	// The distinction from ErrMetadataUnavailable is the presence of a record.
	// There, nothing was captured and no amount of care would help. Here the data
	// is present and specific, so the right response depends on the caller: a
	// resolver should treat the version as ineligible and try another, while a
	// diagnostic traversal should report the package and carry on rather than
	// discard everything it has already learned.
	ErrMetadataUnusable = errors.New("metadata unusable")
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
