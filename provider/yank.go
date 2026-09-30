// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"slices"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// YankPin is one exact pin -- an `==` with no wildcard, or an `===` -- that
// exempts a matching yanked version from rejection. Requester says who wrote it.
type YankPin struct {
	// Package is the project the pin is on.
	Package index.PackageName

	// Specifier is the exact-pin specifier itself.
	Specifier version.Specifier

	// Requester is Root() for a caller requirement, else the project that
	// declared the pin.
	Requester Package

	// RequesterVersion is the requester's version. Zero value for the root.
	RequesterVersion version.Version
}

// exactPins returns the exempting specifiers of one requirement: each `==`
// with no wildcard, and each `===`. Ranges and `==1.*` never exempt.
func exactPins(r requirement.Requirement) []version.Specifier {
	var out []version.Specifier
	for _, s := range r.Specifiers.List() {
		if s.Operator() == "===" || (s.Operator() == "==" && !strings.HasSuffix(s.Version(), ".*")) {
			out = append(out, s)
		}
	}
	return out
}

// rootYankPins returns the exempting pins of Options.Requirements, the root's
// own requirements.
func rootYankPins(reqs []requirement.Requirement) []YankPin {
	var out []YankPin
	for _, r := range reqs {
		for _, s := range exactPins(r) {
			out = append(out, YankPin{
				Package:   index.NewPackageName(r.Name),
				Specifier: s,
				Requester: Root(),
			})
		}
	}
	return out
}

// addYankPin records one pin, ignoring a repeat.
func (p *Provider) addYankPin(pin YankPin) {
	key := strings.Join([]string{
		string(pin.Package), pin.Specifier.String(), pin.Requester.String(), pin.RequesterVersion.String(),
	}, "\x00")
	if p.yankPinsSeen[key] {
		return
	}
	p.yankPinsSeen[key] = true
	p.yankPins = append(p.yankPins, pin)
	p.yankPinsByName[pin.Package] = append(p.yankPinsByName[pin.Package], pin)
}

// recordTransitivePins records the exact pins a DECIDED version's requirements
// carry. Called only from Dependencies, never from the usability probe: a
// version that is merely tried must not exempt anything.
func (p *Provider) recordTransitivePins(
	pkg Package, v version.Version, reqs []requirement.Requirement, env marker.Environment, active []string,
) {
	for _, r := range reqs {
		if !r.Marker.Evaluate(env, active) || r.URL != "" {
			continue
		}
		for _, s := range exactPins(r) {
			p.addYankPin(YankPin{
				Package:          index.NewPackageName(r.Name),
				Specifier:        s,
				Requester:        pkg,
				RequesterVersion: v,
			})
		}
	}
}

// YankPins returns every exact pin this Provider has seen, root first. With
// Options.YankExemptTransitivePins it includes pins from versions the solver
// later backtracked past; resolver.Resolve keeps only the live ones.
func (p *Provider) YankPins() []YankPin {
	return slices.Clone(p.yankPins)
}

// yankExempt reports whether v of pkg is exempted from yank rejection by a
// known pin. Uses the library's own Specifier.Check rather than re-deriving
// PEP 440 equality. The check is per pin, so a pin on 1.0 never exempts 1.1.
func (p *Provider) yankExempt(pkg Package, v version.Version) bool {
	for _, pin := range p.yankPinsByName[pkg.Name] {
		if pin.Specifier.Check(v) {
			return true
		}
	}
	return false
}
