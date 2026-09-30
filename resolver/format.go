// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"slices"
	"strings"

	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

// pythonFormatter supplies the vocabulary go-pubgrub is deliberately blind to:
// how to name a package and how to describe a set of versions, in the words a
// Python user would use.
//
// It is unexported because a failure report's wording is not an API. A caller
// wanting its own presentation reads ResolutionError.Report's Lines, each of
// which carries the incompatibility behind the sentence.
type pythonFormatter struct {
	// permits names the packages behind a yank-pin virtual package's versions.
	// Nil in tests that format plain packages.
	permits *permitNames
}

// permitNames renders a yank-pin virtual package's version set as the real
// packages it stands for. The report calls Set right after Package for the
// same term, so the last package named is the one whose set is being rendered.
type permitNames struct {
	p    *provider.Provider
	last provider.Package
	seen []provider.Package
}

// noneMark stands in for a virtual version set that stands for no package, so
// rewrite can turn the sentence around it into plain words.
const noneMark = "\x00none\x00"

// rewrite turns the report's sentences about yank-pin virtual packages into
// ones about real packages: "no version of an exact pin on foo 1.0 matches
// <none>" becomes "no other package pins foo 1.0".
func (n *permitNames) rewrite(text string) string {
	if n == nil {
		return text
	}
	for _, pkg := range n.seen {
		every, nothing, _ := provider.VirtualPhrases(pkg)
		name := pkg.String()
		text = strings.ReplaceAll(text, "no version of "+name+" matches "+noneMark, nothing)
		text = strings.ReplaceAll(text, "every version of "+name, every)
	}
	return strings.ReplaceAll(text, noneMark, "from no other package")
}

// rootName is how the synthetic root package is named in a report.
//
// It holds a space, which no PEP 503-canonical name can, so a reader cannot
// mistake it for something to go and pin. That also keeps it distinct from
// every project name, which is what Package needs to stay injective.
const rootName = "the root project"

// Package names a package.
//
// ⚠️ THIS MUST BE INJECTIVE, AND provider.Package.String IS NOT GOOD ENOUGH.
//
// go-pubgrub documents that two distinct packages formatting to the same name
// are ordered ARBITRARILY within a sentence, because the ordering that makes a
// report deterministic is over the FORMATTED name. provider.Package.String
// renders the interpreter as "python" -- and "python" is a real project on
// PyPI, so a resolution that involved it would produce a report whose clauses
// came out in a different order from one run to the next.
//
// Rendering the interpreter as "Python" separates them, because a PEP
// 503-canonical name is lowercase. It also reads correctly: "flask 2.0 depends
// on Python >=3.10".
//
// The remaining cases stay one-to-one for the same reason -- a canonical name
// can hold neither a space (so the root is distinct) nor a bracket (so
// "flask[async]" cannot also be a project).
func (f pythonFormatter) Package(pkg provider.Package) string {
	if f.permits != nil {
		f.permits.last = pkg
		if _, _, ok := provider.VirtualPhrases(pkg); ok && !slices.Contains(f.permits.seen, pkg) {
			f.permits.seen = append(f.permits.seen, pkg)
		}
	}
	switch pkg.Kind {
	case provider.KindPython:
		return "Python"
	case provider.KindRoot:
		return rootName
	case provider.KindProject:
		if pkg.Extra != "" {
			return pkg.Name.String() + "[" + pkg.Extra + "]"
		}
		return pkg.Name.String()
	default:
		// A Kind this package does not know about. Falling back keeps a report
		// readable rather than blank, which matters because a report is what a
		// user sees when something has already gone wrong.
		return pkg.String()
	}
}

// Set describes a set of versions.
//
// A single version renders bare -- "flask 3.0 depends on werkzeug >=3.0" is how
// a Python user says it, and a decision is by far the most common thing a
// report names. The two degenerate sets get words rather than punctuation,
// because "*" and "" in the middle of a sentence read as a typo.
func (f pythonFormatter) Set(s pep440set.Set) string {
	if f.permits != nil {
		if name, none, ok := f.permits.p.DescribePermit(f.permits.last, s); ok {
			if none {
				return noneMark
			}
			return name
		}
	}
	if s.IsEmpty() {
		return "no version"
	}
	if s.Equal(pep440set.All()) {
		return "any version"
	}
	if v, ok := s.Singleton(); ok {
		return v.String()
	}
	return s.String()
}
