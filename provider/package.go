// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import "github.com/posit-dev/go-pyresolver/index"

// Kind distinguishes the three things a solver node can be.
//
// # Why a kind rather than a reserved name
//
// It looks like over-engineering until you check PyPI: "python" is a real
// project name there, and so is "root". A model that identified the interpreter
// by a sentinel name would merge it with any genuine dependency on that
// project, and the resulting conflict -- or worse, the resulting non-conflict --
// would be attributed to the wrong thing entirely. A separate field makes the
// collision unrepresentable instead of unlikely.
type Kind uint8

const (
	// KindRoot is the synthetic package holding the user's own requirements.
	// It is the zero value so that a Package that was never constructed is
	// obviously the root rather than a plausible-looking project with an empty
	// name.
	KindRoot Kind = iota

	// KindProject is a real PyPI project, or one of that project's extras.
	KindProject

	// KindPython is the target interpreter, modeled as a package so that a
	// Requires-Python conflict appears in the derivation graph as an ordinary
	// version conflict the report can explain, rather than as a version
	// silently vanishing from the candidate list.
	KindPython
)

// Package is one node in the solver's dependency graph.
//
// It is a comparable struct because go-pubgrub's type parameter P requires
// exactly that and nothing more: the solver keys maps by it, compares it, and
// otherwise never looks inside.
//
// # Extras are packages here
//
// flask and flask[async] are two Packages that differ only in Extra. That is
// the extras-as-virtual-packages model Poetry and uv both use, and it is what
// lets a solver with no notion of extras resolve them correctly: the extra
// depends on its own base at the same version, so the two cannot drift apart.
type Package struct {
	// Kind is which of the three things this is.
	Kind Kind

	// Name is the PEP 503-canonical project name. Empty for KindRoot and
	// KindPython, which have no project name.
	Name index.PackageName

	// Extra is the PEP 685-normalized extra this node activates, or "" for the
	// base package. Only meaningful for KindProject.
	Extra string
}

// Root returns the synthetic root package.
func Root() Package { return Package{Kind: KindRoot} }

// Python returns the target interpreter package.
func Python() Package { return Package{Kind: KindPython} }

// Project returns the base package for a project.
//
// The name is re-normalized rather than trusted. index.PackageName is a plain
// string type, so index.PackageName("Flask_Login") is constructible without
// ever passing through index.NewPackageName, and an un-normalized name reaching
// the solver builds two nodes for one project -- which can report a conflict
// that does not exist. candidate.EnabledPrereleases re-normalizes its input for
// the same reason; normalization is idempotent, so this costs nothing when the
// caller already did it right.
func Project(name index.PackageName) Package {
	return Package{Kind: KindProject, Name: index.NewPackageName(string(name))}
}

// WithExtra returns the virtual package for one extra of a project.
//
// An empty extra yields the base package, so a caller expanding a requirement's
// extras list does not need a special case for a requirement that has none.
func WithExtra(name index.PackageName, extra string) Package {
	pkg := Project(name)
	pkg.Extra = index.NormalizeExtra(extra)
	return pkg
}

// String renders the package the way a Python user would write it: "flask",
// "flask[async]", "python", "<root>".
//
// This is not cosmetic. go-pubgrub's failure report formats packages through
// this method, so it is the text a user reads when a resolution fails, and
// "flask[async]" has to look like the thing they typed rather than like a
// struct. The root is bracketed because it is the one node with no
// user-visible spelling at all.
func (p Package) String() string {
	switch p.Kind {
	case KindRoot:
		return "<root>"
	case KindPython:
		return "python"
	default:
		if p.Extra == "" {
			return string(p.Name)
		}
		return string(p.Name) + "[" + p.Extra + "]"
	}
}
