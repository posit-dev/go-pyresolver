// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// dependency is the concrete instantiation of go-pubgrub's generic dependency,
// spelled once so the rest of this file stays readable.
type dependency = solver.Dependency[Package, pep440set.Set]

// Conformance is asserted here rather than left to the one place that constructs a
// solver, so a signature drift in solver.Provider fails against the file that owns
// the obligation instead of somewhere downstream.
var _ solver.Provider[Package, pep440set.Set] = (*Provider)(nil)

// Dependencies implements solver.Provider.
//
// # Depender is deliberately left empty
//
// go-pubgrub's Depender field collapses a requirement shared by a run of
// adjacent versions into one incompatibility. Empty means "only the version
// being considered", which its documentation describes as always correct and
// only ever costing extra incompatibilities. Filling it in is an optimization
// whose payoff depends on how often adjacent PyPI releases have byte-identical
// requirements, which is a measurement nobody has taken; revisit it with a
// benchmark, not before.
func (p *Provider) Dependencies(pkg Package, ver pep440set.Set) ([]dependency, error) {
	v, ok := ver.Singleton()
	if !ok {
		// The solver only ever asks about one concrete version. Guessing which
		// version a range meant would answer a question nobody asked, and the
		// resulting resolution would look successful.
		return nil, fmt.Errorf("provider: dependencies of %s: %v is not a single version", pkg, ver)
	}

	switch pkg.Kind {
	case KindRoot:
		return p.rootDependencies()
	case KindPython:
		// The interpreter is a leaf. Modeling it as a package is about making
		// a Requires-Python conflict explainable, not about giving Python its
		// own dependency graph.
		return nil, nil
	}

	deps, reason, err := p.projectDependencies(pkg, v)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		// Unreachable through the solver: Candidates does this same work and
		// declines to offer any version that has a reason. Reaching it means
		// something asked about a version that was never a candidate, so an
		// error is the honest answer -- returning a partial dependency list
		// would resolve around a requirement that was never expressed.
		return nil, fmt.Errorf("provider: dependencies of %s %s: %s", pkg, v, reason)
	}
	return deps, nil
}

// rootDependencies expands the caller's own requirements, plus the pin that
// makes the target interpreter a fact of the resolution.
//
// Pinning the interpreter here rather than filtering candidates by it behind
// the solver's back is what lets a Requires-Python conflict appear in the
// derivation graph, so the report can say which package demanded which Python.
func (p *Provider) rootDependencies() ([]dependency, error) {
	deps := []dependency{{Package: Python(), Allowed: pep440set.Exactly(p.opts.PythonVersion)}}

	expanded, reason, err := expandRequirements(p.opts.Requirements, p.opts.Environment, nil)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		// The user's OWN request cannot be expressed. Unlike a package's
		// requirement, there is no other version to fall back to, so this
		// aborts the resolve rather than excluding anything.
		return nil, fmt.Errorf("provider: the requested requirements cannot be resolved: %s", reason)
	}
	p.recordExtraRequests(Root(), version.Version{}, expanded)
	return append(deps, expanded...), nil
}

// projectDependencies computes the dependencies of one version of a real
// project, or reports why that version cannot be used.
//
// A non-empty reason means the version must not be offered to the solver: it is
// data the resolution reasons with, not a failure. An error means the index
// itself failed, which is not something the resolution can reason about at all
// -- letting a transport error read as "no such version" would blame the user's
// constraints for an outage.
func (p *Provider) projectDependencies(pkg Package, v version.Version) ([]dependency, string, error) {
	meta, reason, _, err := p.metadata(pkg, v)
	if err != nil {
		return nil, "", err
	}
	if reason != "" {
		return nil, reason, nil
	}

	return p.dependenciesFrom(pkg, v, meta)
}

// metadata reads one version's metadata, translating the index's refusals into a
// recordable reason.
//
// Split out of projectDependencies so usable can test wheel tags against the
// SAME read rather than a second one: MetadataIndex makes no promise of
// memoization, so asking twice can mean decoding twice, and per-candidate is
// exactly the path that cannot afford it.
func (p *Provider) metadata(pkg Package, v version.Version) (index.PackageMetadata, string, UnusableKind, error) {
	meta, err := p.index.Metadata(p.ctx, pkg.Name, v)
	if err != nil {
		switch {
		case errors.Is(err, index.ErrMetadataUnavailable):
			return meta, ReasonMetadataUnavailable, KindMetadataUnavailable, nil
		case errors.Is(err, index.ErrMetadataUnusable):
			return meta, fmt.Sprintf("its metadata cannot be used: %v", err), KindOther, nil
		case errors.Is(err, index.ErrPackageNotFound):
			return meta, "the index does not have this package", KindOther, nil
		}
		return meta, "", KindOther, fmt.Errorf("provider: metadata for %s %s: %w", pkg, v, err)
	}
	return meta, "", KindOther, nil
}

// dependenciesFrom is projectDependencies once the metadata is in hand.
func (p *Provider) dependenciesFrom(
	pkg Package, v version.Version, meta index.PackageMetadata,
) ([]dependency, string, error) {
	var (
		deps   []dependency
		active []string
		reason string
	)
	reqs := meta.RequiresDist

	// pendingUnreadable holds a record that is only true if this version turns
	// out to be offered. See where it is set, below.
	var pendingUnreadable string

	if pkg.Extra == "" {
		var (
			pyDep dependency
			skip  bool
		)
		if meta.RequiresPythonUnreadable {
			// The version is KEPT: the decoder treats an unparseable
			// interpreter constraint as unconstrained on purpose, because
			// over-admitting a candidate surfaces later as an install-time
			// failure while under-constraining would silently change the
			// resolution. That is only defensible if it is visible.
			//
			// ⚠️ Held until the success return rather than recorded here.
			// Anything below can still exclude this version, and an
			// Offered:true written before that decision is a lie the record's
			// own Offered field exists to prevent -- a consumer would report
			// that the version resolved with an unconstrained interpreter when
			// it was never a candidate.
			pendingUnreadable = "its Requires-Python " + strconv.Quote(meta.RequiresPythonRaw) +
				" could not be parsed, so the interpreter is left unconstrained"
		}
		// The interpreter constraint belongs to the base package. An extra
		// reaches it through the same-version link below, so emitting it there
		// too would only duplicate an incompatibility.
		pyDep, skip, reason = interpreterDependency(meta)
		if reason != "" {
			return nil, reason, nil
		}
		if !skip {
			deps = append(deps, pyDep)
		}
	} else {
		// An extra nobody declared is ignored, as pip and uv do, rather than
		// excluding the version. It is reported in Resolution.MissingExtras.
		if slices.Contains(meta.ProvidesExtra, pkg.Extra) {
			active = []string{pkg.Extra}
			reqs = extraOnly(meta.RequiresDist, p.opts.Environment, active)
		} else {
			reqs = nil
			p.recordUndeclaredExtra(pkg.Name, v, pkg.Extra)
		}

		// The same-version link, whether or not the extra is declared. Without
		// it the extra could resolve to a version other than the base package
		// it is an extra OF, and the installed set would be incoherent.
		deps = append(deps, dependency{Package: Project(pkg.Name), Allowed: pep440set.Exactly(v)})
	}

	expanded, reason, err := expandRequirements(reqs, p.opts.Environment, active)
	if err != nil {
		return nil, "", err
	}
	if reason != "" {
		return nil, reason, nil
	}
	p.recordExtraRequests(Project(pkg.Name), v, expanded)

	// Only now is the version definitely offered, so only now is an
	// Offered:true record truthful.
	p.record(pkg, v, pendingUnreadable, KindOther, true)

	return append(deps, expanded...), "", nil
}

// interpreterDependency converts a version's Requires-Python into a dependency
// on the interpreter package.
//
// skip is true when the version constrains the interpreter not at all. An
// unconstrained dependency on Python is noise in the graph and worse noise in
// the report, and an absent Requires-Python is over two million versions in a
// production PyPI snapshot.
func interpreterDependency(meta index.PackageMetadata) (dep dependency, skip bool, reason string) {
	// RequiresPythonUnreadable leaves RequiresPython empty on purpose -- the
	// decoder treats an unparseable interpreter constraint as unconstrained,
	// because over-admitting a candidate surfaces later as an install-time
	// failure while under-constraining would silently change the resolution.
	// The version stays a candidate here for the same reason.
	//
	// Testing String() rather than a length is what index.PackageMetadata's own
	// SupportsPython does: an empty specifier set is a conjunction over no
	// constraints, which admits every interpreter.
	if meta.RequiresPython.String() == "" {
		return dependency{}, true, ""
	}

	set, err := pep440set.FromSpecifiers(meta.RequiresPython)
	if err != nil {
		if errors.Is(err, pep440set.ErrUnrepresentable) {
			return dependency{}, false, fmt.Sprintf(
				"its Requires-Python %q has no version-set equivalent", meta.RequiresPython.String())
		}
		// FromSpecifiers documents ErrUnrepresentable as its only failure, so
		// this is defensive rather than reachable. It is here so a future error
		// kind cannot be silently read as "unrepresentable".
		return dependency{}, false, fmt.Sprintf("its Requires-Python %q could not be converted: %v",
			meta.RequiresPython.String(), err)
	}
	return dependency{Package: Python(), Allowed: set}, false, ""
}

// extraOnly keeps the requirements an extra ADDS: those whose marker holds with
// the extra active and does not hold without it.
//
// # Why the base package's own requirements are not repeated here
//
// A virtual extra package depends on its base at exactly the same version, so
// everything the base requires is already in the graph by the time the extra is
// decided. Restating it under the extra's name would double the dependency
// edges for every extra, and -- the reason that actually matters -- it would
// make the failure report say "flask[async] depends on werkzeug>=3.0" about a
// requirement flask has whether or not the extra is active, naming a package
// the user never wrote.
//
// This is a deliberate narrowing of the design's "every requires_dist entry
// whose marker evaluates true with the extra active", which would include the
// unconditional ones. Both are correct; this one attributes the requirement to
// the package that actually declares it.
func extraOnly(reqs []requirement.Requirement, env marker.Environment, active []string) []requirement.Requirement {
	out := make([]requirement.Requirement, 0, len(reqs))
	for _, r := range reqs {
		if r.Marker.Evaluate(env, active) && !r.Marker.Evaluate(env, nil) {
			out = append(out, r)
		}
	}
	return out
}

// expandRequirements turns PEP 508 requirements into solver dependencies,
// evaluating each marker with the given extras active.
//
// A non-empty reason means the requirement list as a whole cannot be expressed,
// so whatever declared it must not be offered.
func expandRequirements(reqs []requirement.Requirement, env marker.Environment, active []string) ([]dependency, string, error) {
	var deps []dependency

	for _, r := range reqs {
		if !r.Marker.Evaluate(env, active) {
			continue
		}

		// ⚠️ A direct-reference requirement has an empty Specifiers, which
		// reads as "every version". Accepting it would let the resolver pick
		// an index version the publisher never asked for -- a DIFFERENT
		// artifact than the URL names, chosen silently. PyPI rejects direct
		// references in most uploaded metadata, so this is rare; being rare is
		// not a reason to resolve it wrongly.
		if r.URL != "" {
			return nil, fmt.Sprintf(
				"it requires %s from the direct reference %s, which cannot be resolved against an index",
				r.Name, r.URL), nil
		}

		allowed, err := pep440set.FromSpecifiers(r.Specifiers)
		if err != nil {
			if errors.Is(err, pep440set.ErrUnrepresentable) {
				return nil, fmt.Sprintf("its requirement %q has no version-set equivalent", r.String()), nil
			}
			return nil, "", fmt.Errorf("provider: requirement %q: %w", r.String(), err)
		}

		// Requirement.Name is documented as "exactly as parsed"; gpp does not
		// canonicalize it. Requirement.Extras, by contrast, ARE normalized at
		// parse time -- the asymmetry is real, so neither half is assumed from
		// the other. WithExtra normalizes again anyway, which is idempotent.
		name := index.NewPackageName(r.Name)

		// flask[async,dotenv]>=2.0 becomes THREE dependencies over the same
		// allowed set. Depending on the base package as well as on each extra
		// is what keeps the base package's own requirements in the graph: an
		// extra ADDS requirements, it does not replace them.
		deps = append(deps, dependency{Package: Project(name), Allowed: allowed})
		for _, extra := range r.Extras {
			deps = append(deps, dependency{Package: WithExtra(name, extra), Allowed: allowed})
		}
	}

	return deps, "", nil
}

// ExtraRequest is one requester -> package[extra] edge, recorded as
// expandRequirements builds a dependency onto an extra virtual package. It
// says who asked, independent of whether the target version turns out to
// declare the extra.
//
// This is what was ASKED for, not what the resolution settled on: the solver
// can visit this edge on a branch it later backtracks past. resolver.Resolve
// keeps only the edges the final solution still contains.
type ExtraRequest struct {
	// Requester is Root() or Project(name) -- never carries its own Extra,
	// since this identifies WHO asked, not which of their own extras asked.
	Requester Package

	// RequesterVersion is the requester's pinned version. Meaningless when
	// Requester is Root().
	RequesterVersion version.Version

	// Package is the project whose extra was requested.
	Package index.PackageName

	// Extra is the PEP 685-normalized extra requested.
	Extra string
}

// ExtraRequests returns every requester -> package[extra] edge this Provider
// has seen, including ones on a branch the solver later backtracked past.
func (p *Provider) ExtraRequests() []ExtraRequest {
	return p.extraRequests
}

// recordExtraRequests scans deps for edges onto an extra virtual package.
// rootDependencies and dependenciesFrom call this once they have expanded
// their own requirements, because they are the ones who know their requester
// identity -- the solver's Dependencies method is never told who is asking.
func (p *Provider) recordExtraRequests(requester Package, requesterVersion version.Version, deps []dependency) {
	for _, d := range deps {
		if d.Package.Kind != KindProject || d.Package.Extra == "" {
			continue
		}
		p.recordExtraRequest(requester, requesterVersion, d.Package.Name, d.Package.Extra)
	}
}

// recordExtraRequest adds one edge, ignoring a repeat of one already held. See
// Provider.record for why the dedupe key is built from strings.
func (p *Provider) recordExtraRequest(
	requester Package, requesterVersion version.Version, name index.PackageName, extra string,
) {
	key := strings.Join([]string{requester.String(), requesterVersion.String(), string(name), extra}, "\x00")
	if p.extraRequestsSeen[key] {
		return
	}
	p.extraRequestsSeen[key] = true
	p.extraRequests = append(p.extraRequests, ExtraRequest{
		Requester:        requester,
		RequesterVersion: requesterVersion,
		Package:          name,
		Extra:            extra,
	})
}

// UndeclaredExtra records that one project's specific version does not
// declare an extra the resolution asked about.
type UndeclaredExtra struct {
	Package index.PackageName
	Version version.Version
	Extra   string
}

// UndeclaredExtras returns every (package, version, extra) this Provider found
// not declared, including versions the solver later backtracked past.
func (p *Provider) UndeclaredExtras() []UndeclaredExtra {
	return p.undeclaredExtras
}

// recordUndeclaredExtra adds one record, ignoring a repeat of one already
// held.
func (p *Provider) recordUndeclaredExtra(name index.PackageName, v version.Version, extra string) {
	key := strings.Join([]string{string(name), v.String(), extra}, "\x00")
	if p.undeclaredExtrasSeen[key] {
		return
	}
	p.undeclaredExtrasSeen[key] = true
	p.undeclaredExtras = append(p.undeclaredExtras, UndeclaredExtra{Package: name, Version: v, Extra: extra})
}
