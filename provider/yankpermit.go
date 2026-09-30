// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// Transitive yank pins (Options.YankExemptTransitivePins).
//
// pip lets a yanked version through when a requirer that is really in the
// solution pins it with `==`. That depends on the solution, so it is encoded in
// the solver with two virtual packages, over facts that stay fixed for a solve:
//
//   - A yanked foo 1.0 with known pinners depends on permit(foo 1.0). Version i
//     of the permit depends on pinner i at its exact version, and on need(pinner).
//   - need(p) has one version per known requirer r of p (the root included).
//     Version j depends on r at its exact version and on need(r). The root's
//     version needs nothing, so a need chain ends at the root.
//
// Pinners and requirers are learned from the versions a solve chose (never from
// probes), and the resolver solves again when it learns something (NextSolve).
// The encoding only guides the search. Every solution is checked by
// validateYankJustification (yankgate.go); an invalid one is never returned.
// Its permit and need edges whose pinner or requirer the check did not justify
// are all denied, and the resolver solves again.

// maxYankSolves caps the solves for one resolution. The last one is a fallback
// that permits no transitive pin, so it behaves as the option being off.
const maxYankSolves = 32

// yankKey names one version of one project.
type yankKey struct {
	name index.PackageName
	ver  string
}

func yankKeyOf(name index.PackageName, v version.Version) yankKey {
	return yankKey{name: name, ver: v.String()}
}

// yankNode is one version of one package: a pinner, or a requirer.
type yankNode struct {
	pkg Package
	ver version.Version
}

func (r yankNode) id() string { return r.pkg.String() + "\x00" + r.ver.String() }

func (r yankNode) String() string {
	if r.pkg.Kind == KindRoot {
		return "the root project"
	}
	return r.pkg.String() + " " + r.ver.String()
}

// seenPin is one exact pin a chosen version carried.
type seenPin struct {
	name   index.PackageName
	spec   version.Specifier
	pinner yankNode
}

// decidedDeps is one chosen version with its real dependencies.
type decidedDeps struct {
	node yankNode
	deps []dependency
}

// yankState is the transitive-pin state for one solve. known, reqs and denied
// are fixed for the solve; seen, pins and decided are collected during it.
type yankState struct {
	round  int
	known  map[yankKey][]yankNode      // pinners of a yanked version
	reqs   map[Package][]yankNode      // requirers of a package
	denied map[Package]map[string]bool // need edges that failed validation
	// permitDenied holds pinners that failed validation, per yanked version.
	permitDenied map[yankKey]map[string]bool
	// fallback permits no transitive pin: the last solve.
	fallback bool

	live  map[yankKey][]yankNode // pinners whose need has a version, sorted
	needs map[Package][]yankNode // reqs minus denied, root first

	seen    map[yankKey]version.Version
	pins    []seenPin
	decided map[string]decidedDeps
	pinsOf  map[string][]seenPin // exact pins of a chosen version, by decidedKey
	yanked  map[string]bool      // chosen versions that are yanked, by decidedKey
}

func newYankState(prev *yankState) *yankState {
	y := &yankState{
		known:   make(map[yankKey][]yankNode),
		reqs:    make(map[Package][]yankNode),
		denied:  make(map[Package]map[string]bool),
		live:    make(map[yankKey][]yankNode),
		needs:   make(map[Package][]yankNode),
		seen:    make(map[yankKey]version.Version),
		decided: make(map[string]decidedDeps),
		pinsOf:  make(map[string][]seenPin),
		yanked:  make(map[string]bool),

		permitDenied: make(map[yankKey]map[string]bool),
	}
	if prev == nil {
		return y
	}
	y.round = prev.round
	for k, list := range prev.known {
		y.known[k] = slices.Clone(list)
	}
	for k, list := range prev.reqs {
		y.reqs[k] = slices.Clone(list)
	}
	for k, set := range prev.denied {
		y.denied[k] = make(map[string]bool, len(set))
		for id := range set {
			y.denied[k][id] = true
		}
	}
	for k, set := range prev.permitDenied {
		y.permitDenied[k] = make(map[string]bool, len(set))
		for id := range set {
			y.permitDenied[k][id] = true
		}
	}
	return y
}

func byNodeID(a, b yankNode) int { return strings.Compare(a.id(), b.id()) }

// freeze computes needs and live once the learned facts are final for the solve.
func (y *yankState) freeze() {
	for pkg, list := range y.reqs {
		var out []yankNode
		for _, r := range list {
			if !y.denied[pkg][r.id()] {
				out = append(out, r)
			}
		}
		slices.SortFunc(out, func(a, b yankNode) int {
			if (a.pkg.Kind == KindRoot) != (b.pkg.Kind == KindRoot) {
				if a.pkg.Kind == KindRoot {
					return -1
				}
				return 1
			}
			return byNodeID(a, b)
		})
		if len(out) > 0 {
			y.needs[pkg] = out
		}
	}
	if y.fallback {
		return
	}
	for k, list := range y.known {
		var live []yankNode
		for _, pn := range list {
			if len(y.needs[pn.pkg]) > 0 && !y.permitDenied[k][pn.id()] {
				live = append(live, pn)
			}
		}
		slices.SortFunc(live, byNodeID)
		if len(live) > 0 {
			y.live[k] = live
		}
	}
}

// permitted reports whether yanked v of name has a pinner that could be needed,
// and remembers that v is yanked so a pin found later can be matched to it.
func (y *yankState) permitted(name index.PackageName, v version.Version) bool {
	if y == nil {
		return false
	}
	k := yankKeyOf(name, v)
	y.seen[k] = v
	return len(y.live[k]) > 0
}

// refusedPinners names the known pinners of yanked v when none of them can be
// used, for the rejection message. Empty when there are none.
func (y *yankState) refusedPinners(name index.PackageName, v version.Version) []yankNode {
	if y == nil {
		return nil
	}
	list := slices.Clone(y.known[yankKeyOf(name, v)])
	slices.SortFunc(list, byNodeID)
	return list
}

func decidedKey(pkg Package, v version.Version) string {
	return pkg.String() + "\x00" + v.String()
}

// recordDecided keeps a chosen version's dependencies, for learning requirers
// and for validating the solution.
func (y *yankState) recordDecided(pkg Package, v version.Version, deps []dependency) {
	if y == nil {
		return
	}
	y.decided[decidedKey(pkg, v)] = decidedDeps{node: yankNode{pkg: pkg, ver: v}, deps: deps}
}

// discover records the exact `==` pins of a version the solver chose. `===`
// is skipped: pep440set cannot express it, so its requirer is never chosen.
func (y *yankState) discover(pkg Package, v version.Version, meta index.PackageMetadata, env marker.Environment) {
	key := decidedKey(pkg, v)
	y.yanked[key] = meta.Yanked
	reqs := meta.RequiresDist
	var active []string
	if pkg.Extra != "" {
		if !slices.Contains(meta.ProvidesExtra, pkg.Extra) {
			return
		}
		active = []string{pkg.Extra}
		reqs = extraOnly(meta.RequiresDist, env, active)
	}
	for _, r := range reqs {
		if r.URL != "" || !r.Marker.Evaluate(env, active) {
			continue
		}
		for _, s := range exactPins(r) {
			if s.Operator() != "==" {
				continue
			}
			sp := seenPin{
				name:   index.NewPackageName(r.Name),
				spec:   s,
				pinner: yankNode{pkg: pkg, ver: v},
			}
			y.pins = append(y.pins, sp)
			y.pinsOf[key] = append(y.pinsOf[key], sp)
		}
	}
}

// exactPins returns a requirement's `==` (no wildcard) and `===` specifiers.
func exactPins(r requirement.Requirement) []version.Specifier {
	var out []version.Specifier
	for _, s := range r.Specifiers.List() {
		if s.Operator() == "===" || (s.Operator() == "==" && !strings.HasSuffix(s.Version(), ".*")) {
			out = append(out, s)
		}
	}
	return out
}

// permitPackage is the virtual package guarding yanked v of name.
func permitPackage(name index.PackageName, v version.Version) Package {
	return Package{Kind: kindYankPermit, Name: name, permitVersion: v.String()}
}

// needPackage is the virtual package that stands for "something needs pkg".
func needPackage(pkg Package) Package {
	return Package{Kind: kindYankNeed, Name: pkg.Name, Extra: pkg.Extra}
}

// needTarget is the real package a need package stands for.
func needTarget(pkg Package) Package {
	return Package{Kind: KindProject, Name: pkg.Name, Extra: pkg.Extra}
}

// virtualVersion is the version of a virtual package that stands for entry i.
func virtualVersion(i int) version.Version { return version.MustParse(strconv.Itoa(i + 1)) }

// permitDependency returns the permit a chosen yanked base version depends on.
// A root pin needs none: the root is always selected.
func (p *Provider) permitDependency(pkg Package, v version.Version, meta index.PackageMetadata) (dependency, bool) {
	if pkg.Extra != "" || !meta.Yanked || p.yankExempt(pkg, v) {
		return dependency{}, false
	}
	if len(p.yank.live[yankKeyOf(pkg.Name, v)]) == 0 {
		return dependency{}, false
	}
	return dependency{Package: permitPackage(pkg.Name, v), Allowed: pep440set.All()}, true
}

// virtualEntries is what a virtual package's versions stand for: pinners for a
// permit, requirers for a need.
func (p *Provider) virtualEntries(pkg Package) []yankNode {
	if p.yank == nil {
		return nil
	}
	switch pkg.Kind {
	case kindYankPermit:
		return p.yank.live[yankKey{name: pkg.Name, ver: pkg.permitVersion}]
	case kindYankNeed:
		return p.yank.needs[needTarget(pkg)]
	}
	return nil
}

// virtualCandidates offers the first entry in range; one version at a time.
func (p *Provider) virtualCandidates(allowed pep440set.Set, pkg Package) (pep440set.Set, bool, int, error) {
	var (
		best  version.Version
		found bool
		rank  int
	)
	for i := range p.virtualEntries(pkg) {
		vv := virtualVersion(i)
		if !allowed.Contains(vv) {
			continue
		}
		rank++
		if !found {
			best, found = vv, true
		}
	}
	if !found {
		return pep440set.Empty(), false, 0, nil
	}
	return pep440set.Exactly(best), true, rank, nil
}

// virtualEntry returns the entry version v of a virtual package stands for.
func (p *Provider) virtualEntry(pkg Package, v version.Version) (yankNode, error) {
	entries := p.virtualEntries(pkg)
	i, err := strconv.Atoi(v.String())
	if err != nil || i < 1 || i > len(entries) {
		return yankNode{}, fmt.Errorf("provider: %s has no version %s", pkg, v)
	}
	return entries[i-1], nil
}

// virtualDependencies: entry i is needed at its exact version, together with
// something that needs it in turn. The root needs nothing.
func (p *Provider) virtualDependencies(pkg Package, v version.Version) ([]dependency, error) {
	n, err := p.virtualEntry(pkg, v)
	if err != nil {
		return nil, err
	}
	if n.pkg.Kind == KindRoot {
		return nil, nil
	}
	return []dependency{
		{Package: n.pkg, Allowed: pep440set.Exactly(n.ver)},
		{Package: needPackage(n.pkg), Allowed: pep440set.All()},
	}, nil
}

// NextSolve is for the resolver package only. It is called after each solve
// with the solution's selected set, or nil when the solve failed. valid reports
// whether the solution passed validateYankJustification; an invalid one must
// not be returned. next is a Provider for another solve when this one learned a
// pin or requirer, or its solution was invalid, and nil when the result stands.
// Always (nil, true, nil) without YankExemptTransitivePins.
func (p *Provider) NextSolve(selected map[Package]pep440set.Set) (next *Provider, valid bool, err error) {
	y := p.yank
	if y == nil {
		return nil, true, nil
	}
	valid = true
	var justified map[string]bool
	if selected != nil {
		var bad []yankNode
		justified, bad = p.validateYankJustification(selected)
		valid = len(bad) == 0
	}
	if y.fallback {
		return nil, valid, nil
	}

	ny := newYankState(y)
	learned := false
	for _, sp := range y.pins {
		for k, v := range y.seen {
			if k.name != sp.name || !sp.spec.Check(v) {
				continue
			}
			if !slices.ContainsFunc(ny.known[k], func(pn yankNode) bool { return pn.id() == sp.pinner.id() }) {
				ny.known[k] = append(ny.known[k], sp.pinner)
				learned = true
			}
		}
	}
	if len(ny.known) > 0 && ny.learnRequirers(y.decided) {
		learned = true
	}
	if learned {
		// Denials were judged on what was known then; start over with more.
		clear(ny.denied)
		clear(ny.permitDenied)
	}
	if !valid && !denyUnjustified(p, selected, justified, ny) {
		// Nothing to deny: not expected, but solve once more without
		// transitive pins rather than loop.
		ny.fallback = true
	}
	if valid && !learned {
		return nil, valid, nil
	}

	ny.round = y.round + 1
	ny.fallback = ny.fallback || ny.round >= maxYankSolves-1
	np := New(p.ctx, p.index, p.opts)
	np.ranked = p.ranked
	ny.freeze()
	np.yank = ny
	return np, valid, nil
}

// IsFallback is for the resolver package only: whether this provider permits
// no transitive pin because the solve cap was reached.
func (p *Provider) IsFallback() bool { return p.yank != nil && p.yank.fallback }

// denyUnjustified denies, in next, every permit and need edge in the solution
// whose pinner or requirer is outside justified, and reports whether it denied
// any. An invalid solution always has one: its first unjustified version was
// pulled in by a virtual edge whose entry is also unjustified.
func denyUnjustified(p *Provider, selected map[Package]pep440set.Set, justified map[string]bool, next *yankState) bool {
	denied := false
	for pkg, set := range selected {
		if pkg.Kind != kindYankPermit && pkg.Kind != kindYankNeed {
			continue
		}
		v, _ := set.Singleton()
		n, err := p.virtualEntry(pkg, v)
		if err != nil || n.pkg.Kind == KindRoot || justified[n.id()] {
			continue
		}
		if pkg.Kind == kindYankNeed {
			target := needTarget(pkg)
			if next.denied[target] == nil {
				next.denied[target] = make(map[string]bool)
			}
			next.denied[target][n.id()] = true
		} else {
			k := yankKey{name: pkg.Name, ver: pkg.permitVersion}
			if next.permitDenied[k] == nil {
				next.permitDenied[k] = make(map[string]bool)
			}
			next.permitDenied[k][n.id()] = true
		}
		denied = true
	}
	return denied
}

// learnRequirers records every real edge the solve's chosen versions carried.
// It reports whether a new edge reaches a package some need chain could use,
// that is a known pinner or a requirer of one, transitively.
func (y *yankState) learnRequirers(decided map[string]decidedDeps) bool {
	var added []Package
	keys := make([]string, 0, len(decided))
	for k := range decided {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		d := decided[k]
		for _, dep := range d.deps {
			if dep.Package.Kind != KindProject {
				continue
			}
			if slices.ContainsFunc(y.reqs[dep.Package], func(r yankNode) bool { return r.id() == d.node.id() }) {
				continue
			}
			y.reqs[dep.Package] = append(y.reqs[dep.Package], d.node)
			added = append(added, dep.Package)
		}
	}
	if len(added) == 0 {
		return false
	}

	relevant := make(map[Package]bool)
	var queue []Package
	for _, list := range y.known {
		for _, pn := range list {
			if !relevant[pn.pkg] {
				relevant[pn.pkg] = true
				queue = append(queue, pn.pkg)
			}
		}
	}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		for _, r := range y.reqs[pkg] {
			if r.pkg.Kind == KindProject && !relevant[r.pkg] {
				relevant[r.pkg] = true
				queue = append(queue, r.pkg)
			}
		}
	}
	return slices.ContainsFunc(added, func(pkg Package) bool { return relevant[pkg] })
}

// YankPin is for the resolver package only: a yanked version in a solution and
// one requirer, also in that solution, that pins it exactly. Requester is
// Root() for a root pin.
type YankPin struct {
	Package          index.PackageName
	Version          version.Version
	Requester        Package
	RequesterVersion version.Version
}

// YankPins is for the resolver package only. It lists the yanked versions in
// selected with the selected requirers that pin them, and reads no metadata.
func (p *Provider) YankPins(selected map[Package]pep440set.Set) []YankPin {
	var out []YankPin
	for pkg, set := range selected {
		if pkg.Kind != KindProject || pkg.Extra != "" {
			continue
		}
		v, ok := set.Singleton()
		if !ok || !p.yankedOffered[yankKeyOf(pkg.Name, v)] {
			continue
		}
		if p.yankExempt(pkg, v) {
			out = append(out, YankPin{Package: pkg.Name, Version: v, Requester: Root()})
		}
		if p.yank == nil {
			continue
		}
		for _, pn := range p.yank.known[yankKeyOf(pkg.Name, v)] {
			if rv, ok := selected[pn.pkg].Singleton(); ok && rv.Equal(pn.ver) {
				out = append(out, YankPin{Package: pkg.Name, Version: v, Requester: pn.pkg, RequesterVersion: pn.ver})
			}
		}
	}
	return out
}

// DescribePermit is for the resolver package's failure reports only. For a
// yank-pin virtual package it names who a version set stands for ("from a 2.0
// or d 1.0"); none is true when the set stands for no one. ok is false for
// every real package.
func (p *Provider) DescribePermit(pkg Package, s pep440set.Set) (text string, none, ok bool) {
	if pkg.Kind != kindYankPermit && pkg.Kind != kindYankNeed {
		return "", false, false
	}
	var names []string
	for i, n := range p.virtualEntries(pkg) {
		if s.Contains(virtualVersion(i)) {
			names = append(names, n.String())
		}
	}
	if len(names) == 0 {
		return "", true, true
	}
	return "from " + strings.Join(names, " or "), false, true
}

// VirtualPhrases is for the resolver package's failure reports only. For a
// yank-pin virtual package it returns how to say "every version of it" and "no
// version of it matches the rest", in terms of the real packages.
func VirtualPhrases(pkg Package) (every, nothing string, ok bool) {
	switch pkg.Kind {
	case kindYankPermit:
		return "every exact pin on " + string(pkg.Name) + " " + pkg.permitVersion,
			"no other package pins " + string(pkg.Name) + " " + pkg.permitVersion, true
	case kindYankNeed:
		name := needTarget(pkg).String()
		return "every requirement on " + name, "nothing else requires " + name, true
	}
	return "", "", false
}

// yankedReason is the KindYanked reason. pinners are the version's known
// exact pinners, none of which this resolution otherwise requires.
func yankedReason(pinners []yankNode) string {
	switch len(pinners) {
	case 0:
		return "it was yanked from the index and this resolution has no exact root pin for it"
	case 1:
		return "it was yanked from the index, and its exact pin from " + pinners[0].String() +
			" could not be used in this resolution (" + pinners[0].String() + " is not otherwise required)"
	}
	names := make([]string, len(pinners))
	for i, pn := range pinners {
		names[i] = pn.String()
	}
	return "it was yanked from the index, and its exact pins from " + strings.Join(names, " and ") +
		" could not be used in this resolution (none of them is otherwise required)"
}
