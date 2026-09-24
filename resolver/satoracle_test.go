// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/crillab/gophersat/bf"
	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/extras"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

// oracleKnownFail lists scenarios where the oracle and the resolver
// deliberately disagree, on top of runPackseScenario's own knownFail. Both
// entries here implement something the resolver's matching knownFail entry
// says the resolver does not: the PEP 592 exact-pin exception for a yanked
// version. The oracle computes yanked admissibility itself (see
// exactPinAdmitsYanked), correctly this time, so it agrees with packse and
// disagrees with the resolver.
var oracleKnownFail = map[string]string{
	"yanked/transitive-package-only-yanked-in-range-opt-in": "oracle implements PEP 592's exact-pin " +
		"exception for a yanked version; the resolver does not (see its own knownFail entry)",
	"yanked/transitive-yanked-and-unyanked-dependency-opt-in": "oracle implements PEP 592's exact-pin " +
		"exception for a yanked version; the resolver does not (see its own knownFail entry)",
}

// oracleVersion is what the CNF encoder needs about one published version,
// derived entirely from scenario data (never from provider or candidate
// output -- see the package doc comment on this file's neighbors for why).
type oracleVersion struct {
	str        string
	parsed     version.Version
	admissible bool
	provides   map[string]bool
}

// exactPinAdmitsYanked reports whether some root requirement pins pkgName to
// exactly v, which is PEP 592's exception to "never offer a yanked release":
// an installer MAY still honor an explicit, exact request for it. Checked by
// parsing each root requirement rather than a general specifier-shape
// analysis of Specifiers -- sufficient for the vendored corpus (measured: both
// scenarios that need this write it as "name==x.y.z"), and simpler than
// reconstructing "is this specifier exactly one version" from a parsed
// Specifiers. Matching on the parsed requirement name (not a substring of the
// raw text) avoids a false positive from a prefixed name, e.g. "bb==1.0.0"
// containing the text "b==1.0.0".
func exactPinAdmitsYanked(root tomlRoot, pkgName string, v version.Version) bool {
	needle := "==" + v.String()
	for _, raw := range root.Requires {
		req, err := requirement.Parse(raw)
		if err != nil || req.Name != pkgName {
			continue
		}
		if req.Specifiers.String() == needle {
			return true
		}
	}
	return false
}

// oracleAdmissible computes, independently of provider/candidate, whether v
// may be offered at all: Requires-Python, pre-release admission (sharing
// candidate.PrereleaseSet -- the one deliberate exception named in this
// package's mutation-proof and PR-body notes), and wheel/sdist availability.
func oracleAdmissible(
	v tomlVersion, parsed version.Version, py pythonSpec, prereleases candidate.PrereleaseSet, pkgName string, matcher *tags.Matcher,
) bool {
	reqPy := requiresPythonOf(v)
	if reqPy != "" {
		specs, err := version.NewSpecifiers(reqPy)
		if err == nil && !specs.Check(version.MustParse(py.full)) {
			return false
		}
	}
	if !prereleases.Admits(index.NewPackageName(pkgName), parsed) {
		return false
	}

	facts := versionDistFacts(v)
	hasCompatibleWheel := false
	for _, raw := range facts.wheelTags {
		parsedTag, err := tags.ParseTag(raw)
		if err != nil {
			continue
		}
		if matcher.IsCompatible(parsedTag) {
			hasCompatibleWheel = true
			break
		}
	}
	if !hasCompatibleWheel && !facts.hasSdist {
		return false
	}
	return true
}

// atMostOne encodes "at most one of vars is true" as pairwise exclusions.
// packse's version counts per package are small, so the quadratic clause
// count is not a concern.
func atMostOne(vars []string) bf.Formula {
	var clauses []bf.Formula
	for i := 0; i < len(vars); i++ {
		for j := i + 1; j < len(vars); j++ {
			clauses = append(clauses, bf.Or(bf.Not(bf.Var(vars[i])), bf.Not(bf.Var(vars[j]))))
		}
	}
	if len(clauses) == 0 {
		// ⚠️ Fewer than 2 versions: no pair to exclude. Must be the explicit
		// bf.True constant, NOT bf.And() with zero arguments -- gophersat's own
		// nnf() simplification maps an all-true/empty conjunction to False (the
		// opposite of the vacuous truth this represents), which silently turns a
		// satisfiable scenario UNSAT under bf.Solve while direct Eval still
		// (correctly) reads it as true. Measured against gophersat v1.4.0.
		return bf.True
	}
	return bf.And(clauses...)
}

// oracleModel is the CNF built for one scenario, plus what's needed to
// evaluate the resolver's own answer against it directly.
//
// vars names every variable the formula mentions. bf.Formula.Eval panics on a
// model with no binding for a variable it reaches, so resolverModel must
// supply an explicit false for everything the resolver did not pin -- vars is
// what makes that possible.
type oracleModel struct {
	formula bf.Formula
	vars    []string
}

func projVar(pkg, ver string) string         { return pkg + "@" + ver }
func extraVar(pkg, extra, ver string) string { return pkg + "[" + extra + "]@" + ver }

// buildOracleModel encodes s as CNF. One variable per (package, version) plus
// one per (package[extra], version); at-most-one per project; the root
// requirements; and implications of the form x(p,v) -> OR(admissible
// versions of each dependency). Extras are modeled the way go-pyresolver
// models them (a version must itself declare an extra for a request naming it
// to admit that version) so the oracle agrees with the resolver on the
// extras/ knownFail scenarios rather than adding a second, unrelated
// disagreement.
func buildOracleModel(t *testing.T, s tomlScenario, py pythonSpec, env marker.Environment, matcher *tags.Matcher) oracleModel {
	t.Helper()

	prereleases := candidate.EnabledPrereleases(mustRequirements(t, s.Root.Requires...), oraclePrereleaseAllow(s))

	versions := make(map[string][]oracleVersion, len(s.Packages))
	for pkgName, pkg := range s.Packages {
		name := index.NewPackageName(pkgName).String()
		for verStr, v := range pkg.Versions {
			parsed, err := version.Parse(verStr)
			if err != nil {
				t.Fatalf("oracle %s: %s %s: bad version: %v", s.Name, pkgName, verStr, err)
			}
			admissible := oracleAdmissible(v, parsed, py, prereleases, name, matcher) &&
				(!v.Yanked || exactPinAdmitsYanked(s.Root, pkgName, parsed))
			provides := make(map[string]bool, len(v.Extras))
			for e := range v.Extras {
				provides[extras.Normalize(e)] = true
			}
			versions[name] = append(versions[name], oracleVersion{str: verStr, parsed: parsed, admissible: admissible, provides: provides})
		}
	}

	requirementFormula := func(r requirement.Requirement) bf.Formula {
		if !r.Marker.Evaluate(env, nil) {
			return bf.True // marker false: this edge does not apply, vacuously true
		}
		name := index.NewPackageName(r.Name).String()
		var terms []bf.Formula
		for _, vi := range versions[name] {
			if !vi.admissible {
				continue
			}
			if r.Specifiers.String() != "" && !r.Specifiers.Check(vi.parsed) {
				continue
			}
			ok := true
			for _, e := range r.Extras {
				if !vi.provides[e] {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			term := []bf.Formula{bf.Var(projVar(name, vi.str))}
			for _, e := range r.Extras {
				term = append(term, bf.Var(extraVar(name, e, vi.str)))
			}
			terms = append(terms, bf.And(term...))
		}
		if len(terms) == 0 {
			// No admissible candidate satisfies this requirement at all: the
			// requirement is unsatisfiable. Must be the explicit bf.False
			// constant, not bf.Or() with zero arguments -- see atMostOne's
			// comment; gophersat's nnf() maps an empty disjunction to True, the
			// opposite of what an unsatisfiable requirement needs.
			return bf.False
		}
		return bf.Or(terms...)
	}

	clauses := make([]bf.Formula, 0, len(s.Root.Requires)+len(versions)*2)
	for _, raw := range s.Root.Requires {
		req, err := requirement.Parse(raw)
		if err != nil {
			t.Fatalf("oracle %s: root requirement %q: %v", s.Name, raw, err)
		}
		clauses = append(clauses, requirementFormula(req))
	}

	// Sort package names so clause order (and any solver tie-breaking) does
	// not depend on Go's randomized map iteration.
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)

	var allVars []string

	for _, name := range names {
		vis := versions[name]
		sort.Slice(vis, func(i, j int) bool { return vis[i].str < vis[j].str })

		varNames := make([]string, len(vis))
		for i, vi := range vis {
			varNames[i] = projVar(name, vi.str)
		}
		allVars = append(allVars, varNames...)
		clauses = append(clauses, atMostOne(varNames))

		for _, vi := range vis {
			if !vi.admissible {
				continue
			}
			pkgName := originalPackageName(s.Packages, name)
			v := s.Packages[pkgName].Versions[vi.str]

			var deps []bf.Formula
			for _, raw := range v.Requires {
				req, err := requirement.Parse(raw)
				if err != nil {
					t.Fatalf("oracle %s: %s %s requirement %q: %v", s.Name, name, vi.str, raw, err)
				}
				deps = append(deps, requirementFormula(req))
			}
			if len(deps) > 0 {
				clauses = append(clauses, bf.Implies(bf.Var(projVar(name, vi.str)), bf.And(deps...)))
			}

			extraNames := make([]string, 0, len(v.Extras))
			for e := range v.Extras {
				extraNames = append(extraNames, e)
			}
			sort.Strings(extraNames)
			for _, e := range extraNames {
				reqs := v.Extras[e]
				canonExtra := extras.Normalize(e)
				edeps := []bf.Formula{bf.Var(projVar(name, vi.str))}
				for _, raw := range reqs {
					req, err := requirement.Parse(raw)
					if err != nil {
						t.Fatalf("oracle %s: %s %s extra %q requirement %q: %v", s.Name, name, vi.str, e, raw, err)
					}
					edeps = append(edeps, requirementFormula(req))
				}
				clauses = append(clauses, bf.Implies(bf.Var(extraVar(name, canonExtra, vi.str)), bf.And(edeps...)))
				allVars = append(allVars, extraVar(name, canonExtra, vi.str))
			}
		}
	}

	if len(clauses) == 0 {
		// No root requirements and no packages: trivially satisfiable. Must be
		// bf.True, not bf.And() with zero arguments -- see atMostOne's comment.
		// Unreachable against the vendored corpus (every scenario has a non-empty
		// root.requires), kept as a guard against a future re-pull that adds one.
		return oracleModel{formula: bf.True, vars: allVars}
	}
	return oracleModel{formula: bf.And(clauses...), vars: allVars}
}

// originalPackageName finds the raw TOML key for a canonicalized name --
// needed because s.Packages is keyed by the un-normalized name packse wrote,
// but the CNF is built over canonical names to line up with
// resolver.Resolution.Pinned.
func originalPackageName(pkgs map[string]tomlPackage, canonical string) string {
	for raw := range pkgs {
		if index.NewPackageName(raw).String() == canonical {
			return raw
		}
	}
	return canonical
}

// oraclePrereleaseAllow mirrors runPackseScenario's resolver_options.prereleases
// emulation, so the oracle and the resolver share the same admission input.
func oraclePrereleaseAllow(s tomlScenario) []index.PackageName {
	if !s.ResolverOptions.Prereleases {
		return nil
	}
	return allPackageNames(s.Packages)
}

// resolverModel builds the variable assignment the CNF sees for a resolved
// Resolution: true for each pinned (package, version) and each active
// (package, extra) at that version, false for every other variable the model
// mentions (bf.Formula.Eval panics on an unbound one).
func resolverModel(vars []string, res *resolver.Resolution) map[string]bool {
	model := make(map[string]bool, len(vars))
	for _, name := range vars {
		model[name] = false
	}
	for name, v := range res.Pinned {
		model[projVar(name.String(), v.String())] = true
	}
	for name, extraList := range res.Extras {
		v, ok := res.Pinned[name]
		if !ok {
			continue
		}
		for _, e := range extraList {
			model[extraVar(name.String(), e, v.String())] = true
		}
	}
	return model
}

// oracleRow is one line of the three-way disagreement table the PR body
// quotes.
type oracleRow struct {
	name, packse, resolverAns, oracleAns string
}

// TestSATOracle cross-checks resolver.Resolve against an independent CNF
// encoding of the same scenario, built from packse's own scenario data (see
// buildOracleModel). Two directions, per scenario:
//
//   - The resolver resolves => its pins satisfy the CNF (checked by
//     evaluating the model directly, never by calling gophersat).
//   - The resolver says unsatisfiable => gophersat also returns UNSAT.
//
// A disagreement fails the case unless the scenario is on oracleKnownFail.
func TestSATOracle(t *testing.T) {
	scenarios := loadPackseScenarios(t)
	if len(scenarios) == 0 {
		t.Fatal("no scenarios loaded -- testdata/packse is missing or empty")
	}

	var rows []oracleRow
	var checked int

	for _, ps := range scenarios {
		name := ps.relName
		s := ps.scenario
		if s.ResolverOptions.Universal {
			continue // outOfScope, per TestPackse
		}
		if _, ok := unsupportedOption[name]; ok {
			continue
		}
		if len(s.ResolverOptions.NoBuild) > 0 || len(s.ResolverOptions.NoBinary) > 0 {
			continue
		}
		if s.ResolverOptions.PythonPlatform != nil {
			if _, _, ok := packsePlatform(*s.ResolverOptions.PythonPlatform); !ok {
				continue
			}
		}
		checked++

		t.Run(name, func(t *testing.T) {
			py, err := scenarioPython(s)
			if err != nil {
				t.Fatalf("build python target: %v", err)
			}
			target, err := buildTarget(py, s.ResolverOptions.PythonPlatform)
			if err != nil {
				t.Fatalf("build tags target: %v", err)
			}
			env, err := buildEnvironment(target, py)
			if err != nil {
				t.Fatalf("build marker environment: %v", err)
			}
			matcher, err := target.Compile()
			if err != nil {
				t.Fatalf("compile target: %v", err)
			}
			filter, err := wheelTagFilter(target, fmt.Sprintf("cp%d%d on %s/%s", py.major, py.minor, target.OS, target.Arch))
			if err != nil {
				t.Fatalf("build wheel tag filter: %v", err)
			}

			idx := index.NewFilteredIndex(buildMockIndex(t, s.Name, s.Packages), index.FilterPolicy{ExcludeYanked: true})
			opts := resolver.Options{Environment: env, PythonVersion: version.MustParse(py.full), WheelTags: filter}
			if s.ResolverOptions.Prereleases {
				opts.AllowPrerelease = allPackageNames(s.Packages)
			}

			reqs := mustRequirements(t, s.Root.Requires...)
			res, resolveErr := resolver.Resolve(context.Background(), reqs, idx, opts)

			model := buildOracleModel(t, s, py, env, matcher)
			oracleAssignment := bf.Solve(model.formula)
			oracleSAT := oracleAssignment != nil

			var re *resolver.ResolutionError
			resolverSAT := resolveErr == nil
			resolverIsConflict := errors.As(resolveErr, &re)

			row := oracleRow{
				name:        name,
				packse:      satLabel(s.Expected.Satisfiable),
				resolverAns: satLabel(resolverSAT),
				oracleAns:   satLabel(oracleSAT),
			}
			if row.packse != row.resolverAns || row.packse != row.oracleAns || row.resolverAns != row.oracleAns {
				rows = append(rows, row)
			}

			switch {
			case resolverSAT:
				// Direction 1: the resolver's own pins must satisfy the CNF.
				if !model.formula.Eval(resolverModel(model.vars, res)) {
					t.Errorf("%s: resolver resolved to %v, but that assignment does not satisfy the oracle's CNF", name, pins(t, res))
				}

			case resolverIsConflict:
				// Direction 2: the resolver's unsatisfiable must agree with gophersat.
				reason, known := oracleKnownFail[name]
				switch {
				case known && oracleSAT:
					// Expected disagreement; nothing further to assert.
				case known && !oracleSAT:
					t.Errorf("%s unexpectedly agreed with the resolver, remove it from oracleKnownFail (reason was: %s)", name, reason)
				case !known && oracleSAT:
					t.Errorf("%s: resolver says unsatisfiable, but the oracle found a model: %v", name, oracleAssignment)
				}

			default:
				// A harness-level error (not a ResolutionError): the oracle has
				// nothing to cross-check against a failure that is not the
				// resolver's own answer.
				t.Fatalf("%s: Resolve failed with a non-ResolutionError: %v", name, resolveErr)
			}
		})
	}

	t.Logf("SAT oracle: cross-checked %d scenarios, %d disagreement(s)", checked, len(rows))
	if len(rows) > 0 {
		var b strings.Builder
		b.WriteString("scenario | packse | resolver | oracle\n")
		b.WriteString("---|---|---|---\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "%s | %s | %s | %s\n", r.name, r.packse, r.resolverAns, r.oracleAns)
		}
		t.Log(b.String())
	}
}

func satLabel(satisfiable bool) string {
	if satisfiable {
		return "sat"
	}
	return "unsat"
}
