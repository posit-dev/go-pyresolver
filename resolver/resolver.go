// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"fmt"
	"slices"

	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// defaultMaxRounds bounds the solver's main loop when Options.MaxRounds is zero.
//
// It is a safety valve rather than a tuning knob. go-pubgrub documents that
// termination of the OUTER loop is asserted rather than derived, and offers
// MaxRounds as the place to put a bound when the input cannot be trusted.
// requires_dist is exactly that: arbitrary text published by third parties.
// A resolution that hits this bound fails loudly instead of hanging.
//
// Unexported: this package's supported surface is Resolve, Options, Resolution
// and ResolutionError, and a consumer that wants a specific bound sets one.
const defaultMaxRounds = 10_000

// Options configures one resolution.
type Options struct {
	// Environment is the single concrete marker environment every PEP 508
	// marker is evaluated in. RFD 0001 defers universal (environment-
	// independent) resolution, so this is one target, not a set. Required.
	//
	// Build it with marker.EnvironmentFromTarget rather than as a struct
	// literal: a literal zero-fills the fields it omits, which turns
	// python_version into "" and silently flips the answer of any marker that
	// mentions it. Resolve refuses an Environment carrying no
	// python_full_version for that reason.
	Environment marker.Environment

	// PythonVersion is the interpreter the resolution targets. Required, and it
	// MUST agree with Environment's python_full_version.
	//
	// It is carried separately because the interpreter-as-package model needs a
	// parsed version.Version while Environment holds marker variables as
	// strings. Two sources of truth for the interpreter is how a resolution
	// silently targets one Python while evaluating markers for another, so
	// Resolve compares them before doing anything else.
	PythonVersion version.Version

	// Policy orders the admissible versions of a package, deciding which one
	// the solver tries first. Nil means candidate.Newest.
	//
	// A Policy ranks and never filters: it cannot make a version unavailable.
	// See candidate.Rank.
	Policy candidate.Policy

	// AllowPrerelease names packages whose pre-release versions may be offered
	// even though no requirement asked for one. Names must already be
	// canonical (build them with index.NewPackageName).
	//
	// A package whose own requirement names a pre-release -- ">=2.0rc1" -- is
	// enabled without being listed here.
	AllowPrerelease []index.PackageName

	// MaxRounds bounds the solver's main loop. Zero means 10,000.
	//
	// It is a safety valve, not a tuning knob: go-pubgrub documents that
	// termination of the OUTER loop is asserted rather than derived, and
	// requires_dist is arbitrary text published by third parties. A resolution
	// that hits the bound fails loudly instead of hanging.
	MaxRounds int
}

// Resolution is a successful resolution: one version chosen for every package
// the requirements reach.
type Resolution struct {
	// Pinned maps each resolved project to its chosen version.
	//
	// It is keyed by project name, not by solver package: a caller wants
	// "flask 3.0", not "flask 3.0 alongside flask[async] 3.0". The interpreter
	// is absent because it is an input to the resolution rather than a result
	// of it, and so is the synthetic root.
	Pinned map[index.PackageName]version.Version

	// Order lists the projects in the order the solver decided them, each one
	// exactly once, and holds every key of Pinned.
	//
	// It is a slice rather than a map on purpose: it is the only part of a
	// Resolution that carries an order, and an order read out of a map differs
	// run to run.
	Order []index.PackageName

	// Extras records which extras of a project the resolution activated, sorted
	// and deduplicated. A project resolved without extras has no entry.
	//
	// The extras' own requirements are already in Pinned -- this says which
	// optional feature sets pulled them in, which is what a caller needs to
	// reproduce the same install.
	Extras map[index.PackageName][]string
}

// rootVersion is the synthetic version of the root package. Nothing in a
// resolution depends on what it is; it exists because the solver decides a
// version for every package including the root.
var rootVersion = version.MustParse("0")

// Resolve chooses one version of every package reqs transitively requires.
//
// A failure that is a genuine conflict between requirements comes back as a
// *ResolutionError carrying an explanation built from the solver's derivation
// graph. Any other error -- an index that could not answer, a cancelled
// context, options that do not describe a single interpreter -- comes back as
// itself, because presenting an outage as "your requirements conflict" sends
// the caller looking for a problem that is not there.
func Resolve(
	ctx context.Context,
	reqs []requirement.Requirement,
	idx index.MetadataIndex,
	opts Options,
) (*Resolution, error) {
	if err := validate(opts); err != nil {
		return nil, err
	}

	p := provider.New(ctx, idx, provider.Options{
		Environment:   opts.Environment,
		PythonVersion: opts.PythonVersion,
		Policy:        opts.Policy,
		Prereleases:   candidate.EnabledPrereleases(reqs, opts.AllowPrerelease),
		Requirements:  reqs,
		RootVersion:   rootVersion,
	})

	s := solver.New(provider.Root(), pep440set.Exactly(rootVersion), p)
	s.MaxRounds = opts.MaxRounds
	if s.MaxRounds == 0 {
		s.MaxRounds = defaultMaxRounds
	}

	sol, err := s.Solve()
	if err != nil {
		return nil, explain(err, p.Unusable())
	}
	return collapse(sol)
}

// validate checks that the options describe ONE interpreter.
//
// This runs before the index is touched. A mismatch caught after a solve would
// have spent the whole resolution -- and every index call it provokes --
// answering the wrong question, and a mismatch never caught at all produces a
// resolution pinned for one Python whose markers were evaluated for another.
// That failure is invisible: it resolves, it installs, and it breaks at import
// time.
func validate(opts Options) error {
	raw := opts.Environment.PythonFullVersion
	if raw == "" {
		return fmt.Errorf(
			"resolver: Options.Environment has no python_full_version, so it cannot be " +
				"checked against Options.PythonVersion; build it with marker.EnvironmentFromTarget " +
				"rather than as a struct literal")
	}
	envVersion, err := version.Parse(raw)
	if err != nil {
		return fmt.Errorf("resolver: Options.Environment python_full_version %q: %w", raw, err)
	}
	// PEP 440 equality, not string equality: 3.11.4 and 3.11.4.0 are the same
	// interpreter, and refusing that pair would reject a correct caller.
	if !envVersion.Equal(opts.PythonVersion) {
		return fmt.Errorf(
			"resolver: Options.PythonVersion is %s but Options.Environment's python_full_version "+
				"is %s; a resolution targeting one interpreter while evaluating markers for "+
				"another is wrong in a way nothing later reports",
			opts.PythonVersion, envVersion)
	}
	return nil
}

// collapse turns a solver solution into Python terms: virtual extra packages
// fold into their base project, and the synthetic root and the interpreter drop
// out entirely.
func collapse(sol *solver.Solution[provider.Package, pep440set.Set]) (*Resolution, error) {
	res := &Resolution{
		Pinned: make(map[index.PackageName]version.Version, len(sol.Selected)),
		Extras: make(map[index.PackageName][]string),
	}

	// Pinned comes from Selected, which is the answer; Order comes from Order,
	// which is only the path taken to it. Reading versions out of Order instead
	// would silently drop any package the two disagree about.
	for pkg, set := range sol.Selected {
		if pkg.Kind != provider.KindProject {
			continue
		}
		v, ok := set.Singleton()
		if !ok {
			// The solver decides exactly one version per package, so this
			// cannot happen -- but a Resolution promising a pin it does not
			// have would be discovered much later, by whoever installs it.
			return nil, fmt.Errorf(
				"resolver: %s was decided as %v, which is not a single version", pkg, set)
		}
		if prior, seen := res.Pinned[pkg.Name]; seen && !prior.Equal(v) {
			// The same-version link between an extra and its base is what makes
			// this impossible. If it ever fires, the extras model is broken and
			// the closure that comes out of it cannot be trusted.
			return nil, fmt.Errorf(
				"resolver: %s resolved to both %s and %s", pkg.Name, prior, v)
		}
		res.Pinned[pkg.Name] = v
		if pkg.Extra != "" && !slices.Contains(res.Extras[pkg.Name], pkg.Extra) {
			res.Extras[pkg.Name] = append(res.Extras[pkg.Name], pkg.Extra)
		}
	}

	// Selected is a map, so its iteration order is randomized per range. Sorting
	// each extras list is what keeps two runs over the same inputs producing the
	// same output.
	for name := range res.Extras {
		slices.Sort(res.Extras[name])
	}

	seen := make(map[index.PackageName]bool, len(res.Pinned))
	for _, pkg := range sol.Order {
		if pkg.Kind != provider.KindProject || seen[pkg.Name] {
			continue
		}
		seen[pkg.Name] = true
		res.Order = append(res.Order, pkg.Name)
	}
	// Order is documented to hold every key of Pinned. It does, because a
	// package cannot be selected without being decided -- but appending in
	// sorted order rather than trusting that keeps a package visible instead of
	// invisible if it ever stops holding.
	var missing []index.PackageName
	for name := range res.Pinned {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	res.Order = append(res.Order, missing...)

	return res, nil
}
