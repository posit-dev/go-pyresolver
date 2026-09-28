// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import "testing"

// TestYanksCapturedDefaultsToFalse mirrors TestWheelTagsCompleteDefaultsToFalse:
// an index that cannot say its yank data is complete must read as not
// captured, so yank filtering stays off rather than turning on for indexes
// that never speak to the question.
func TestYanksCapturedDefaultsToFalse(t *testing.T) {
	if YanksCaptured(plainIndex{}) {
		t.Error("an index that does not implement YankIndex must read as not captured")
	}
	if YanksCaptured(NewMockIndex("m")) {
		t.Error("a fresh MockIndex has no yank data and must read as not captured")
	}
	if !YanksCaptured(NewMockIndex("m").SetYanksCaptured(true)) {
		t.Error("an index that declares itself captured must read as captured")
	}
}

// TestMultiIndexYanksCapturedIsTheAND mirrors the wheel-tag composition rule:
// one captured source must not license filtering a sibling's uncaptured
// versions.
func TestMultiIndexYanksCapturedIsTheAND(t *testing.T) {
	captured := NewMockIndex("a").SetYanksCaptured(true)
	uncaptured := NewMockIndex("b")

	tests := []struct {
		name    string
		sources []MetadataIndex
		want    bool
	}{
		{"no sources", nil, false},
		{"one captured", []MetadataIndex{captured}, true},
		{"both captured", []MetadataIndex{captured, NewMockIndex("c").SetYanksCaptured(true)}, true},
		{"captured then uncaptured", []MetadataIndex{captured, uncaptured}, false},
		{"uncaptured then captured", []MetadataIndex{uncaptured, captured}, false},
		{"captured then non-implementor", []MetadataIndex{captured, plainIndex{}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewMultiIndex(tc.sources...).YanksCaptured()
			if got != tc.want {
				t.Errorf("YanksCaptured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFilteredIndexForwardsYanksCaptured mirrors
// TestFilteredIndexForwardsWheelTagsComplete: a FilterPolicy removes versions,
// it does not change whether the surviving versions' yank data is complete.
func TestFilteredIndexForwardsYanksCaptured(t *testing.T) {
	captured := NewMockIndex("a").SetYanksCaptured(true)
	if !NewFilteredIndex(captured, FilterPolicy{}).YanksCaptured() {
		t.Error("a FilteredIndex over a captured index must report captured")
	}
	if NewFilteredIndex(NewMockIndex("b"), FilterPolicy{}).YanksCaptured() {
		t.Error("a FilteredIndex over an uncaptured index must report not captured")
	}
	if NewFilteredIndex(plainIndex{}, FilterPolicy{}).YanksCaptured() {
		t.Error("a FilteredIndex over a non-implementor must report not captured")
	}
}
