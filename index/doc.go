// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package index defines MetadataIndex, the architectural seam between the
// resolver and storage, and its implementations.
//
// MetadataIndex is the single interface the resolver uses to learn what
// versions exist, what they depend on, and what files they ship. The resolver
// core never makes an HTTP request and never touches the database; it calls
// this interface, and the implementation decides where the bytes come from.
// That separation is what makes the same resolver usable for connected PPM,
// air-gapped PPM, local Python sources, and tests.
//
// # Implementation status
//
//   - MetadataIndex, its types, and MockIndex — implemented.
//   - RSFIndex — implemented. Serves Versions and Metadata from a local RSF
//     file via the pypirsf package. This is the standalone path: one file on
//     disk, no network, no database, and therefore reproducible — the file is a
//     dated artifact, so the same file resolves the same way forever.
//   - FilteredIndex (pre-release, yanked, and snapshot-date policy) and
//     MultiIndex (ordered sources) — implemented. Both are generic wrappers, so
//     they belong here rather than in any one consumer. See "A file-level policy
//     needs a file-serving index" below for the constraint that shapes how they
//     compose.
//   - Package Manager implements its own index against this interface, over its
//     resident RSF and its own caching. That is a different access path to the
//     same data rather than a duplicate of RSFIndex, and it stays in PPM.
//     Tracked as rstudio/package-manager#19437. DBIndex, backed by PPM's
//     pypi_projects table, belongs there for the same reason: a public module
//     cannot reach PPM's database.
//
// # Where dependency metadata comes from
//
// The RSF, read in-process, with no per-package network request. RFD 0001
// Rev 15 reversed the carrier for the dependency fieldset so that
// requires_dist, requires_python, and provides_extra are resident in the file.
// That reversal is what makes offline and reproducible resolution possible, so
// an implementation that reaches the network for dependency metadata is not
// merely slower — it breaks the case the design exists to serve.
//
// # Files is not available from every index
//
// An RSF carries dependency metadata only: no filename, hash, upload time, or
// yanked flag appears anywhere in the record. RSFIndex therefore reports
// ErrFilesUnavailable from Files.
//
// That is a distinct sentinel from ErrMetadataUnavailable because the two call
// for different handling. ErrFilesUnavailable means "this source cannot answer,
// ask another"; ErrMetadataUnavailable means "this version is unusable, choose
// another". Returning an empty slice instead would assert something false —
// that the version ships no files.
//
// # A file-level policy needs a file-serving index
//
// Of FilterPolicy's three axes, only pre-release exclusion is decidable from a
// version alone. Yanking is per-file per PEP 592 and an upload time belongs to a
// file, so those two are evaluated through Files — which means a file-level
// policy is not expressible over an index that serves none.
//
// FilteredIndex reports ErrFilesUnavailable there rather than resolving it
// either way. Admitting everything would defeat the policy invisibly, and
// dropping everything would report every package in the index as having no
// acceptable version — a constraint conflict that does not exist. The
// composition that works is a FilteredIndex over a MultiIndex pairing an RSF
// with a file-serving source, which is also the arrangement RFD 0001 Section 6
// describes.
package index
