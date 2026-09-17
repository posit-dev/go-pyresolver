// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/tags"
)

const wheelTagTargetName = "cp311 on manylinux_2_28_x86_64"

// windowsOnlyIndex is a package whose only release publishes a Windows wheel and
// no sdist, with the index declaring its tag data complete.
func windowsOnlyIndex(t *testing.T) *index.MockIndex {
	t.Helper()

	idx := index.NewMockIndex("test").
		AddPackage("acme").
		SetWheelTagsComplete(true)

	meta, err := index.ParseRecord(index.RawRecord{
		WheelTags:    []string{"cp311-cp311-win_amd64"},
		TagsCaptured: true,
	})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", meta)
	return idx
}

// resolveForLinux resolves reqs with wheel-tag filtering on for a linux target.
func resolveForLinux(t *testing.T, idx index.MetadataIndex, reqs ...string) (*resolver.Resolution, error) {
	t.Helper()

	matcher, err := tags.Target{
		Implementation: "cp", PyMajor: 3, PyMinor: 11,
		OS: "linux", Arch: "x86_64", Libc: "glibc", LibcMajor: 2, LibcMinor: 28,
	}.Compile()
	if err != nil {
		t.Fatalf("compile target: %v", err)
	}

	opts := testOptions(t)
	opts.WheelTags = &provider.WheelTagFilter{Matcher: matcher, Target: wheelTagTargetName}
	return resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, opts)
}

// TestFailureNamesTheTargetAndTheIncompatibility is the reason error.go had to
// change, and it is the assertion #20590 exists for.
//
// ⚠️ Before this, relevantSdistOnly discarded every reason but
// ReasonMetadataUnavailable, so a resolution that failed because nothing was
// installable on the target rendered as a bare "no version of acme matches" --
// true, useless, and silent about the one fact the reader needs. This is the
// message an admin hits when they specify a glibc floor older than any available
// wheel.
func TestFailureNamesTheTargetAndTheIncompatibility(t *testing.T) {
	_, err := resolveForLinux(t, windowsOnlyIndex(t), "acme>=1.0.0")
	if err == nil {
		t.Fatal("resolve succeeded, but nothing acme publishes can be installed on this target")
	}

	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *resolver.ResolutionError", err, err)
	}
	msg := re.Error()

	for _, want := range []string{
		"acme",
		"1.0.0",
		wheelTagTargetName,       // what it was matched against
		"cp311-cp311-win_amd64",  // what the version actually publishes
		"no source distribution", // why the sdist fallback did not save it
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure message does not mention %q:\n%s", want, msg)
		}
	}
}

// TestTagRejectionIsRecordedAsAKind pins that the record is machine-readable, not
// just human-readable: a caller deciding whether to fail or warn needs a category,
// and matching the sentence cannot work when the sentence names the tags.
func TestTagRejectionIsRecordedAsAKind(t *testing.T) {
	_, err := resolveForLinux(t, windowsOnlyIndex(t), "acme>=1.0.0")
	if err == nil {
		t.Fatal("resolve succeeded unexpectedly")
	}

	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *resolver.ResolutionError", err, err)
	}

	found := false
	for _, u := range re.Unusable {
		if u.Kind == provider.KindNoCompatibleWheel {
			found = true
			if u.Offered {
				t.Error("a rejected version must not be recorded as offered")
			}
			if !u.Kind.Reportable() {
				t.Error("a no-compatible-wheel record must be reportable, or it never reaches the message")
			}
		}
	}
	if !found {
		t.Errorf("no KindNoCompatibleWheel record in %+v", re.Unusable)
	}
}

// TestSdistFallbackKeepsTheResolutionWorking is the other half, and the one that
// would catch a filter that over-rejects.
//
// acme's wheels all miss the target, but it publishes an sdist, so RFD 0001 §5.3
// admits it and the resolution must SUCCEED. A tag filter that narrowed this away
// would break resolutions that work today, which is a worse outcome than not
// filtering at all.
func TestSdistFallbackKeepsTheResolutionWorking(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddPackage("acme").
		SetWheelTagsComplete(true)

	meta, err := index.ParseRecord(index.RawRecord{
		WheelTags:    []string{"cp311-cp311-win_amd64"},
		HasSdist:     true,
		TagsCaptured: true,
	})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", meta)

	res, err := resolveForLinux(t, idx, "acme>=1.0.0")
	if err != nil {
		t.Fatalf("an sdist is still a way to install acme: %v", err)
	}
	if got := pins(t, res)["acme"]; got != "1.0.0" {
		t.Errorf("acme pinned to %q, want 1.0.0", got)
	}
}

// TestIncompleteTagDataDoesNotFilter is the production state as of 2026-09: tags
// are on the wire for 1.2% of packages and the snapshot reports itself incomplete.
// The resolution must behave exactly as it did before wheel tags existed.
func TestIncompleteTagDataDoesNotFilter(t *testing.T) {
	idx := windowsOnlyIndex(t).SetWheelTagsComplete(false)

	res, err := resolveForLinux(t, idx, "acme>=1.0.0")
	if err != nil {
		t.Fatalf("an incomplete snapshot must not filter on tags: %v", err)
	}
	if got := pins(t, res)["acme"]; got != "1.0.0" {
		t.Errorf("acme pinned to %q, want 1.0.0", got)
	}
}

// TestSdistOnlyExplanationStillRenders guards the category that already existed.
// Generalizing the filter from one constant to a kind predicate is exactly the
// kind of change that can drop the case it started from.
func TestSdistOnlyExplanationStillRenders(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0").
		SetUnavailable("flask", "3.0")

	re := resolutionError(t, idx, "flask>=3.0")
	msg := re.Error()

	for _, want := range []string{"flask", "3.0", "sdist", "wheel"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the sdist-only message lost %q:\n%s", want, msg)
		}
	}
}
