// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestUnparseableRequirementIsClassifiable pins that a non-conforming
// Requires-Dist entry is reported as ErrMetadataUnusable.
//
// The refusal itself is deliberate and unchanged: silently dropping a requirement
// this module cannot parse would hand a resolver an incomplete dependency set and
// produce a confident wrong answer. What the sentinel adds is the ability to tell
// that refusal apart from an I/O failure or a bug, so a caller can respond in
// proportion instead of choosing between aborting and swallowing everything.
//
// The "broken" fixture carries the requirement "!!! not a requirement".
func TestUnparseableRequirementIsClassifiable(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Metadata(context.Background(), NewPackageName("broken"), mustVersion(t, "1.0"))
	if err == nil {
		t.Fatal("expected an error for a package whose requirement PEP 508 rejects")
	}
	if !errors.Is(err, ErrMetadataUnusable) {
		t.Errorf("error does not wrap ErrMetadataUnusable, so a caller cannot classify it: %v", err)
	}
}

// TestUnusableIsDistinctFromUnavailable keeps the two sentinels from collapsing
// into each other, which would defeat the point of adding one.
//
// The difference is whether a record EXISTS. ErrMetadataUnavailable means nothing
// was captured and no amount of care helps. ErrMetadataUnusable means the data is
// present and specific, which is what makes a targeted response possible. A caller
// that cannot tell them apart cannot decide whether retrying a different version
// is worth anything.
func TestUnusableIsDistinctFromUnavailable(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	// Present but non-conforming: unusable, NOT unavailable.
	_, err := idx.Metadata(ctx, NewPackageName("broken"), mustVersion(t, "1.0"))
	if errors.Is(err, ErrMetadataUnavailable) {
		t.Errorf("a non-conforming requirement must not report ErrMetadataUnavailable: %v", err)
	}

	// No record at all: unavailable, NOT unusable.
	_, err = idx.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "99.0"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("precondition: a version with no record should be unavailable, got %v", err)
	}
	if errors.Is(err, ErrMetadataUnusable) {
		t.Errorf("a missing record must not report ErrMetadataUnusable: %v", err)
	}
}

// TestUnusableErrorKeepsTheOffendingString checks that wrapping did not cost the
// diagnostic detail. The specific malformed requirement is the only thing that
// makes such a failure actionable, so it must survive in the message.
func TestUnusableErrorKeepsTheOffendingString(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Metadata(context.Background(), NewPackageName("broken"), mustVersion(t, "1.0"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "!!! not a requirement") {
		t.Errorf("error message lost the offending requirement string: %q", got)
	}
}
