// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"reflect"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// plainIndex is a MetadataIndex that does NOT implement WheelTagIndex, standing in
// for the sources that cannot answer the question -- which is most of them, and
// which includes Package Manager's own out-of-tree index.
type plainIndex struct{}

func (plainIndex) Versions(context.Context, PackageName) ([]version.Version, error) {
	return nil, ErrPackageNotFound
}

func (plainIndex) Metadata(context.Context, PackageName, version.Version) (PackageMetadata, error) {
	return PackageMetadata{}, ErrPackageNotFound
}

func (plainIndex) Files(context.Context, PackageName, version.Version) ([]DistFile, error) {
	return nil, ErrFilesUnavailable
}

// TestWheelTagsCompleteDefaultsToFalse pins the fail-safe direction for an index
// that cannot answer.
//
// "Cannot say its tag data is complete" and "has said it is incomplete" are the
// same answer here on purpose: both mean nothing licenses filtering. The opposite
// default would turn the capability into an opt-OUT, so every existing index would
// silently start filtering on tag data it does not have.
func TestWheelTagsCompleteDefaultsToFalse(t *testing.T) {
	if WheelTagsComplete(plainIndex{}) {
		t.Error("an index that does not implement WheelTagIndex must read as incomplete")
	}
	if WheelTagsComplete(NewMockIndex("m")) {
		t.Error("a fresh MockIndex has no tag data and must read as incomplete")
	}
	if !WheelTagsComplete(NewMockIndex("m").SetWheelTagsComplete(true)) {
		t.Error("an index that declares itself complete must read as complete")
	}
}

// TestMultiIndexWheelTagsCompleteIsTheAND is the composition rule, and the OR is a
// real bug rather than a stylistic preference.
//
// A MultiIndex answers from whichever source has the package. Under an OR, one
// complete source would license filtering against BOTH, and every version served
// by the incomplete sibling would be judged on tags that were never derived.
func TestMultiIndexWheelTagsCompleteIsTheAND(t *testing.T) {
	complete := NewMockIndex("a").SetWheelTagsComplete(true)
	incomplete := NewMockIndex("b")

	tests := []struct {
		name    string
		sources []MetadataIndex
		want    bool
	}{
		{"no sources", nil, false},
		{"one complete", []MetadataIndex{complete}, true},
		{"both complete", []MetadataIndex{complete, NewMockIndex("c").SetWheelTagsComplete(true)}, true},
		{"complete then incomplete", []MetadataIndex{complete, incomplete}, false},
		{"incomplete then complete", []MetadataIndex{incomplete, complete}, false},
		{"complete then non-implementor", []MetadataIndex{complete, plainIndex{}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewMultiIndex(tc.sources...).WheelTagsComplete()
			if got != tc.want {
				t.Errorf("WheelTagsComplete() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFilteredIndexForwardsWheelTagsComplete pins that a FilterPolicy does not
// change the answer.
//
// A policy removes versions; the tag data on the versions that survive is as
// complete as it ever was. Answering false here instead would make wrapping an
// index in a filter a silent way to turn tag filtering off.
func TestFilteredIndexForwardsWheelTagsComplete(t *testing.T) {
	complete := NewMockIndex("a").SetWheelTagsComplete(true)
	if !NewFilteredIndex(complete, FilterPolicy{}).WheelTagsComplete() {
		t.Error("a FilteredIndex over a complete index must report complete")
	}
	if NewFilteredIndex(NewMockIndex("b"), FilterPolicy{}).WheelTagsComplete() {
		t.Error("a FilteredIndex over an incomplete index must report incomplete")
	}
	if NewFilteredIndex(plainIndex{}, FilterPolicy{}).WheelTagsComplete() {
		t.Error("a FilteredIndex over a non-implementor must report incomplete")
	}
}

// TestParseRecordCarriesTagsThrough pins that the three tag fields survive the
// parse step unchanged, including the distinction the whole design rests on.
func TestParseRecordCarriesTagsThrough(t *testing.T) {
	tests := []struct {
		name string
		raw  RawRecord
	}{
		{"captured with tags", RawRecord{
			WheelTags: []string{"py3-none-any"}, HasSdist: true, TagsCaptured: true,
		}},
		{"captured, no wheels, sdist", RawRecord{HasSdist: true, TagsCaptured: true}},
		{"captured, nothing at all", RawRecord{TagsCaptured: true}},
		{"uncaptured", RawRecord{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := ParseRecord(tc.raw)
			if err != nil {
				t.Fatalf("ParseRecord: %v", err)
			}
			if !reflect.DeepEqual(meta.WheelTags, tc.raw.WheelTags) {
				t.Errorf("WheelTags = %v, want %v", meta.WheelTags, tc.raw.WheelTags)
			}
			if meta.HasSdist != tc.raw.HasSdist {
				t.Errorf("HasSdist = %v, want %v", meta.HasSdist, tc.raw.HasSdist)
			}
			if meta.TagsCaptured != tc.raw.TagsCaptured {
				t.Errorf("TagsCaptured = %v, want %v", meta.TagsCaptured, tc.raw.TagsCaptured)
			}
		})
	}
}

// TestCloneCopiesWheelTags is the aliasing contract, and it is sharper for tags
// than for anything else Clone copies.
//
// An RSFIndex's tag slices alias ONE pool backing array shared by every version in
// the same slot. A caller that sorted the slice it was handed would therefore not
// corrupt one cache entry, it would corrupt the tags of every co-pooled version --
// and only of those, which is the hardest possible shape to debug.
func TestCloneCopiesWheelTags(t *testing.T) {
	original := PackageMetadata{WheelTags: []string{"py3-none-any", "cp39-cp39-manylinux_2_17_x86_64"}}

	clone := original.Clone()
	clone.WheelTags[0] = "MUTATED"

	if original.WheelTags[0] != "py3-none-any" {
		t.Errorf("mutating the clone changed the original: %q", original.WheelTags[0])
	}

	// nil is preserved rather than normalized to empty, matching the rest of
	// Clone: a caller distinguishing the two must keep seeing what it saw.
	if got := (PackageMetadata{}).Clone(); got.WheelTags != nil {
		t.Errorf("Clone() turned a nil tag slice into %v", got.WheelTags)
	}
}

// TestCloneCopiesEveryExportedSlice is the maintenance contract Clone documents,
// checked by reflection instead of by hope.
//
// Clone's own comment says that adding an exported slice to PackageMetadata means
// adding a copy to it. Nothing enforced that, so the next exported slice would be
// shared with the cache silently -- this change added one and would have been that
// bug. The test walks the type rather than a list, so it also covers the slice
// after this one.
func TestCloneCopiesEveryExportedSlice(t *testing.T) {
	typ := reflect.TypeOf(PackageMetadata{})

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() || f.Type.Kind() != reflect.Slice {
			continue
		}

		// A one-element slice of the field's own element type, so this works for
		// any slice PackageMetadata grows later, not just []string.
		filled := reflect.New(typ).Elem()
		slice := reflect.MakeSlice(f.Type, 1, 1)
		filled.Field(i).Set(slice)

		original := filled.Interface().(PackageMetadata)
		clone := original.Clone()

		got := reflect.ValueOf(clone).Field(i)
		if got.Len() != 1 {
			t.Errorf("Clone() dropped %s", f.Name)
			continue
		}
		if got.UnsafePointer() == slice.UnsafePointer() {
			t.Errorf("Clone() SHARES %s with the original; add a copy for it in Clone", f.Name)
		}
	}
}
