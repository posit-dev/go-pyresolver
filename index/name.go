// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import "github.com/posit-dev/go-python-packaging/extras"

// PackageName is a PEP 503-normalized Python project name.
//
// It is a distinct type rather than a bare string on purpose. Normalization is
// a correctness concern here, not cosmetics: if "Flask" and "flask" reach the
// solver as different identities, it builds two nodes for one package and can
// report a conflict that does not exist. Making the normalized form its own
// type means an un-normalized string cannot reach the interface by accident.
//
// Always construct with NewPackageName. A conversion like PackageName(s)
// compiles but skips normalization, so it is only correct when s is already
// known-normalized (for example, a value read back out of this package).
type PackageName string

// NewPackageName normalizes raw per PEP 503: lowercase, then collapse any run
// of "-", "_", or "." into a single "-".
//
// The normalization is delegated to go-python-packaging's extras.Normalize,
// which implements exactly this algorithm -- PEP 685 extra-name normalization
// and PEP 503 project-name normalization are the same transformation, and that
// function's own documentation describes it as mirroring pypa/packaging's
// canonicalize_name for both. Reusing it avoids a second copy of the
// transformation that could drift from the first.
//
// The name is admittedly a poor fit for this call site; a PEP 503-named entry
// point belongs in go-python-packaging alongside the PEP 685 one. Tracked in
// rstudio/package-manager#19425.
func NewPackageName(raw string) PackageName {
	return PackageName(extras.Normalize(raw))
}

// String returns the normalized name.
func (p PackageName) String() string { return string(p) }

// NormalizeExtra normalizes a Python extra name per PEP 685.
//
// Provided so callers assembling extras alongside package names do not have to
// import go-python-packaging's extras package directly and then wonder whether
// the two normalizations agree. They do -- it is the same function.
func NormalizeExtra(raw string) string { return extras.Normalize(raw) }
