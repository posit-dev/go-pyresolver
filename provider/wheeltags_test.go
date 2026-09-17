// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

// linuxTarget compiles the same target testEnv describes: CPython 3.11 on
// manylinux x86_64. Compiled from tags.Target rather than assembled by hand so
// the compatible set is the library's, not this test's idea of it.
func linuxTarget(t *testing.T) *tags.Matcher {
	t.Helper()
	m, err := tags.Target{
		Implementation: "cp", PyMajor: 3, PyMinor: 11,
		OS: "linux", Arch: "x86_64", Libc: "glibc", LibcMajor: 2, LibcMinor: 28,
	}.Compile()
	if err != nil {
		t.Fatalf("compile target: %v", err)
	}
	return m
}

const testTargetName = "cp311 on manylinux_2_28_x86_64"

// tagFilter returns a filter for linuxTarget.
func tagFilter(t *testing.T) *provider.WheelTagFilter {
	t.Helper()
	return &provider.WheelTagFilter{Matcher: linuxTarget(t), Target: testTargetName}
}

// tagIndex builds a one-package index whose single version carries the given tag
// claim, with the index declared COMPLETE so filtering is actually on.
//
// It returns the index already wrapped in a countingIndex, because most tests
// here care how much work a rejection cost as well as its outcome.
func tagIndex(t *testing.T, wheelTags []string, hasSdist, captured bool) *countingIndex {
	t.Helper()

	idx := index.NewMockIndex("test").
		AddVersion("acme", "1.0.0", "flask>=2.0 ; python_version >= '3.8'").
		SetWheelTagsComplete(true)

	meta, err := index.ParseRecord(index.RawRecord{
		RequiresDist: []string{"flask>=2.0 ; python_version >= '3.8'"},
		WheelTags:    wheelTags,
		HasSdist:     hasSdist,
		TagsCaptured: captured,
	})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", meta)
	idx.AddPackage("flask").AddVersion("flask", "2.1.0")

	return newCountingIndex(idx)
}

// admits reports whether Candidates offered acme 1.0.0, plus what was recorded.
func admits(t *testing.T, idx index.MetadataIndex, filter *provider.WheelTagFilter) (bool, []provider.Unusable) {
	t.Helper()

	opts := testOptions(t)
	opts.WheelTags = filter
	p := provider.New(context.Background(), idx, opts)

	_, found, _, err := p.Candidates(provider.Project(index.NewPackageName("acme")), atLeast(t, "1.0.0"))
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	return found, p.Unusable()
}

// TestWheelTagRejectionRules covers all five rows of the rejection rule, plus
// uncaptured, in one table.
//
// ⚠️ Only ONE row rejects for incompatibility, and that asymmetry is the point:
// rejecting a version that still has a way to install is how a tag filter narrows
// a resolution that would otherwise have succeeded. A test that only checked the
// rejecting row would pass just as well against a filter that rejected all five.
func TestWheelTagRejectionRules(t *testing.T) {
	tests := []struct {
		name      string
		wheelTags []string
		hasSdist  bool
		captured  bool
		wantFound bool
		wantKind  provider.UnusableKind
	}{
		{
			name:      "a compatible wheel",
			wheelTags: []string{"cp311-cp311-manylinux_2_17_x86_64"},
			captured:  true,
			wantFound: true,
		},
		{
			name:      "wheels, none compatible, an sdist",
			wheelTags: []string{"cp311-cp311-win_amd64"},
			hasSdist:  true,
			captured:  true,
			wantFound: true,
		},
		{
			name:      "wheels, none compatible, no sdist",
			wheelTags: []string{"cp311-cp311-win_amd64"},
			captured:  true,
			wantFound: false,
			wantKind:  provider.KindNoCompatibleWheel,
		},
		{
			name:      "no wheels, an sdist",
			hasSdist:  true,
			captured:  true,
			wantFound: true,
		},
		{
			name:      "no files at all",
			captured:  true,
			wantFound: false,
			wantKind:  provider.KindNoDistributions,
		},
		{
			// Not one of the five: the producer derived nothing for this version, so
			// there is no claim to test. Rejecting here would filter on data that
			// was never collected.
			name:      "tags never captured",
			wantFound: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx := tagIndex(t, tc.wheelTags, tc.hasSdist, tc.captured)
			found, recorded := admits(t, idx, tagFilter(t))

			if found != tc.wantFound {
				t.Fatalf("offered = %v, want %v (recorded %v)", found, tc.wantFound, recorded)
			}
			if tc.wantFound {
				for _, u := range recorded {
					if u.Kind == provider.KindNoCompatibleWheel || u.Kind == provider.KindNoDistributions {
						t.Errorf("an admitted version must record no tag rejection, got %q", u.Reason)
					}
				}
				return
			}

			if len(recorded) != 1 {
				t.Fatalf("recorded %d reasons, want exactly 1: %v", len(recorded), recorded)
			}
			if recorded[0].Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", recorded[0].Kind, tc.wantKind)
			}
			if recorded[0].Offered {
				t.Error("a rejected version must not be recorded as offered")
			}
		})
	}
}

// TestWheelTagRejectionNamesTheTarget pins that a rejection says what it was
// matching against and what the version published.
//
// Without both, the message is unactionable: the reader cannot tell whether their
// target or the package is the surprise, which is exactly the position an admin
// specifying a glibc floor older than any available wheel ends up in.
func TestWheelTagRejectionNamesTheTarget(t *testing.T) {
	idx := tagIndex(t, []string{"cp311-cp311-win_amd64"}, false, true)
	found, recorded := admits(t, idx, tagFilter(t))

	if found {
		t.Fatal("a Windows-only wheel with no sdist is not usable on linux")
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d reasons, want 1: %v", len(recorded), recorded)
	}
	reason := recorded[0].Reason
	if !strings.Contains(reason, testTargetName) {
		t.Errorf("reason %q must name the target %q", reason, testTargetName)
	}
	if !strings.Contains(reason, "cp311-cp311-win_amd64") {
		t.Errorf("reason %q must name the tags the version publishes", reason)
	}
}

// TestWheelTagRejectionPrecedesInterpreterWork is the PLACEMENT assertion, and it
// is why the check sits immediately after the metadata read.
//
// A test that only checked the rejection outcome would pass with the check
// anywhere in usable(), so this one makes the two positions produce DIFFERENT
// observable results. The version below is rejectable twice over: its wheels miss
// the target and it has no sdist, AND its Requires-Python "===1.0" has no
// version-set equivalent, which interpreterDependency reports as its own reason.
// Whichever check runs first is the reason that gets recorded.
//
// So this fails if the tag test is moved below the interpreter and marker work --
// which is the placement Rev 2 of the design specified, and the one the decision
// record corrected.
func TestWheelTagRejectionPrecedesInterpreterWork(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddPackage("acme").
		SetWheelTagsComplete(true)

	meta, err := index.ParseRecord(index.RawRecord{
		RequiresPython: "===1.0",
		WheelTags:      []string{"cp311-cp311-win_amd64"},
		TagsCaptured:   true,
	})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if meta.RequiresPython.String() == "" {
		t.Fatal("fixture invariant: the Requires-Python must survive parsing to be reached later")
	}
	idx.SetMetadata("acme", "1.0.0", meta)
	counted := newCountingIndex(idx)

	found, recorded := admits(t, counted, tagFilter(t))
	if found {
		t.Fatal("the version must be rejected")
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d reasons, want 1: %v", len(recorded), recorded)
	}
	if recorded[0].Kind != provider.KindNoCompatibleWheel {
		t.Errorf("kind = %q, want %q -- a later reason winning means the tag test moved below it",
			recorded[0].Kind, provider.KindNoCompatibleWheel)
	}
	if strings.Contains(recorded[0].Reason, "Requires-Python") {
		t.Errorf("reason %q is the interpreter's, so the tag test ran too late", recorded[0].Reason)
	}

	// And the tag test must reuse the read that fed it rather than asking again:
	// MetadataIndex promises no memoization, so a second ask can mean a second
	// decode on the per-candidate path.
	if got := counted.metadata.Load(); got != 1 {
		t.Errorf("metadata reads = %d, want 1", got)
	}
}

// TestInterpreterRejectionStillWinsWithoutTagFiltering is the control for the test
// above, and without it that test proves much less than it looks.
//
// Same version, tag filtering off. The interpreter reason must now be the one
// recorded -- which is what shows the previous test's assertion discriminates
// between the two placements, rather than the interpreter reason being
// unreachable for this fixture in the first place.
func TestInterpreterRejectionStillWinsWithoutTagFiltering(t *testing.T) {
	idx := index.NewMockIndex("test").AddPackage("acme")

	meta, err := index.ParseRecord(index.RawRecord{
		RequiresPython: "===1.0",
		WheelTags:      []string{"cp311-cp311-win_amd64"},
		TagsCaptured:   true,
	})
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	idx.SetMetadata("acme", "1.0.0", meta)

	found, recorded := admits(t, idx, nil)
	if found {
		t.Fatal("an unrepresentable Requires-Python is still a rejection")
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d reasons, want 1: %v", len(recorded), recorded)
	}
	if !strings.Contains(recorded[0].Reason, "Requires-Python") {
		t.Errorf("reason = %q, want the interpreter's", recorded[0].Reason)
	}
}

// TestWheelTagFilteringOffWhenIndexIsIncomplete is the gate that matters most in
// production today.
//
// The index below carries a real, genuinely incompatible tag claim. Filtering must
// still not happen, because on a partially derived corpus a version with no
// compatible tag cannot be told apart from one whose tags were never derived --
// and rejecting on that basis removes installable packages. The live PyPI snapshot
// has tags for 1.2% of packages, so this is the state, not an edge case.
func TestWheelTagFilteringOffWhenIndexIsIncomplete(t *testing.T) {
	idx := tagIndex(t, []string{"cp311-cp311-win_amd64"}, false, true)
	// Same data, minus the completeness declaration.
	idx.inner.(*index.MockIndex).SetWheelTagsComplete(false)

	found, recorded := admits(t, idx, tagFilter(t))
	if !found {
		t.Fatalf("an incomplete index must not filter on tags; recorded %v", recorded)
	}
}

// TestWheelTagFilteringOffWithoutAFilter pins that tag data alone changes nothing.
// A caller who never asked for tag filtering gets the previous behaviour exactly.
func TestWheelTagFilteringOffWithoutAFilter(t *testing.T) {
	idx := tagIndex(t, []string{"cp311-cp311-win_amd64"}, false, true)

	found, recorded := admits(t, idx, nil)
	if !found {
		t.Fatalf("no filter means no tag rejection; recorded %v", recorded)
	}
}

// TestWheelTagFilteringOffWithoutAMatcher covers the half-built filter. A
// WheelTagFilter with no Matcher has no notion of compatibility, so it must filter
// nothing rather than reject everything -- the same fail-open direction as an
// incomplete index.
func TestWheelTagFilteringOffWithoutAMatcher(t *testing.T) {
	idx := tagIndex(t, []string{"cp311-cp311-win_amd64"}, false, true)

	found, _ := admits(t, idx, &provider.WheelTagFilter{Target: testTargetName})
	if !found {
		t.Fatal("a filter with no matcher must filter nothing")
	}
}

// TestUnreadableTagIsNotCompatible pins that a tag this build cannot parse counts
// as not compatible rather than as a wildcard.
//
// The version still survives here because it publishes an sdist -- which is the
// honest outcome: an unreadable tag is one lost wheel, not a lost release. A
// decoder that treated it as compatible would offer a version nothing can install.
func TestUnreadableTagIsNotCompatible(t *testing.T) {
	garbage := []string{"", "not-a-valid-tag-at-all-"}

	withSdist := tagIndex(t, garbage, true, true)
	if found, recorded := admits(t, withSdist, tagFilter(t)); !found {
		t.Fatalf("an unreadable tag plus an sdist is still installable; recorded %v", recorded)
	}

	withoutSdist := tagIndex(t, garbage, false, true)
	found, recorded := admits(t, withoutSdist, tagFilter(t))
	if found {
		t.Fatal("an unreadable tag must not count as compatible")
	}
	if len(recorded) != 1 || recorded[0].Kind != provider.KindNoCompatibleWheel {
		t.Fatalf("want one no-compatible-wheel record, got %v", recorded)
	}
}

// TestPureWheelIsCompatible is the case that dominates the corpus: py3-none-any is
// 8.0M of 14.3M observed tag occurrences, so a filter that got it wrong would
// reject most of PyPI.
func TestPureWheelIsCompatible(t *testing.T) {
	idx := tagIndex(t, []string{"py3-none-any"}, false, true)

	if found, recorded := admits(t, idx, tagFilter(t)); !found {
		t.Fatalf("py3-none-any runs everywhere; recorded %v", recorded)
	}
}

// TestCompressedTagExpandsToAlternatives pins that one tag STRING can carry
// several tags. "cp310.cp311-none-any" is two, and only the second matches this
// target -- so a filter comparing whole strings, or taking only ParseTag's first
// result, would reject an installable wheel.
func TestCompressedTagExpandsToAlternatives(t *testing.T) {
	parsed, err := tags.ParseTag("cp310.cp311-none-any")
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	if len(parsed) < 2 {
		t.Fatalf("fixture invariant: expected a compressed tag to expand, got %v", parsed)
	}

	idx := tagIndex(t, []string{"cp310.cp311-none-any"}, false, true)
	if found, recorded := admits(t, idx, tagFilter(t)); !found {
		t.Fatalf("a compressed tag matching on its second alternative is compatible; recorded %v", recorded)
	}
}

// TestIsCompatibleOrNewerSlotsIn confirms the issue's claim that a different
// matching policy needs no structural change here: WheelTagMatcher is an
// interface, so an alternative policy is a wrapper rather than a mode flag.
//
// The stand-in below admits everything, which is enough to show the seam is a
// seam. It deliberately does not reimplement or-newer semantics -- asserting a
// policy this package does not own would be testing go-python-packaging.
func TestIsCompatibleOrNewerSlotsIn(t *testing.T) {
	idx := tagIndex(t, []string{"cp311-cp311-win_amd64"}, false, true)

	found, recorded := admits(t, idx, &provider.WheelTagFilter{
		Matcher: alwaysCompatible{},
		Target:  "a laxer policy",
	})
	if !found {
		t.Fatalf("a custom matcher decides compatibility; recorded %v", recorded)
	}
}

// alwaysCompatible is a WheelTagMatcher standing in for a laxer policy such as
// tags.Matcher.IsCompatibleOrNewer.
type alwaysCompatible struct{}

func (alwaysCompatible) IsCompatible([]tags.Tag) bool { return true }

// TestTagSlicesAreNotMutated pins that admission leaves the index's tag slice
// alone.
//
// It matters more than it looks: an RSFIndex's tag slices alias one pool backing
// array shared by every version in the same slot, so sorting or de-duplicating in
// place here would corrupt the tags of unrelated versions -- and only of the ones
// that happened to share a slot, which is the hardest possible shape to debug.
func TestTagSlicesAreNotMutated(t *testing.T) {
	original := []string{"cp311-cp311-win_amd64", "py2-none-any", "cp311-cp311-macosx_11_0_arm64"}
	held := make([]string, len(original))
	copy(held, original)

	idx := tagIndex(t, original, true, true)
	if found, _ := admits(t, idx, tagFilter(t)); !found {
		t.Fatal("fixture invariant: the sdist keeps this version usable")
	}

	for i := range held {
		if original[i] != held[i] {
			t.Fatalf("tag slice was reordered or rewritten at %d: got %q, want %q",
				i, original[i], held[i])
		}
	}
}

// TestWheelTagsCompleteRequiresForwarding is the wrapper hazard, asserted rather
// than only documented.
//
// The capability is discovered by type assertion, so a MetadataIndex decorator
// that does not implement WheelTagsComplete turns filtering off for everything
// behind it. That is the safe direction and an invisible one, and this is the test
// that makes the next such wrapper fail loudly here instead of quietly shipping
// with the feature disabled.
func TestWheelTagsCompleteRequiresForwarding(t *testing.T) {
	complete := index.NewMockIndex("test").SetWheelTagsComplete(true)

	if !index.WheelTagsComplete(complete) {
		t.Fatal("fixture invariant: the mock declares itself complete")
	}
	if !index.WheelTagsComplete(newCountingIndex(complete)) {
		t.Error("countingIndex must forward WheelTagsComplete, or every test wrapping it silently stops filtering")
	}
	if index.WheelTagsComplete(opaqueWrapper{complete}) {
		t.Error("a wrapper that does not forward must read as incomplete, not inherit the capability")
	}
}

// opaqueWrapper is a MetadataIndex decorator that forwards everything EXCEPT
// WheelTagsComplete, standing in for a wrapper someone forgot to update.
type opaqueWrapper struct{ inner index.MetadataIndex }

func (o opaqueWrapper) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	return o.inner.Versions(ctx, pkg)
}

func (o opaqueWrapper) Metadata(
	ctx context.Context, pkg index.PackageName, ver version.Version,
) (index.PackageMetadata, error) {
	return o.inner.Metadata(ctx, pkg, ver)
}

func (o opaqueWrapper) Files(
	ctx context.Context, pkg index.PackageName, ver version.Version,
) ([]index.DistFile, error) {
	return o.inner.Files(ctx, pkg, ver)
}
