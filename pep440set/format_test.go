// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"sort"
	"strings"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// A set reaches a user through go-pubgrub's failure report, which asks its
// Formatter to describe one. Nothing else can: spans and bounds are unexported,
// and deliberately so. Without this method the report renders a Set with %v and
// prints the raw struct -- measured, and it is several hundred characters of
// braces per version range.
func TestStringRendersSpecifiersTheWayTheyWereWritten(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want string
	}{
		{"==1.4", "==1.4"},
		{">=1.0", ">=1.0"},
		{">1.0", ">1.0"},
		{"<2.0", "<2.0"},
		{"<=2.0", "<=2.0"},
		{">=1.0,<2.0", ">=1.0,<2.0"},
		{">=1.0,<=2.0", ">=1.0,<=2.0"},
		{"~=1.4.2", ">=1.4.2,<1.5"},
		{"==1.*", "==1.*"},
		{"!=1.0", "!=1.0"},
		{"==1.0+ubuntu1", "==1.0+ubuntu1"},
		// Neither clause of the `!=` complement is `<1.0 || >1.0`, and the
		// difference is not cosmetic: `!=1.0` matches 1.0rc1 and 1.0.post1,
		// while `<1.0 || >1.0` matches neither. See
		// TestStringDistinguishesSetsHoldingDifferentVersions.
		{"!=1.0,>=0.5", ">=0.5,<1.0[+pre] || >=1.0.post0.dev0"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			specs, err := version.NewSpecifiers(tc.spec)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.spec, err)
			}
			s, err := FromSpecifiers(specs)
			if err != nil {
				t.Fatalf("convert %q: %v", tc.spec, err)
			}
			if got := s.String(); got != tc.want {
				t.Errorf("(%s).String() = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

func TestStringRendersTheDegenerateSets(t *testing.T) {
	if got, want := All().String(), "*"; got != want {
		t.Errorf("All().String() = %q, want %q", got, want)
	}
	if got, want := Empty().String(), "<none>"; got != want {
		t.Errorf("Empty().String() = %q, want %q", got, want)
	}
	if got, want := (Set{}).String(), "<none>"; got != want {
		t.Errorf("zero Set.String() = %q, want %q", got, want)
	}
}

func TestStringRendersExactly(t *testing.T) {
	if got, want := Exactly(version.MustParse("3.11.4")).String(), "==3.11.4"; got != want {
		t.Errorf("Exactly(3.11.4).String() = %q, want %q", got, want)
	}
}

// A rendering that dropped a span would understate a constraint, which in a
// failure report means describing a conflict that is not the one that happened.
func TestStringNamesEverySpan(t *testing.T) {
	s := Exactly(version.MustParse("1.0")).
		Union(Exactly(version.MustParse("2.0"))).
		Union(Exactly(version.MustParse("3.0")))
	if got, want := s.String(), "==1.0 || ==2.0 || ==3.0"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// Every renderable bound must produce SOME text. A silently empty side would
// turn ">=1.0,<2.0" into ">=1.0" -- a weaker constraint than the real one, and
// nothing downstream could tell.
func TestStringNeverRendersAnEmptyClause(t *testing.T) {
	operands := []string{"1", "1.0", "1.0.1", "1!2.0", "1.0rc1", "1.0.post1", "1.0.dev0", "1.0+local"}
	ops := []string{"==", ">=", ">", "<", "<=", "!=", "~="}
	for _, op := range ops {
		for _, operand := range operands {
			specs, err := version.NewSpecifiers(op + operand)
			if err != nil {
				continue // not a legal specifier; the grammar's business, not ours
			}
			s, err := FromSpecifiers(specs)
			if err != nil {
				continue
			}
			got := s.String()
			if got == "" {
				t.Errorf("%s%s rendered as the empty string", op, operand)
			}
			for _, part := range splitAll(got) {
				if part == "" || part == "<" || part == ">" || part == "==" ||
					part == ">=" || part == "<=" || part == "!=" {
					t.Errorf("%s%s rendered as %q, which holds a bare operator", op, operand, got)
				}
			}
		}
	}
}

// renderingGrid is the set of versions every rendering claim in this file is
// measured against. It surrounds release 1.0 on every axis PEP 440 has -- dev,
// pre, post, local, and the neighbouring releases -- because that is where the
// positions a specifier cannot name live, and a rendering that misstates one
// misstates it HERE or nowhere.
var renderingGrid = []string{
	"0.9", "1.0.dev0", "1.0.dev0+a", "1.0.dev1", "1.0rc1.dev0", "1.0rc1.dev0+a",
	"1.0rc1", "1.0rc1+a", "1.0rc1.post0.dev0", "1.0rc1.post1", "1.0rc2",
	"1.0", "1.0+a", "1.0+b", "1.0.post0.dev0", "1.0.post0", "1.0.post1",
	"1.0.post1.dev0", "1.0.post1+a", "1.0.post2",
	"1.0.0.1", "1.0.1", "1.1", "2.0", "1!1.0",
}

func gridVersions(t *testing.T) []version.Version {
	t.Helper()
	out := make([]version.Version, 0, len(renderingGrid))
	for _, s := range renderingGrid {
		v, err := version.Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		out = append(out, v)
	}
	return out
}

// holdsSameVersions reports whether two sets are indistinguishable over the
// grid. Set.Equal answers about POSITIONS and can separate sets that hold the
// same versions (see the package doc); a rendering only has to distinguish sets
// a user could tell apart, so the grid is the right question to ask here.
func holdsSameVersions(a, b Set, grid []version.Version) bool {
	for _, v := range grid {
		if a.Contains(v) != b.Contains(v) {
			return false
		}
	}
	return true
}

// edgeAnchorSets builds one span per (edge, anchor), in both bound positions.
// Between them these cover every bound lowerText and upperText can be handed.
func edgeAnchorSets(t *testing.T) map[string]Set {
	t.Helper()
	anchors := []string{"1.0", "1.0rc1", "1.0rc1.dev0", "1.0.dev0", "1.0.post1", "1.0+a", "1.0.post1+a"}
	edges := map[string]edge{
		"belowRelease": edgeBelowRelease,
		"at":           edgeAt,
		"aboveExact":   edgeAboveExact,
		"aboveLocals":  edgeAboveLocals,
		"aboveRelease": edgeAboveRelease,
	}
	out := map[string]Set{}
	for _, a := range anchors {
		av, err := version.Parse(a)
		if err != nil {
			t.Fatalf("parse anchor %q: %v", a, err)
		}
		for name, e := range edges {
			b := bound{v: av, edge: e}
			out["["+name+"("+a+"), +inf)"] = newSet(span{b, posInf()})
			out["(-inf, "+name+"("+a+"))"] = newSet(span{negInf(), b})
		}
	}
	return out
}

// TestStringRendersEveryEdgeExactly is the measurement behind String's claim.
//
// For every edge in both bound positions: if the rendering is a specifier, it
// must parse back to a set holding the SAME versions -- not merely a close one.
// The regression it guards is a rendering that names a different set than the
// one it describes, which is what ">1.0" did for the position above every
// 1.0+local: that position holds 1.0.post1 and `>1.0` does not.
//
// A rendering that does not parse is only allowed when PEP 440 genuinely cannot
// spell the position, and String says so in the text: a bracketed marker, or a
// local label (which no ordered comparison may carry).
func TestStringRendersEveryEdgeExactly(t *testing.T) {
	grid := gridVersions(t)
	for name, s := range edgeAnchorSets(t) {
		t.Run(name, func(t *testing.T) {
			text := s.String()
			specs, err := version.NewSpecifiers(text)
			if err != nil {
				if !strings.Contains(text, "[") && !strings.Contains(text, "+") {
					t.Fatalf("%s rendered as %q, which is neither a specifier nor "+
						"marked as unspellable: %v", name, text, err)
				}
				return
			}
			round, err := FromSpecifiers(specs)
			if err != nil {
				t.Fatalf("%s rendered as %q, which parses but does not convert: %v", name, text, err)
			}
			for _, v := range grid {
				if got, want := round.Contains(v), s.Contains(v); got != want {
					t.Errorf("%s rendered as %q: reparsed set Contains(%s)=%v, original=%v",
						name, text, v, got, want)
				}
			}
		})
	}
}

// TestStringDistinguishesSetsHoldingDifferentVersions is the collision test.
//
// ⚠️ TWO SETS A USER CAN TELL APART MUST NOT RENDER THE SAME TEXT. `>1.0` and
// the complement of `<=1.0` were both rendered ">1.0" and they differ on every
// post-release of 1.0; the complement of `>1.0` was rendered "<=1.0" and holds
// them too. A failure report built on that text names a version range the
// solver never reasoned about.
//
// Sets that hold the same versions MAY share a rendering -- (-inf, at(1.0.dev0))
// and (-inf, belowRelease(1.0)) are different positions with no version between
// them, and printing both as "<1.0" is right, not a bug. That is why the test
// compares over the version grid rather than with Equal.
func TestStringDistinguishesSetsHoldingDifferentVersions(t *testing.T) {
	grid := gridVersions(t)
	sets := edgeAnchorSets(t)

	// The pairs from the review, by name, so a failure says which is which.
	mk := func(spec string) Set {
		t.Helper()
		specs, err := version.NewSpecifiers(spec)
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}
		s, err := FromSpecifiers(specs)
		if err != nil {
			t.Fatalf("convert %q: %v", spec, err)
		}
		return s
	}
	for _, spec := range []string{
		">1.0", "<=1.0", ">=1.0", "<1.0", "==1.0", "!=1.0", "==1.*", "~=1.0",
		">=0.5,!=1.0", "==1.0+a", "!=1.0+a", ">1.0rc1", "<1.0rc1", ">1.0.post1",
		"<=1.0.post1", ">=1.0.dev0",
	} {
		sets[spec] = mk(spec)
		sets["("+spec+").Complement()"] = mk(spec).Complement()
	}

	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)

	byText := map[string][]string{}
	for _, name := range names {
		byText[sets[name].String()] = append(byText[sets[name].String()], name)
	}
	for text, sharing := range byText {
		for i := range sharing {
			for j := i + 1; j < len(sharing); j++ {
				a, b := sets[sharing[i]], sets[sharing[j]]
				if holdsSameVersions(a, b, grid) {
					continue
				}
				var differ []string
				for _, v := range grid {
					if a.Contains(v) != b.Contains(v) {
						differ = append(differ, v.String())
					}
				}
				t.Errorf("%s and %s both render as %q but differ on %v",
					sharing[i], sharing[j], text, differ)
			}
		}
	}
}

// TestStringDoesNotMisstateTheReviewedSets pins the three sets that were
// measured to render wrongly, with the version that proved it.
func TestStringDoesNotMisstateTheReviewedSets(t *testing.T) {
	post1 := version.MustParse("1.0.post1")
	gt := func(spec string) Set {
		t.Helper()
		specs, err := version.NewSpecifiers(spec)
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}
		s, err := FromSpecifiers(specs)
		if err != nil {
			t.Fatalf("convert %q: %v", spec, err)
		}
		return s
	}
	a := gt(">1.0")               // excludes 1.0.post1
	b := gt("<=1.0").Complement() // holds 1.0.post1
	c := gt(">1.0").Complement()  // holds 1.0.post1
	d := gt("!=1.0")              // holds 1.0.post1 and 1.0rc1

	if a.Contains(post1) || !b.Contains(post1) || !c.Contains(post1) || !d.Contains(post1) {
		t.Fatalf("premise changed: >1.0 holds %v, (<=1.0)' holds %v, (>1.0)' holds %v, !=1.0 holds %v",
			a.Contains(post1), b.Contains(post1), c.Contains(post1), d.Contains(post1))
	}
	if a.String() == b.String() {
		t.Errorf(">1.0 and (<=1.0).Complement() both render as %q, and they differ on 1.0.post1",
			a.String())
	}
	if a.String() == c.String() {
		t.Errorf(">1.0 and (>1.0).Complement() both render as %q", a.String())
	}
	if got, want := a.String(), ">1.0"; got != want {
		t.Errorf(">1.0 renders as %q, want %q", got, want)
	}
	if got, want := b.String(), ">=1.0.post0.dev0"; got != want {
		t.Errorf("(<=1.0).Complement() renders as %q, want %q", got, want)
	}
	if got, want := c.String(), "<=1.0[+post]"; got != want {
		t.Errorf("(>1.0).Complement() renders as %q, want %q", got, want)
	}
}

// splitAll breaks a rendering into its individual clauses.
func splitAll(s string) []string {
	var out []string
	for _, span := range strings.Split(s, " || ") {
		out = append(out, strings.Split(span, ",")...)
	}
	return out
}
