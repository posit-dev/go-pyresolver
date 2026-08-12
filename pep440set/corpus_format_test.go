// SPDX-License-Identifier: Apache-2.0 OR MIT

// This differential shares corpus_test.go's external test package and its
// helpers -- corpusIndex, sample, and the two env vars -- for the reason given
// there: it imports index, and an in-package test importing index would make a
// future index -> pep440set dependency an import cycle rather than a decision.
package pep440set_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
)

// maxReportedRenderings bounds the failure output, as the Contains differential
// does: a systematic rendering bug produces thousands of identical-looking
// lines, and the first few are the ones anybody reads.
const maxReportedRenderings = 25

// reparseRendering turns a rendering back into a set.
//
// The " || " between spans is this package's own notation, not specifier
// syntax, so the groups are parsed separately and unioned. ok is false when any
// group is not a specifier, which is how the caller tells an unspellable
// position from a wrong one.
func reparseRendering(text string) (pep440set.Set, bool) {
	out := pep440set.Empty()
	for _, part := range strings.Split(text, " || ") {
		specs, err := version.NewSpecifiers(part)
		if err != nil {
			return out, false
		}
		s, err := pep440set.FromSpecifiers(specs)
		if err != nil {
			return out, false
		}
		out = out.Union(s)
	}
	return out, true
}

// TestRenderingDifferentialAgainstRealCorpus is TestStringRendersEveryEdgeExactly
// asked of the specifiers real publishers write, and of their COMPLEMENTS.
//
// The complements are the point. A published specifier renders back to itself
// almost by construction -- FromSpecifiers built the set from that text minutes
// earlier -- but the solver negates every term it derives, and a complement is
// where the bounds PEP 440 has no operator for come from: the complement of
// `>=6.0` starts below 6.0 and HOLDS 6.0.0rc1, which `<6.0` does not match.
// Rendering it "<6.0" was measured at 2,877 of 54,120 renderings over 1,500
// production packages before the bounds were made version-exact.
//
// The claim under test is not that every set has a specifier -- several do not,
// and String marks those. It is that a rendering which IS a specifier names the
// same versions as the set it came from.
func TestRenderingDifferentialAgainstRealCorpus(t *testing.T) {
	idx := corpusIndex(t)
	ctx := context.Background()

	limit := defaultCorpusPackages
	if raw := os.Getenv(corpusPackagesEnv); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s=%q: %v", corpusPackagesEnv, raw, err)
		}
		limit = n
	}

	all := idx.Packages()
	packages := sample(all, limit)

	versionsOf := map[index.PackageName][]version.Version{}
	depVersions := func(pkg index.PackageName) []version.Version {
		if vs, ok := versionsOf[pkg]; ok {
			return vs
		}
		vs, err := idx.Versions(ctx, pkg)
		if err != nil {
			vs = nil
		}
		versionsOf[pkg] = vs
		return vs
	}

	var (
		renderings int
		exact      int
		unspelled  int
		mismatches int
		reported   int
	)

	for _, raw := range packages {
		pkg := index.NewPackageName(raw)
		vers, err := idx.Versions(ctx, pkg)
		if err != nil {
			continue
		}
		versionsOf[pkg] = vers

		for _, ver := range sample(vers, maxVersionsPerPackage) {
			meta, err := idx.Metadata(ctx, pkg, ver)
			if err != nil {
				continue
			}
			for _, req := range meta.RequiresDist {
				ss := req.Specifiers
				if ss.String() == "" {
					continue
				}
				set, err := pep440set.FromSpecifiers(ss)
				if err != nil {
					// `===` and the `||` extension; the Contains differential
					// is where that refusal is checked.
					continue
				}

				candidates := depVersions(index.NewPackageName(req.Name))
				for _, s := range []pep440set.Set{set, set.Complement()} {
					renderings++
					text := s.String()
					if text == "*" || text == "<none>" {
						exact++
						continue
					}
					round, ok := reparseRendering(text)
					if !ok {
						// A position PEP 440 cannot spell. String must SAY so:
						// a marker, or a local label no comparison may carry.
						if !strings.Contains(text, "[") && !strings.Contains(text, "+") {
							t.Errorf("%s %s requires %q: rendering %q is neither a "+
								"specifier nor marked as unspellable",
								pkg, ver, req.Name, text)
						}
						unspelled++
						continue
					}
					bad := false
					for _, v := range candidates {
						if round.Contains(v) == s.Contains(v) {
							continue
						}
						bad = true
						mismatches++
						if reported < maxReportedRenderings {
							reported++
							t.Errorf("MISMATCH: %q rendered as %q, which %s %s "+
								"(from %s %s requiring %s)",
								ss.String(), text,
								map[bool]string{true: "admits", false: "rejects"}[round.Contains(v)],
								v.Original(), pkg, ver, req.Name)
						}
					}
					if !bad {
						exact++
					}
				}
			}
		}
	}

	t.Logf("corpus renderings: %d over %d packages; %d exact, %d unspellable-and-marked",
		renderings, len(packages), exact, unspelled)
	if mismatches > reported {
		t.Errorf("%d rendering disagreements total; %d reported above", mismatches, reported)
	}

	// A run that rendered nothing would be green and meaningless, which is the
	// failure mode corpus_test.go's floors exist to prevent.
	if renderings < 1000 {
		t.Errorf("only %d renderings measured over %d packages; the walk is not reaching "+
			"real requirements", renderings, len(packages))
	}
}
