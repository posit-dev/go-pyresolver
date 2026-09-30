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
// pip lets a yanked version through when a selected requirer pins it with `==`.
// That depends on the solution, so it cannot be a fixed fact about the version.
// It is encoded in the solver instead: each solve has a fixed pinner set K, a
// yanked foo 1.0 with pinners in K depends on a virtual package permit(foo 1.0),
// and version i of that package depends on pinner i at its exact version. So
// foo 1.0 is chosen only together with one of its pinners.
//
// K is learned: pins a solve's chosen versions carry to a yanked version not
// yet in K are collected, and the resolver solves again with them (NextSolve).
// A permit can also drag in a pinner nothing else needs. pip would not install
// that pinner, so a pinner only reachable through permits is denied for that
// version and the resolver solves again. K and the denied set only grow, and
// both are bounded by the pins in the index, so the loop ends.

// maxYankSolves is a defensive bound. Every extra solve adds at least one
// (version, pinner) pair to K or to the denied set, and neither shrinks, so the
// real bound is twice the number of distinct exact pins reached. Tripping this
// means that argument is broken.
const maxYankSolves = 10000

// yankKey names one version of one project.
type yankKey struct {
	name index.PackageName
	ver  string
}

func yankKeyOf(name index.PackageName, v version.Version) yankKey {
	return yankKey{name: name, ver: v.String()}
}

// yankPinner is a (package, version) whose requirements pin a yanked version.
type yankPinner struct {
	pkg Package
	ver version.Version
}

func (r yankPinner) id() string { return r.pkg.String() + "\x00" + r.ver.String() }

// seenPin is one exact pin a chosen version carried.
type seenPin struct {
	name   index.PackageName
	spec   version.Specifier
	pinner yankPinner
}

// yankState is the transitive-pin state for one solve. known and denied are
// fixed for the solve; seen, pins and decided are collected during it.
type yankState struct {
	round  int
	known  map[yankKey][]yankPinner
	denied map[yankKey]map[string]bool
	live   map[yankKey][]yankPinner // known minus denied, sorted

	seen    map[yankKey]version.Version
	pins    []seenPin
	decided map[string][]dependency
}

func newYankState(prev *yankState) *yankState {
	y := &yankState{
		known:   make(map[yankKey][]yankPinner),
		denied:  make(map[yankKey]map[string]bool),
		live:    make(map[yankKey][]yankPinner),
		seen:    make(map[yankKey]version.Version),
		decided: make(map[string][]dependency),
	}
	if prev == nil {
		return y
	}
	y.round = prev.round
	for k, list := range prev.known {
		y.known[k] = slices.Clone(list)
	}
	for k, set := range prev.denied {
		y.denied[k] = make(map[string]bool, len(set))
		for id := range set {
			y.denied[k][id] = true
		}
	}
	return y
}

// freeze computes live once K and the denied set are final for the solve.
func (y *yankState) freeze() {
	for k, list := range y.known {
		var live []yankPinner
		for _, pn := range list {
			if !y.denied[k][pn.id()] {
				live = append(live, pn)
			}
		}
		slices.SortFunc(live, func(a, b yankPinner) int { return strings.Compare(a.id(), b.id()) })
		if len(live) > 0 {
			y.live[k] = live
		}
	}
}

// permitted reports whether yanked v of name has a live pinner, and remembers
// that v is yanked so a pin found later in this solve can be matched to it.
func (y *yankState) permitted(name index.PackageName, v version.Version) bool {
	if y == nil {
		return false
	}
	k := yankKeyOf(name, v)
	y.seen[k] = v
	return len(y.live[k]) > 0
}

func decidedKey(pkg Package, v version.Version) string {
	return pkg.String() + "\x00" + v.String()
}

// recordDecided keeps a chosen version's dependencies for the liveness check.
func (y *yankState) recordDecided(pkg Package, v version.Version, deps []dependency) {
	if y == nil {
		return
	}
	y.decided[decidedKey(pkg, v)] = deps
}

// discover records the exact `==` pins of a version the solver chose. `===`
// is skipped: pep440set cannot express it, so its requirer is never chosen.
func (y *yankState) discover(pkg Package, v version.Version, meta index.PackageMetadata, env marker.Environment) {
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
			y.pins = append(y.pins, seenPin{
				name:   index.NewPackageName(r.Name),
				spec:   s,
				pinner: yankPinner{pkg: pkg, ver: v},
			})
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

// permitVersion is the version of a permit package that stands for pinner i.
func permitVersion(i int) version.Version { return version.MustParse(strconv.Itoa(i + 1)) }

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

func (p *Provider) permitPinners(pkg Package) []yankPinner {
	if p.yank == nil {
		return nil
	}
	return p.yank.live[yankKey{name: pkg.Name, ver: pkg.permitVersion}]
}

// permitCandidates offers the first live pinner in range; one version each.
func (p *Provider) permitCandidates(allowed pep440set.Set, pkg Package) (pep440set.Set, bool, int, error) {
	var (
		best  version.Version
		found bool
		rank  int
	)
	for i := range p.permitPinners(pkg) {
		pv := permitVersion(i)
		if !allowed.Contains(pv) {
			continue
		}
		rank++
		if !found {
			best, found = pv, true
		}
	}
	if !found {
		return pep440set.Empty(), false, 0, nil
	}
	return pep440set.Exactly(best), true, rank, nil
}

// permitDependencies: version i of a permit depends on pinner i, exactly.
func (p *Provider) permitDependencies(pkg Package, v version.Version) ([]dependency, error) {
	pinners := p.permitPinners(pkg)
	i, err := strconv.Atoi(v.String())
	if err != nil || i < 1 || i > len(pinners) {
		return nil, fmt.Errorf("provider: dependencies of %s: no pinner %s", pkg, v)
	}
	pn := pinners[i-1]
	return []dependency{{Package: pn.pkg, Allowed: pep440set.Exactly(pn.ver)}}, nil
}

// NextSolve is called after each solve with the solution's selected set, or
// nil when the solve failed. It returns a Provider for another solve when this
// one found a new pinner or selected a pinner only a permit needed, and nil
// when the result stands. Always nil without YankExemptTransitivePins.
func (p *Provider) NextSolve(selected map[Package]pep440set.Set) (*Provider, error) {
	y := p.yank
	if y == nil {
		return nil, nil
	}
	next := newYankState(y)
	changed := false

	for _, sp := range y.pins {
		for k, v := range y.seen {
			if k.name != sp.name || !sp.spec.Check(v) {
				continue
			}
			if !slices.ContainsFunc(next.known[k], func(pn yankPinner) bool { return pn.id() == sp.pinner.id() }) {
				next.known[k] = append(next.known[k], sp.pinner)
				changed = true
			}
		}
	}

	if selected != nil {
		reach := y.reachable(selected)
		for pkg, set := range selected {
			if pkg.Kind != kindYankPermit {
				continue
			}
			pv, _ := set.Singleton()
			i, err := strconv.Atoi(pv.String())
			pinners := p.permitPinners(pkg)
			if err != nil || i < 1 || i > len(pinners) {
				return nil, fmt.Errorf("provider: %s was decided as %v", pkg, set)
			}
			if pn := pinners[i-1]; !reach[pn.pkg] {
				k := yankKey{name: pkg.Name, ver: pkg.permitVersion}
				if next.denied[k] == nil {
					next.denied[k] = make(map[string]bool)
				}
				next.denied[k][pn.id()] = true
				changed = true
			}
		}
	}

	if !changed {
		return nil, nil
	}
	next.round = y.round + 1
	if next.round >= maxYankSolves {
		return nil, fmt.Errorf("provider: transitive yank pins did not settle after %d solves", next.round)
	}
	np := New(p.ctx, p.index, p.opts)
	np.ranked = p.ranked
	next.freeze()
	np.yank = next
	return np, nil
}

// reachable is the set of selected packages the root reaches without passing
// through a permit.
func (y *yankState) reachable(selected map[Package]pep440set.Set) map[Package]bool {
	reach := map[Package]bool{Root(): true}
	queue := []Package{Root()}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		v, ok := selected[pkg].Singleton()
		if !ok {
			continue
		}
		for _, d := range y.decided[decidedKey(pkg, v)] {
			if d.Package.Kind == kindYankPermit || reach[d.Package] {
				continue
			}
			if _, ok := selected[d.Package]; ok {
				reach[d.Package] = true
				queue = append(queue, d.Package)
			}
		}
	}
	return reach
}

// YankPin is a yanked version in a solution and one requirer, also in that
// solution, that pins it exactly. Requester is Root() for a root pin.
type YankPin struct {
	Package          index.PackageName
	Version          version.Version
	Requester        Package
	RequesterVersion version.Version
}

// YankPins lists the yanked versions in selected with the selected requirers
// that pin them. It reads no metadata.
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

// DescribePermit names the pinners a permit package's version set stands for,
// so a failure report names the real pin. ok is false for any other package.
func (p *Provider) DescribePermit(pkg Package, s pep440set.Set) (string, bool) {
	if pkg.Kind != kindYankPermit {
		return "", false
	}
	var names []string
	for i, pn := range p.permitPinners(pkg) {
		if s.Contains(permitVersion(i)) {
			names = append(names, pn.pkg.String()+" "+pn.ver.String())
		}
	}
	if len(names) == 0 {
		return "from no selected package", true
	}
	return "from " + strings.Join(names, " or "), true
}
