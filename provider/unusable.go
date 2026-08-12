// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// Unusable records something the resolution could not use about one version of
// one package, so a failure report can say "flask 3.0 exists but ships only an
// sdist" instead of "no versions available" -- which is the single worst thing
// it could say about a version the user can plainly see on PyPI.
//
// # A record is not the same as an exclusion
//
// Most reasons remove the version from consideration, but not all: an
// unparseable Requires-Python leaves the version a candidate with an
// unconstrained interpreter, matching the index decoder's deliberate choice to
// over-admit rather than silently change the resolution. That choice is only
// defensible if it is visible, so it is recorded too -- and Offered is what
// keeps the two cases apart, rather than leaving a consumer to infer it from
// the wording of Reason.
type Unusable struct {
	// Package is what the record is about, including its extra if the record
	// concerns an extra rather than the base package.
	Package Package

	// Version is the specific version concerned.
	Version version.Version

	// Reason is a human-readable phrase completing "... because ...", meant to
	// be read by whoever ran the resolution.
	Reason string

	// Offered reports whether the version was still offered to the solver
	// despite Reason. False means it was excluded from the candidate count.
	Offered bool
}

// Unusable returns every recorded reason, in the order first encountered.
//
// Records are deduplicated: the solver asks about the same package many times
// as it backtracks, and a report listing one sdist-only version forty times
// would be unreadable.
func (p *Provider) Unusable() []Unusable {
	return p.unusable
}

// record adds one reason, ignoring a repeat of one already held.
//
// The dedupe key is built from strings rather than from the struct itself
// because version.Version holds slices, so it is not comparable and cannot key
// a map.
func (p *Provider) record(pkg Package, v version.Version, reason string, offered bool) {
	if reason == "" {
		return
	}
	key := strings.Join([]string{pkg.String(), v.String(), reason}, "\x00")
	if p.recorded[key] {
		return
	}
	p.recorded[key] = true
	p.unusable = append(p.unusable, Unusable{
		Package: pkg,
		Version: v,
		Reason:  reason,
		Offered: offered,
	})
}
