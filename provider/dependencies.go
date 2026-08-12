// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"errors"
	"fmt"

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
	meta, err := p.index.Metadata(p.ctx, pkg.Name, v)
	if err != nil {
		switch {
		case errors.Is(err, index.ErrMetadataUnavailable):
			return nil, "no dependency metadata is published for it (an sdist-only or dynamic-metadata release)", nil
		case errors.Is(err, index.ErrMetadataUnusable):
			return nil, fmt.Sprintf("its metadata cannot be used: %v", err), nil
		case errors.Is(err, index.ErrPackageNotFound):
			return nil, "the index does not have this package", nil
		}
		return nil, "", fmt.Errorf("provider: metadata for %s %s: %w", pkg, v, err)
	}

	var deps []dependency

	pyDep, skip, reason := interpreterDependency(meta)
	if reason != "" {
		return nil, reason, nil
	}
	if !skip {
		deps = append(deps, pyDep)
	}

	expanded, reason, err := expandRequirements(meta.RequiresDist, p.opts.Environment, nil)
	if err != nil {
		return nil, "", err
	}
	if reason != "" {
		return nil, reason, nil
	}
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
		// the other.
		deps = append(deps, dependency{Package: Project(index.NewPackageName(r.Name)), Allowed: allowed})
	}

	return deps, "", nil
}
