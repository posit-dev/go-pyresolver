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
//   - FilteredIndex (prerelease and yanked policy) and MultiIndex (ordered
//     sources) are generic and belong here. Not yet built; tracked as
//     rstudio/package-manager#18648.
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
package index
