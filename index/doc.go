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
// The interface, its types, and MockIndex are implemented. RSFIndex and
// CachedJSONIndex follow in rstudio/package-manager#18647.
//
// # Implementation status
//
// Per RFD 0001 Section 6:
//
//   - RSFIndex + CachedJSONIndex: connected PPM. RSFIndex reads both the
//     version list and the per-version dependencies from the resident PyPI
//     RSF with no CDN call; only file lists go to the CDN, via
//     CachedJSONIndex with Ristretto caching. Note the direction here — RFD
//     Rev 15 reversed the carrier for the dependency fieldset, so
//     requires_dist, requires_python, and provides_extra live IN the RSF.
//     Only Files() is CDN-backed.
//   - OfflineIndex: air-gapped PPM. Dependencies come from the same resident
//     RSF, so Metadata needs no network; file lists come from local files
//     pre-warmed by the offline downloader.
//   - DBIndex: local Python sources, backed by the pypi_projects table.
//   - MockIndex: in-memory, for tests. IMPLEMENTED.
//   - FilteredIndex: composable wrapper applying snapshot-date, prerelease,
//     and yanked policy.
//   - MultiIndex: combines ordered sources.
//
// Only RSFIndex, CachedJSONIndex, and MockIndex are in scope for the initial
// release.
package index
