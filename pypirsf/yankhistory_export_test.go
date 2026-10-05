// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"testing"
)

func widgetHistory() map[string]map[string][]historyTransition {
	return map[string]map[string][]historyTransition{
		"widget": {
			"1.1": {{"1000000100", true}, {"1000000300", false}, {"1000000500", true}},
			"1.2": {{"1000000200", true}, {"1000000400", false}},
		},
	}
}

func TestYankHistoryLookups(t *testing.T) {
	h, err := DecodeYankHistory(buildYankHistoryDepsField(true, widgetHistory()), nil)
	if err != nil {
		t.Fatalf("DecodeYankHistory: %v", err)
	}
	if !h.Captured() {
		t.Error("Captured() = false, want true")
	}

	keys := []string{"0999999999", "1000000100", "1000000150", "1000000250", "1000000300", "1000000400", "1000000450", "1000000500"}
	// want[version] holds the YankedAt answer for each key above, in order.
	want := map[string][]bool{
		"1.0": {false, false, false, false, false, false, false, false},
		"1.1": {false, true, true, true, false, false, false, true},
		"1.2": {false, false, false, true, true, false, false, false},
	}
	for version, row := range want {
		for i, key := range keys {
			if got := h.YankedAt("widget", version, key); got != row[i] {
				t.Errorf("YankedAt(widget, %s, %s) = %v, want %v", version, key, got, row[i])
			}
		}
	}
	for _, c := range []struct{ cname, version string }{{"nosuch", "1.0"}, {"widget", "9.9"}} {
		if h.YankedAt(c.cname, c.version, "1000000500") || h.YankedLatest(c.cname, c.version) {
			t.Errorf("%s %s: unknown package or version must not be yanked", c.cname, c.version)
		}
	}

	latest := map[string]bool{"1.0": false, "1.1": true, "1.2": false}
	for version, w := range latest {
		if got := h.YankedLatest("widget", version); got != w {
			t.Errorf("YankedLatest(widget, %s) = %v, want %v", version, got, w)
		}
	}
}

func TestYankHistoryCapturedAndNil(t *testing.T) {
	h, err := DecodeYankHistory(buildYankHistoryDepsField(false, widgetHistory()), nil)
	if err != nil {
		t.Fatalf("DecodeYankHistory: %v", err)
	}
	if h.Captured() {
		t.Error("Captured() = true for captured=false payload")
	}

	var nilH *YankHistory
	if nilH.Captured() || nilH.YankedAt("widget", "1.1", "1000000500") || nilH.YankedLatest("widget", "1.1") {
		t.Error("nil *YankHistory must answer false everywhere")
	}
}

func TestDecodeYankHistoryRejectsCorruptField(t *testing.T) {
	valid := buildYankHistoryDepsField(true, widgetHistory())
	badVersion := string([]byte{depsFormatStored, 2}) + valid[2:]
	for name, field := range map[string]string{
		"empty":            "",
		"bad format byte":  "\x7f" + valid[1:],
		"bad version byte": badVersion,
		"trailing bytes":   valid + "x",
		"truncated":        valid[:len(valid)-1],
	} {
		if h, err := DecodeYankHistory(field, nil); err == nil || h != nil {
			t.Errorf("%s: got (%v, %v), want nil and an error", name, h, err)
		}
	}
}

// TestOpenDepsYankedMatchesYankedLatest proves Open's per-version Yanked flags
// are the exported latest state, for every version.
func TestOpenDepsYankedMatchesYankedLatest(t *testing.T) {
	names := []string{"werkzeug"}
	versions := []string{"1.0", "1.1", "1.2"}
	deps := map[string]VersionDeps{}
	snaps := []SnapshotRecord{}
	for _, v := range versions {
		deps[v] = VersionDeps{}
		snaps = append(snaps, SnapshotRecord{Snapshot: "2026080100", Version: v, ReleaseDate: "\x00\x01", Summary: "s"})
	}
	widget := PackageRecord{
		CanonicalName: "widget",
		ProjectName:   "widget",
		Snapshots:     snaps,
		Deps:          buildDepsField(t, deps, names),
		Depsdict:      buildDepsdictField(names),
	}
	field := buildYankHistoryDepsField(true, widgetHistory())
	f, err := Open(writeFixtureRSF(t, []PackageRecord{widget, sentinelRecord(field)}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	h, err := DecodeYankHistory(field, nil)
	if err != nil {
		t.Fatalf("DecodeYankHistory: %v", err)
	}
	got, err := f.Deps("widget")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	gotYanked := map[string]bool{}
	for v, vd := range got {
		gotYanked[v] = vd.Yanked
	}
	wantYanked := map[string]bool{"1.0": false, "1.1": true, "1.2": false}
	for _, v := range versions {
		if g, w := gotYanked[v], h.YankedLatest("widget", v); g != w {
			t.Errorf("Deps(widget)[%s].Yanked = %v, YankedLatest = %v", v, g, w)
		}
		if gotYanked[v] != wantYanked[v] {
			t.Errorf("Deps(widget)[%s].Yanked = %v, want %v", v, gotYanked[v], wantYanked[v])
		}
	}
	if len(gotYanked) != len(wantYanked) {
		t.Errorf("Deps(widget) versions = %v, want %v", gotYanked, wantYanked)
	}
}

func FuzzDecodeYankHistory(f *testing.F) {
	f.Add(buildYankHistoryDepsField(true, widgetHistory()))
	f.Add(buildYankHistoryDepsField(false, nil))
	f.Add(string([]byte{depsFormatStored, 1, 1}) + string(bytes.Repeat([]byte{0xff}, 12)))
	f.Fuzz(func(t *testing.T, field string) {
		h, err := DecodeYankHistory(field, nil)
		if err != nil {
			return
		}
		h.Captured()
		h.YankedAt("widget", "1.1", "1000000300")
		h.YankedLatest("widget", "1.1")
	})
}
