// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/version"
)

func mustV(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func versions(t *testing.T, ss ...string) []version.Version {
	t.Helper()
	out := make([]version.Version, 0, len(ss))
	for _, s := range ss {
		out = append(out, mustV(t, s))
	}
	return out
}

func names(vs []version.Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Original())
	}
	return out
}

func TestNewestRanksHighestFirst(t *testing.T) {
	in := versions(t, "1.0", "2.0", "1.5rc1", "0.9")
	got := names(Rank("flask", in, Newest{}))
	want := []string{"2.0", "1.5rc1", "1.0", "0.9"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Rank = %v, want %v", got, want)
		}
	}
}

func TestRankDoesNotMutateInput(t *testing.T) {
	in := versions(t, "1.0", "2.0", "0.9")
	before := names(in)
	Rank("flask", in, Newest{})
	if after := names(in); after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
		t.Errorf("Rank mutated its input: %v -> %v", before, after)
	}
}

func TestRankNilPolicyIsNewest(t *testing.T) {
	in := versions(t, "1.0", "2.0")
	if got := names(Rank("flask", in, nil)); got[0] != "2.0" {
		t.Errorf("nil policy should behave as Newest; got %v", got)
	}
}

// A policy that ranks is not a policy that filters: Rank must return exactly
// as many versions as it was given, whatever the policy says.
func TestRankNeverDropsAVersion(t *testing.T) {
	in := versions(t, "1.0", "2.0", "3.0")
	if got := Rank("flask", in, demoteAll{}); len(got) != len(in) {
		t.Errorf("Rank returned %d versions, want %d", len(got), len(in))
	}
}

// demoteAll treats every version as equally undesirable.
type demoteAll struct{}

func (demoteAll) Less(index.PackageName, version.Version, version.Version) bool { return false }

// TestRankStableUnderIndifference: an indifferent policy must preserve input order.
func TestRankStableUnderIndifference(t *testing.T) {
	in := versions(t, "1.0", "3.0", "2.0")
	got := names(Rank("flask", in, demoteAll{}))
	want := []string{"1.0", "3.0", "2.0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Rank = %v, want input order %v", got, want)
		}
	}
}
