// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set_test

import (
	"testing"

	"github.com/posit-dev/go-pubgrub/versionset"

	"github.com/posit-dev/go-pyresolver/pep440set"
)

// The load-bearing claim: Set satisfies go-pubgrub's constraint structurally,
// so the algebra never imports the solver.
var _ versionset.Set[pep440set.Set] = pep440set.Set{}

// TestGenericHelpersAccept exercises versionset's derived predicates, which is
// what the solver actually calls.
func TestGenericHelpersAccept(t *testing.T) {
	all := pep440set.All()
	empty := pep440set.Empty()

	if !versionset.IsSubsetOf(empty, all) {
		t.Error("Empty should be a subset of All")
	}
	if !versionset.IsDisjointFrom(empty, all) {
		t.Error("Empty should be disjoint from All")
	}
	if versionset.Difference(all, all).IsEmpty() != true {
		t.Error("All minus All should be empty")
	}
}
