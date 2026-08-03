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
// Per RFD 0001 Section 6:
//
//   - MetadataIndex, its types, MockIndex — IMPLEMENTED here.
//   - CachedJSONIndex — IMPLEMENTED here. Serves Files (and Versions) from the
//     per-snapshot index JSON, with a bounded cache keyed by
//     (package, snapshot).
//   - RSFIndex — implemented in PPM, not here. It needs PPM's deps-blob
//     decoder and store types, which live in a private repo that imports this
//     module; putting it here would invert the dependency. Tracked as
//     rstudio/package-manager#19437.
//   - OfflineIndex (air-gapped, same resident RSF) and DBIndex (PPM's
//     pypi_projects table) belong in PPM for the same reason. DBIndex is the
//     clearest case: a public module cannot reach PPM's database.
//   - FilteredIndex (snapshot-date, prerelease, and yanked policy) and
//     MultiIndex (ordered sources) are generic and belong here, but are out of
//     scope for the initial release.
//
// # Where dependency metadata comes from
//
// Not from the CDN. RFD Rev 15 reversed the carrier for the dependency
// fieldset, so requires_dist, requires_python, and provides_extra are resident
// IN the PyPI RSF and read in-process. Only Files() is CDN-backed. That
// reversal is what makes air-gapped resolution possible, which is why
// CachedJSONIndex refuses Metadata outright rather than serving it from the
// document it already has in hand.
package index
