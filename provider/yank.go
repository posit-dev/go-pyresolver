// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// rootYankPins returns, per package a root requirement pins, the specifiers
// that exempt a yanked version: a root `==` with no wildcard, or a root
// `===`. Only reqs (Options.Requirements, the root's own requirements) are
// considered -- a transitive `==`, found while expanding a dependency, never
// reaches this function and so never exempts. A requirement whose marker is
// false for env is not a requirement, so its pin does not count.
func rootYankPins(reqs []requirement.Requirement, env marker.Environment) map[index.PackageName][]version.Specifier {
	out := make(map[index.PackageName][]version.Specifier)
	for _, r := range reqs {
		if !r.Marker.Evaluate(env, nil) {
			continue
		}
		name := index.NewPackageName(r.Name)
		for _, s := range r.Specifiers.List() {
			if s.Operator() == "===" || (s.Operator() == "==" && !strings.HasSuffix(s.Version(), ".*")) {
				out[name] = append(out[name], s)
			}
		}
	}
	return out
}

// yankExempt reports whether v of pkg is exempted from yank rejection by a
// root pin. Uses the library's own Specifier.Check rather than re-deriving
// PEP 440 equality.
func (p *Provider) yankExempt(pkg Package, v version.Version) bool {
	for _, s := range p.rootYankPins[pkg.Name] {
		if s.Check(v) {
			return true
		}
	}
	return false
}
