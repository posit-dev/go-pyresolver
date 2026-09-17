// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"fmt"
	"strings"

	"github.com/posit-dev/go-pubgrub/report"
	"github.com/posit-dev/go-pubgrub/solver"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// ResolutionError reports that the requirements themselves cannot be satisfied,
// and carries the proof.
//
// It is returned only for a genuine conflict. An index that could not answer, a
// cancelled context, or options that do not describe a single interpreter come
// back as themselves, because presenting an outage as "your requirements
// conflict" sends the caller looking for a problem that is not there.
type ResolutionError struct {
	// Report is the explanation, one sentence per Line, in reading order.
	//
	// Read the Lines rather than parsing Error's text: each carries the
	// incompatibility it states, so the packages and version ranges behind a
	// sentence are reachable without re-walking the derivation graph -- which
	// is the hard part, and is already done.
	Report *report.Report[provider.Package, pep440set.Set]

	// Unusable holds the versions the resolution set aside and why, in the order
	// first encountered.
	//
	// It is UNFILTERED but not exhaustive, and the difference matters if you are
	// rendering it. Error mentions only the entries relevant to this failure,
	// because a version set aside from a package that resolved fine is noise;
	// this field holds all of them. But "all of them" means every version the
	// resolution actually EXAMINED, which is no longer every version published:
	// candidate selection stops at the first usable version, so a version ranked
	// below the one chosen is never looked at and never appears here.
	//
	// ⚠️ So do not present this as "everything wrong with these packages". It is
	// what the resolution encountered on its way to this answer. The entries that
	// explain a failure are still all present -- a package with nothing usable is
	// examined exhaustively, because that is what establishing "nothing" requires
	// -- which is the case a report needs. See provider.Provider.Unusable.
	Unusable []provider.Unusable

	// cause is the solver error this was built from, so errors.As can reach
	// *solver.Unsolvable and the derivation graph inside it.
	cause error
}

// Error renders the explanation, followed by a note for each release that was
// set aside for a reason this failure makes relevant.
//
// A nil Report yields a bare sentence rather than a panic, following
// report.Explain's own reasoning about a nil root cause: an error message is
// what someone sees when something has already gone wrong, so it is the last
// place that should introduce a second failure.
func (e *ResolutionError) Error() string {
	var b strings.Builder
	if e.Report == nil {
		b.WriteString("resolver: the requirements cannot be satisfied, " +
			"and no explanation was recorded")
	} else {
		b.WriteString(e.Report.String())
	}
	for _, u := range e.relevantRejections() {
		b.WriteString("\n\n")
		b.WriteString(rejectionExplanation(u))
	}
	return b.String()
}

// Unwrap returns the solver's own error, so errors.As reaches
// *solver.Unsolvable and the derivation graph it carries.
func (e *ResolutionError) Unwrap() error { return e.cause }

// rejectionExplanation writes the paragraph for one set-aside version.
//
// Without one the report says "no version of flask matches >=3.0" about a version
// the user can plainly see on PyPI, which is the single worst thing it could say:
// everything in the sentence is true, and it sends the reader to look for a
// release that is right there.
//
// # Each kind gets its own paragraph, and the remedy is the reason why
//
// The categories differ in what the reader should DO. "Pin to a version that
// ships a wheel" is right for sdist-only metadata and wrong for a
// platform-incompatible wheel, where the release is fine and the target is the
// mismatch. A single paragraph covering both would have to be vague enough to be
// useless.
//
// The default arm is deliberately a real sentence rather than a panic or an empty
// string: an error message is what someone sees when something has already gone
// wrong, so a new kind that reaches here should read plainly, not vanish.
func rejectionExplanation(u provider.Unusable) string {
	switch u.Kind {
	case provider.KindMetadataUnavailable:
		return fmt.Sprintf(
			"Note: %s %s exists, but it publishes no readable dependency metadata -- it ships "+
				"only an sdist, or declares its metadata dynamically. This resolver does not build "+
				"sdists to find out what they require, so that version was not considered. Pin %s "+
				"to a version that ships a wheel, or ask its maintainer to publish one.",
			u.Package.Name, u.Version, u.Package.Name)

	case provider.KindNoCompatibleWheel:
		return fmt.Sprintf(
			"Note: %s %s exists, but %s, so that version was not considered. Resolve for a "+
				"target its wheels support, or pin %s to a version that publishes one.",
			u.Package.Name, u.Version, u.Reason, u.Package.Name)

	case provider.KindNoDistributions:
		return fmt.Sprintf(
			"Note: %s %s exists, but %s, so that version was not considered.",
			u.Package.Name, u.Version, u.Reason)
	}

	return fmt.Sprintf("Note: %s %s was not considered because %s.",
		u.Package.Name, u.Version, u.Reason)
}

// relevantRejections selects the records worth putting in front of a user for
// THIS failure.
//
// Three filters, and all three matter:
//
//   - Offered == false. An offered version was a candidate; its record is a
//     note about how it was treated, not a reason it could not be used, and
//     reporting one claims a version was rejected when it was not.
//   - Kind.Reportable(). A record has to name something the reader can act on.
//     ⚠️ This used to be equality against provider.ReasonMetadataUnavailable,
//     which meant every OTHER category was silently dropped -- a resolution that
//     failed because nothing was installable on the target rendered as a bare
//     "no solution" that did not mention the target. The predicate lives on the
//     kind, in the package that mints kinds, so adding a category is a decision
//     made once rather than a filter here that nobody remembers to widen.
//   - One paragraph per (project, version). The provider's own dedupe key is
//     the SOLVER package, and an extra is a separate solver package for the
//     same project: flask and flask[async] each get a record for flask 3.0
//     being sdist-only. rejectionExplanation reads only the project name, the
//     version and the reason, so those two records produce byte-identical
//     paragraphs, and a report that says the same thing twice reads like two
//     problems.
func (e *ResolutionError) relevantRejections() []provider.Unusable {
	if e.Report == nil {
		return nil
	}
	var out []provider.Unusable
	// version.Version holds slices and cannot key a map, which is why the
	// provider builds its key from strings too.
	seen := map[string]bool{}
	for _, u := range e.Unusable {
		if u.Offered || !u.Kind.Reportable() {
			continue
		}
		key := string(u.Package.Name) + "\x00" + u.Version.String()
		if seen[key] {
			continue
		}
		if e.reportNames(u) {
			seen[key] = true
			out = append(out, u)
		}
	}
	return out
}

// reportNames reports whether the explanation is about u's project, over a
// range that holds u's version.
//
// ⚠️ A LINE'S OWN Node IS NOT ENOUGH. The last line of a report states §7.4's
// terminal incompatibility, whose only term is about the root package -- the
// packages its sentence names come from the incompatibilities it was DERIVED
// from. Testing only the node itself finds nothing for the very failure this
// explanation exists to annotate: "no version of flask matches >=3.0", where
// the report is one line long.
//
// Following Causes here is a membership test, not a second explanation. The
// report's own ordering and line-numbering work is not repeated -- that is what
// report.FromError is for.
//
// Matching is by project name and ignores extras: flask and flask[async] are
// two solver nodes for one project, and a user reading "no version of
// flask[async] matches ..." is being told something about flask.
func (e *ResolutionError) reportNames(u provider.Unusable) bool {
	// The graph is a DAG with sharing -- a conclusion needed twice is derived
	// once and cited -- so an unvisited-set is what keeps this linear.
	visited := map[*solver.Incompatibility[provider.Package, pep440set.Set]]bool{}
	for _, line := range e.Report.Lines {
		if incompatibilityNames(line.Node, u, visited) {
			return true
		}
	}
	return false
}

func incompatibilityNames(
	inc *solver.Incompatibility[provider.Package, pep440set.Set],
	u provider.Unusable,
	visited map[*solver.Incompatibility[provider.Package, pep440set.Set]]bool,
) bool {
	if inc == nil || visited[inc] {
		return false
	}
	visited[inc] = true

	for _, pkg := range inc.Packages() {
		if pkg.Kind != provider.KindProject || pkg.Name != u.Package.Name {
			continue
		}
		if t, ok := inc.Term(pkg); ok && t.Set().Contains(u.Version) {
			return true
		}
	}

	a, b, derived := inc.Causes()
	if !derived {
		return false
	}
	return incompatibilityNames(a, u, visited) || incompatibilityNames(b, u, visited)
}

// explain turns a failed solve into a *ResolutionError, or passes the error
// through unchanged when it was not a resolution failure at all.
//
// The explanation is built with report.FromError rather than by walking the
// derivation graph here. §9's ordering and line-numbering rules are the hard
// part of presenting a PubGrub failure, go-pubgrub implements them, and a
// second implementation would only be a second thing to get wrong.
func explain(err error, unusable []provider.Unusable) error {
	rep, ok := report.FromError[provider.Package, pep440set.Set](err, pythonFormatter{})
	if !ok {
		// Not a conflict: the solve could not be carried out. Reporting a
		// provider failure as "these requirements conflict" would be a lie
		// about whose problem it is.
		return err
	}
	return &ResolutionError{Report: rep, Unusable: unusable, cause: err}
}
