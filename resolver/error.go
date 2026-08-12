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

	// Unusable holds every version the resolution set aside and why, in the
	// order first encountered.
	//
	// It is the COMPLETE record, not the filtered one. Error mentions only the
	// entries relevant to this failure, because a version excluded from a
	// package that resolved fine is noise -- but a consumer building its own
	// presentation should be able to see everything that happened.
	Unusable []provider.Unusable

	// cause is the solver error this was built from, so errors.As can reach
	// *solver.Unsolvable and the derivation graph inside it.
	cause error
}

// Error renders the explanation, followed by a note for each release that was
// set aside for a reason this failure makes relevant.
func (e *ResolutionError) Error() string {
	var b strings.Builder
	b.WriteString(e.Report.String())
	for _, u := range e.relevantSdistOnly() {
		b.WriteString("\n\n")
		b.WriteString(sdistOnlyExplanation(u))
	}
	return b.String()
}

// Unwrap returns the solver's own error, so errors.As reaches
// *solver.Unsolvable and the derivation graph it carries.
func (e *ResolutionError) Unwrap() error { return e.cause }

// sdistOnlyExplanation is the message #18657 requires.
//
// Without it the report says "no version of flask matches >=3.0" about a
// version the user can plainly see on PyPI, which is the single worst thing it
// could say: everything in the sentence is true, and it sends the reader to
// look for a release that is right there.
func sdistOnlyExplanation(u provider.Unusable) string {
	return fmt.Sprintf(
		"Note: %s %s exists, but it publishes no readable dependency metadata -- it ships "+
			"only an sdist, or declares its metadata dynamically. This resolver does not build "+
			"sdists to find out what they require, so that version was not considered. Pin %s "+
			"to a version that ships a wheel, or ask its maintainer to publish one.",
		u.Package.Name, u.Version, u.Package.Name)
}

// relevantSdistOnly selects the records worth putting in front of a user for
// THIS failure.
//
// Two filters, and both matter:
//
//   - Offered == false. An offered version was a candidate; its record is a
//     note about how it was treated, not a reason it could not be used, and
//     reporting one claims a version was rejected when it was not.
//   - The report has to be talking about that package, at a version inside a
//     range the report names. A release excluded from a package that resolved
//     perfectly well is noise, and noise in a failure report is what makes
//     people stop reading them.
func (e *ResolutionError) relevantSdistOnly() []provider.Unusable {
	if e.Report == nil {
		return nil
	}
	var out []provider.Unusable
	for _, u := range e.Unusable {
		if u.Offered || u.Reason != provider.ReasonMetadataUnavailable {
			continue
		}
		if e.reportNames(u) {
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
