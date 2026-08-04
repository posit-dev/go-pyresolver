// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

// VersionDeps is the dependency metadata for one version of one package, as
// carried in the RSF.
//
// Every field is the RAW published string, unparsed. Parsing PEP 508
// requirements and PEP 440 specifiers is the caller's job — see
// github.com/posit-dev/go-python-packaging — which keeps this package's only
// dependency a zstd implementation, and lets a consumer that just wants to
// echo the strings back avoid the parsing cost entirely.
//
// A zero VersionDeps is meaningful: it says this version was captured and
// declares no dependencies, no interpreter constraint, and no extras. That is
// different from a version being absent from a decoded map, which says nothing
// was captured for it. Collapsing the two would make "declares no
// dependencies" indistinguishable from "we do not know", and a resolver must
// treat those differently.
type VersionDeps struct {
	// RequiresDist holds the raw PEP 508 requirement strings, each possibly
	// carrying an environment marker.
	RequiresDist []string

	// RequiresPython is the raw PEP 440 specifier set constraining the
	// interpreter, or "" when unconstrained.
	RequiresPython string

	// ProvidesExtra lists the extras this version declares.
	ProvidesExtra []string
}
