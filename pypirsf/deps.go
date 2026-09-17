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
//
// The tag fields repeat that same distinction one level down, and "captured"
// means a narrower thing there: a zero VersionDeps has TagsCaptured false, so
// the version is present with its deps and carries no tag claim at all.
type VersionDeps struct {
	// RequiresDist holds the raw PEP 508 requirement strings, each possibly
	// carrying an environment marker.
	RequiresDist []string

	// RequiresPython is the raw PEP 440 specifier set constraining the
	// interpreter, or "" when unconstrained.
	RequiresPython string

	// ProvidesExtra lists the extras this version declares.
	ProvidesExtra []string

	// WheelTags holds the PEP 425 tag triples ("py3-none-any") of the wheels this
	// version publishes, as raw strings. Matching them against a target
	// environment belongs to the caller, same as PEP 508 and PEP 440 parsing.
	//
	// ⚠️ READ-ONLY. Many versions' tag slices alias one pool backing array, in
	// the same kind and lifetime as RequiresDist: sorting or de-duplicating this
	// slice in place corrupts every version sharing the slot. Copy before
	// mutating.
	//
	// Empty means "publishes no wheels" only when TagsCaptured is true. See that
	// field: the two together carry three distinct states, and reading emptiness
	// alone conflates two of them.
	WheelTags []string

	// HasSdist reports that this version publishes a source distribution.
	//
	// It is the difference between "no compatible wheel, but buildable from
	// source" and "nothing installable here at all", which is the only reason a
	// consumer can reject a version for its tags without over-rejecting. Only
	// meaningful when TagsCaptured is true.
	HasSdist bool

	// TagsCaptured reports that the producer derived a tag claim for this
	// version. It is not redundant with len(WheelTags) > 0, and that is the whole
	// point:
	//
	//	captured, tags non-empty -> these are the wheels
	//	captured, tags empty     -> publishes no wheels (authoritative)
	//	NOT captured             -> unknown; says nothing about wheels
	//
	// A consumer that reads the third state as the second filters out a version
	// it knows nothing about. Snapshots predating the wheel-tag work have every
	// version uncaptured, which is why absence cannot be an error either.
	//
	// ⚠️ Per-version capture is NOT sufficient to enable filtering. Ask the file
	// whether its vocabulary is complete (File.WheelTagsComplete): on a partially
	// backfilled snapshot the captured minority is not a corpus you can filter
	// against.
	TagsCaptured bool
}
