// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/version"
)

// validateYankJustification is the soundness gate for YankExemptTransitivePins.
// It reads only the solution and the real requirements of its chosen versions,
// never the permit/need encoding. The rule:
//
// J starts as {root}. A selected version X joins J when a member of J has a
// requirement on X's package that X satisfies and, if X is yanked, a member
// of J pins X's package to exactly X's version with `==` (a root `==` or `===`
// counts). The solution is valid when every selected version is in J.
//
// It returns J, keyed by node id, and the selected versions outside it.
func (p *Provider) validateYankJustification(selected map[Package]pep440set.Set) (map[string]bool, []yankNode) {
	y := p.yank
	var nodes []yankNode
	for pkg, set := range selected {
		if pkg.Kind != KindProject {
			continue
		}
		v, _ := set.Singleton()
		nodes = append(nodes, yankNode{pkg: pkg, ver: v})
	}

	rootVer, _ := selected[Root()].Singleton()
	inJ := map[string]bool{}
	reached := map[string]bool{} // has a real edge from J
	pinned := map[string]bool{}  // has an exact pin from J
	queue := []yankNode{{pkg: Root(), ver: rootVer}}
	inJ[queue[0].id()] = true

	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		key := decidedKey(m.pkg, m.ver)
		for _, x := range nodes {
			if inJ[x.id()] {
				continue
			}
			for _, d := range y.decided[key].deps {
				if d.Package == x.pkg && d.Allowed.Contains(x.ver) {
					reached[x.id()] = true
				}
			}
			if m.pkg.Kind == KindRoot {
				if p.rootPinsVersion(x.pkg, x.ver) {
					pinned[x.id()] = true
				}
			} else if pinsVersion(y.pinsOf[key], x.pkg, x.ver) {
				pinned[x.id()] = true
			}
		}
		// A new member can admit any node not yet in J, so recheck them all.
		for _, x := range nodes {
			if inJ[x.id()] || !reached[x.id()] {
				continue
			}
			if y.yanked[decidedKey(x.pkg, x.ver)] && !pinned[x.id()] {
				continue
			}
			inJ[x.id()] = true
			queue = append(queue, x)
		}
	}

	var bad []yankNode
	for _, x := range nodes {
		if !inJ[x.id()] {
			bad = append(bad, x)
		}
	}
	return inJ, bad
}

// rootPinsVersion reports whether an active root requirement pins pkg exactly
// to v. It reads the requirements itself rather than trusting yankExempt.
func (p *Provider) rootPinsVersion(pkg Package, v version.Version) bool {
	for _, r := range p.opts.Requirements {
		if index.NewPackageName(r.Name) != pkg.Name || !r.Marker.Evaluate(p.opts.Environment, nil) {
			continue
		}
		for _, s := range exactPins(r) {
			if s.Check(v) {
				return true
			}
		}
	}
	return false
}

// pinsVersion reports whether pins holds an exact `==` pin on pkg's project
// that v satisfies.
func pinsVersion(pins []seenPin, pkg Package, v version.Version) bool {
	for _, sp := range pins {
		if sp.name == pkg.Name && sp.spec.Check(v) {
			return true
		}
	}
	return false
}
