// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"testing"
)

// Every MetadataIndex implementation must give the SAME answer for the same
// state, or a consumer's error branch is dead code against one index and live
// against another.
//
// rstudio/package-manager#19466's F12 found three answers for one state --
// unknown version of a KNOWN package:
//
//	documented contract  ErrPackageNotFound
//	MockIndex            ErrPackageNotFound
//	RSFIndex             ErrMetadataUnavailable
//
// ErrMetadataUnavailable is the correct one, and the other two were changed to
// match. ErrPackageNotFound is untrue on its face here -- the package WAS found
// -- and it promised a distinction RSFIndex cannot make, since it derives its
// version list from the RSF dependency map and so cannot tell an unknown version
// from one present with nothing captured.
//
// This test is table-driven over implementations on purpose: a new
// MetadataIndex added later is one line away from being covered, and the failure
// mode this guards is precisely "the new implementation chose its own answer".
func TestImplementationsAgreeOnErrorStates(t *testing.T) {
	ctx := context.Background()

	// Each implementation is built with the SAME logical contents: one package
	// "flask" with one version "3.0.0".
	impls := map[string]MetadataIndex{
		"MockIndex": NewMockIndex("agree").AddVersion("flask", "3.0.0"),
		"RSFIndex":  openFixtureIndex(t),
	}

	known := NewPackageName("flask")
	unknownPkg := NewPackageName("definitely-not-a-package")

	for name, idx := range impls {
		t.Run(name+"/unknown package", func(t *testing.T) {
			_, err := idx.Metadata(ctx, unknownPkg, mustVersion(t, "1.0"))
			if !errors.Is(err, ErrPackageNotFound) {
				t.Errorf("Metadata on an unknown package = %v, want ErrPackageNotFound", err)
			}
			// Must not ALSO satisfy the other states.
			if errors.Is(err, ErrMetadataUnavailable) {
				t.Errorf("an unknown package must not also report ErrMetadataUnavailable: %v", err)
			}
		})

		t.Run(name+"/unknown version of a known package", func(t *testing.T) {
			_, err := idx.Metadata(ctx, known, mustVersion(t, "99.99.99"))
			if !errors.Is(err, ErrMetadataUnavailable) {
				t.Errorf("Metadata on an unknown version = %v, want ErrMetadataUnavailable", err)
			}
			// ⚠️ This is the assertion that would have caught F12. The package
			// is present, so claiming not-found is a falsehood a caller acts on.
			if errors.Is(err, ErrPackageNotFound) {
				t.Errorf("an unknown VERSION of a present package must not report "+
					"ErrPackageNotFound -- the package was found: %v", err)
			}
		})

		t.Run(name+"/unknown package via Versions", func(t *testing.T) {
			_, err := idx.Versions(ctx, unknownPkg)
			if !errors.Is(err, ErrPackageNotFound) {
				t.Errorf("Versions on an unknown package = %v, want ErrPackageNotFound", err)
			}
		})
	}
}
