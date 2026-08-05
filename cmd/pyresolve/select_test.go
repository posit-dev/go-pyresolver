// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// TestSelectHighestMatchesReferenceSelection pins PEP 440's default pre-release
// exclusion.
//
// Every expected value below is the MEASURED output of the reference
// implementation, pypa/packaging 26.2, for an unconstrained candidate set:
//
//	SpecifierSet("").filter([...])
//
//	['1.0', '1.1', '2.0rc1']       -> ['1.0', '1.1']
//	['2.0rc1']                     -> ['2.0rc1']
//	['1.0', '2.0.dev1']            -> ['1.0']
//	['2.0.dev1']                   -> ['2.0.dev1']
//	['1.0', '1.1.post1']           -> ['1.0', '1.1.post1']
//	['1.0a1', '1.0b1', '1.0rc1']   -> ['1.0a1', '1.0b1', '1.0rc1']
//	['1.0', '2.0a1', '2.0.dev1']   -> ['1.0']
//
// Recorded as measurements rather than as a reading of the spec, because a test
// written from the same understanding as the implementation agrees with it. The
// spec settles which behaviour is correct; the reference settles what the answer
// looks like.
func TestSelectHighestMatchesReferenceSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "a release candidate loses to a lower stable release",
			input: []string{"1.0", "1.1", "2.0rc1"},
			want:  "1.1",
		},
		{
			name:  "a release candidate wins when it is all there is",
			input: []string{"2.0rc1"},
			want:  "2.0rc1",
		},
		{
			name:  "a dev release is a pre-release and loses to a stable one",
			input: []string{"1.0", "2.0.dev1"},
			want:  "1.0",
		},
		{
			name:  "a dev release wins when it is all there is",
			input: []string{"2.0.dev1"},
			want:  "2.0.dev1",
		},
		{
			name:  "a post-release is NOT a pre-release and stays selectable",
			input: []string{"1.0", "1.1.post1"},
			want:  "1.1.post1",
		},
		{
			name:  "all pre-releases: the highest of them is chosen, across spellings",
			input: []string{"1.0a1", "1.0b1", "1.0rc1"},
			want:  "1.0rc1",
		},
		{
			name:  "several pre-release kinds still lose to one stable release",
			input: []string{"1.0", "2.0a1", "2.0.dev1"},
			want:  "1.0",
		},
		{
			name:  "a post-release carrying a dev segment IS a pre-release",
			input: []string{"1.0", "1.1.post1.dev0"},
			want:  "1.0",
		},
		{
			name:  "input order does not matter",
			input: []string{"2.0rc1", "1.1", "1.0"},
			want:  "1.1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vers := make([]version.Version, 0, len(tc.input))
			for _, s := range tc.input {
				v, err := version.Parse(s)
				if err != nil {
					t.Fatalf("Parse(%q): %v", s, err)
				}
				vers = append(vers, v)
			}

			got, ok := selectHighest(vers)
			if !ok {
				t.Fatalf("selectHighest(%v) reported no candidate", tc.input)
			}
			if got.String() != tc.want {
				t.Errorf("selectHighest(%v) = %q, want %q", tc.input, got.String(), tc.want)
			}
		})
	}
}

// TestSelectHighestEmpty covers the only case that reports no candidate. It is
// separate because every other test asserts ok is true, so a bug making
// selectHighest always report a candidate would otherwise go unnoticed.
func TestSelectHighestEmpty(t *testing.T) {
	if _, ok := selectHighest(nil); ok {
		t.Error("selectHighest(nil) reported a candidate")
	}
	if _, ok := selectHighest([]version.Version{}); ok {
		t.Error("selectHighest(empty) reported a candidate")
	}
}

// TestSelectHighestDoesNotMutateItsInput guards a hazard that is invisible at the
// call sites: both callers hold the slice returned by index.Versions, and walk
// reuses it. Sorting in place would reorder a caller's slice as a side effect of
// a query that reads like a pure function.
func TestSelectHighestDoesNotMutateItsInput(t *testing.T) {
	input := []string{"2.0rc1", "1.1", "1.0"}
	vers := make([]version.Version, 0, len(input))
	for _, s := range input {
		v, err := version.Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		vers = append(vers, v)
	}

	if _, ok := selectHighest(vers); !ok {
		t.Fatal("precondition: expected a candidate")
	}

	for i, want := range input {
		if got := vers[i].String(); got != want {
			t.Errorf("input reordered: vers[%d] = %q, want %q -- selectHighest must not "+
				"sort its argument in place, because callers reuse that slice", i, got, want)
		}
	}
}
