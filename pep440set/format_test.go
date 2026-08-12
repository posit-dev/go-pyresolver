// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
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
		{"!=1.0,>=0.5", ">=0.5,<1.0 || >1.0"},
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

// splitAll breaks a rendering into its individual clauses.
func splitAll(s string) []string {
	var out []string
	for _, span := range strings.Split(s, " || ") {
		out = append(out, strings.Split(span, ",")...)
	}
	return out
}
