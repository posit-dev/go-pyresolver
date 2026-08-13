// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"strings"

	"github.com/posit-dev/go-python-packaging/version"
)

// ReasonMetadataUnavailable is the Unusable.Reason recorded for a release whose
// dependency metadata cannot be read without building it -- an sdist-only or
// dynamic-metadata release.
//
// It is a named constant rather than a literal because a consumer has to be
// able to pick these records out of the rest: this is the one reason with a
// remedy a user can act on ("pin to a version that ships a wheel"), and the
// resolver's failure explanation says so. Matching the sentence by hand from
// another package would silently stop matching the first time the wording is
// improved.
const ReasonMetadataUnavailable = "no dependency metadata is published for it " +
	"(an sdist-only or dynamic-metadata release)"

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
	// despite Reason. False means it was passed over: not selectable, so the
	// walk moved on to the next-ranked version.
	Offered bool
}

// Unusable returns every recorded reason, in the order first encountered.
//
// Records are deduplicated: the solver asks about the same package many times
// as it backtracks, and a report listing one sdist-only version forty times
// would be unreadable.
//
// # ⚠️ This is what was ENCOUNTERED, not an audit of every published version
//
// Candidates walks versions in ranked order and stops at the first usable one, so
// a version older than the one chosen is never examined and never recorded. Before
// the found/rank split every in-range version was tested, and so every unusable
// one appeared here.
//
// The records that matter are still collected. When a package has nothing usable
// the walk is exhaustive by necessity, so every reason is recorded — and that is
// the case a report most needs to explain, because it is the one that produces
// "no version of X matches". What is dropped is reasons about versions the
// resolution had already moved past, which no failure was going to be attributed
// to.
//
// The exhaustive-when-nothing-is-usable half IS tested, by
// TestEveryReasonIsRecordedWhenNOTHINGIsUsable.
//
// ⚠️ The broader claim that no failure REPORT changes is not. A 200-package prototype
// comparison against the production snapshot produced none, but nothing here pins it:
// the differential compares found, best and rank, not Unusable() and not rendered
// reports. Treat a report difference as possible-but-unexpected rather than excluded.
//
// Do not read a short list as "nothing else is wrong with this package".
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
