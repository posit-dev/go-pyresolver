// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
// Unexported: this package's supported surface is Resolve, Options, Resolution,
// ResolutionError, MissingExtra and Requester, and a consumer that wants a
// specific bound sets one.
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

	// AllowPrerelease names packages whose pre-releases are ranked alongside
	// their final releases, rather than only after every final release in
	// range. Names must already be canonical (build them with
	// index.NewPackageName).
	//
	// Every package's pre-releases are still offered as a fallback when no
	// final release in range is usable, whether or not it is named here --
	// see candidate.PrereleaseSet. This only affects a package where a final
	// IS usable: named here, its newest pre-release can still beat an older
	// final; not named, the final wins regardless of version order.
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

	// WheelTags rejects versions that publish nothing installable on the target:
	// wheels that cannot run here, and no source distribution to fall back to.
	// Nil leaves tag filtering off.
	//
	// ⚠️ Two things silently leave it off even when set, and both are correct.
	// A filter with no Matcher filters nothing, and so does an index whose wheel
	// tag data is incomplete -- on a partially derived corpus, "no compatible tag"
	// cannot be told apart from "tags never derived", so filtering would reject
	// installable packages. As of 2026-09 the production PyPI snapshot is in that
	// state.
	//
	// ⚠️ Compile ONE filter per environment cell. Resolving several targets
	// against one shared index is the intended use, and a filter reused across
	// cells answers the first cell's question under the second cell's name.
	WheelTags *provider.WheelTagFilter
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

	// Unusable holds what this resolution recorded about the versions it
	// examined, in the order first encountered. It is empty when there was
	// nothing to record.
	//
	// It exists because a resolution can SUCCEED by passing over the release the
	// caller meant. Under the default newest-first policy, a project whose
	// newest release publishes no usable metadata resolves to an older one, and
	// without this field that is indistinguishable from the older one being
	// newest. A caller that wants to fail rather than silently downgrade -- or
	// to say why it downgraded -- has nothing else to read.
	//
	// # An entry is not proof a version was rejected
	//
	// Offered distinguishes "accepted, with a note" from "passed over". An
	// Offered:true record happens on ordinary successes -- an unreadable
	// Requires-Python on the very version that gets pinned -- so
	// len(Unusable) != 0 is NOT "something was set aside". The predicate for a
	// release genuinely set aside for missing metadata is
	//
	//	!u.Offered && u.Kind == provider.KindMetadataUnavailable
	//
	// ⚠️ Read Kind, not Reason. This predicate used to be written against
	// provider.ReasonMetadataUnavailable, and it still works for that one
	// category -- but a reason that carries per-version detail, as a wheel-tag
	// rejection does, matches no constant, so the string form silently reports
	// nothing for it. For "set aside for any reason at all", use
	// !u.Offered alone; for "for a reason worth showing a user",
	// !u.Offered && u.Kind.Reportable().
	//
	// ⚠️ Dedupe on (Package.Name, Version) as well. The provider's dedupe key is
	// the SOLVER package, and an extra is a separate solver package for the same
	// project, so flask and flask[async] each record flask 3.0. Unlike
	// ResolutionError.Error, which dedupes before rendering, this field hands you
	// both. Key on Package.Name, not Package.String(): the latter renders an
	// extra as "flask[async]".
	//
	// # It is unfiltered, and it is not exhaustive
	//
	// Candidate selection stops TESTING at the first usable version in RANKED
	// order, so anything ranked below the chosen version is never examined and
	// cannot appear here. Nor can a version the requirement's range or the
	// pre-release policy excluded, since both are checked before usability is.
	// This is what the resolution encountered on its way to this answer, not
	// everything wrong with these packages.
	//
	// ⚠️ Ranked order is not version order. Under the default policy the two
	// coincide, so a newer release passed over for an older one IS reported --
	// which is the case this field is for. A non-default Options.Policy moves the
	// gap: a newer release demoted below the winner is passed over without being
	// examined, and so without being reported. If you rely on this field to
	// detect a downgrade, either leave Policy nil or account for the ordering you
	// imposed.
	Unusable []provider.Unusable

	// MissingExtras lists each requested package[extra] whose pinned version
	// does not declare the extra. go-pyresolver ignores it rather than
	// excluding that version, matching pip and uv, and this is the warning a
	// caller shows for it.
	//
	// It describes the FINAL solution only: an extra requested on a branch the
	// solver later backtracked past is not reported, the same way Extras never
	// names a virtual package that was not ultimately selected.
	//
	// One entry per (requester, package, extra), sorted by (package, extra,
	// requester).
	MissingExtras []MissingExtra
}

// MissingExtra is a requested extra that the pinned version does not declare,
// so it was ignored (pip and uv do the same).
type MissingExtra struct {
	// Package is the project whose extra is missing.
	Package index.PackageName

	// Version is Package's pinned version -- the one that does not declare
	// Extra.
	Version version.Version

	// Extra is the PEP 685-normalized extra that was requested.
	Extra string

	// RequestedBy is who asked for Package[Extra].
	RequestedBy Requester
}

// Requester is the root (the caller's own requirements) or one pinned
// package.
type Requester struct {
	// Root is true when the caller's own requirements asked for the extra.
	Root bool

	// Package is empty when Root.
	Package index.PackageName

	// Version is the zero value when Root.
	Version version.Version
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
		WheelTags:     opts.WheelTags,
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
	res, err := collapse(sol)
	if err != nil {
		return nil, err
	}
	// Read from the same provider the failure path reads, so a release set aside
	// is reported identically whether the resolution went on to succeed or not.
	res.Unusable = p.Unusable()
	filterUndeclaredExtras(res, p.UndeclaredExtras())
	res.MissingExtras = missingExtras(res, p.ExtraRequests(), p.UndeclaredExtras())
	return res, nil
}

// undeclaredKey identifies one (package, version, extra) triple so
// filterUndeclaredExtras and missingExtras can both test membership in the
// provider's undeclared-extra set without version.Version's Equal, which
// cannot key a map.
func undeclaredKey(name index.PackageName, v version.Version, extra string) string {
	return name.String() + "\x00" + v.String() + "\x00" + extra
}

// filterUndeclaredExtras removes an extra from res.Extras when the pinned
// version does not declare it. Resolution.Extras documents itself as "what a
// caller needs to reproduce the same install", and a version that never
// declared the extra cannot be reproduced by asking for it.
func filterUndeclaredExtras(res *Resolution, undeclared []provider.UndeclaredExtra) {
	if len(undeclared) == 0 {
		return
	}
	bad := make(map[string]bool, len(undeclared))
	for _, u := range undeclared {
		bad[undeclaredKey(u.Package, u.Version, u.Extra)] = true
	}
	for name, list := range res.Extras {
		v, ok := res.Pinned[name]
		if !ok {
			continue
		}
		kept := list[:0]
		for _, e := range list {
			if !bad[undeclaredKey(name, v, e)] {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(res.Extras, name)
		} else {
			res.Extras[name] = kept
		}
	}
}

// requesterKey renders a Requester so missingExtras can sort and dedupe on it
// without version.Version's Equal, which cannot key a map or a sort compare
// directly.
func requesterKey(r Requester) string {
	if r.Root {
		return ""
	}
	return r.Package.String() + "@" + r.Version.String()
}

// missingExtras turns the provider's raw requester -> package[extra] edges
// into MissingExtra values, keeping only what the FINAL solution still
// contains: a requester that is pinned (Root always is) at the recorded
// version, and a target whose pinned version does not declare the extra.
//
// The provider's edges include ones from a branch the solver later
// backtracked past -- see the trap on warnings from versions the solver left
// behind. Filtering against res.Pinned, rather than trusting the edge's own
// recorded facts, is what excludes them.
func missingExtras(
	res *Resolution, requests []provider.ExtraRequest, undeclared []provider.UndeclaredExtra,
) []MissingExtra {
	bad := make(map[string]bool, len(undeclared))
	for _, u := range undeclared {
		bad[undeclaredKey(u.Package, u.Version, u.Extra)] = true
	}

	var out []MissingExtra
	for _, req := range requests {
		var requester Requester
		var requesterPinned bool
		switch req.Requester.Kind {
		case provider.KindRoot:
			requester = Requester{Root: true}
			requesterPinned = true
		case provider.KindProject:
			requester = Requester{Package: req.Requester.Name, Version: req.RequesterVersion}
			v, ok := res.Pinned[req.Requester.Name]
			requesterPinned = ok && v.Equal(req.RequesterVersion)
		default:
			continue // the interpreter never requests an extra
		}
		if !requesterPinned {
			continue
		}

		v, ok := res.Pinned[req.Package]
		if !ok || !bad[undeclaredKey(req.Package, v, req.Extra)] {
			continue
		}
		out = append(out, MissingExtra{
			Package:     req.Package,
			Version:     v,
			Extra:       req.Extra,
			RequestedBy: requester,
		})
	}

	slices.SortFunc(out, func(a, b MissingExtra) int {
		if c := strings.Compare(a.Package.String(), b.Package.String()); c != 0 {
			return c
		}
		if c := strings.Compare(a.Extra, b.Extra); c != 0 {
			return c
		}
		return strings.Compare(requesterKey(a.RequestedBy), requesterKey(b.RequestedBy))
	})
	return slices.CompactFunc(out, func(a, b MissingExtra) bool {
		return a.Package == b.Package && a.Extra == b.Extra && a.Version.Equal(b.Version) &&
			a.RequestedBy.Root == b.RequestedBy.Root &&
			a.RequestedBy.Package == b.RequestedBy.Package &&
			a.RequestedBy.Version.Equal(b.RequestedBy.Version)
	})
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
