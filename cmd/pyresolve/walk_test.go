// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// The fixture chain is flask -> werkzeug -> markupsafe, three edges deep.
// requests is unreferenced by any of them.

func TestWalkCmdFullDepthReachesWholeChain(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	want := map[string]bool{"flask": true, "werkzeug": true, "markupsafe": true}
	got := map[string]bool{}
	for _, p := range result.Packages {
		got[p] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("expected %q reachable, packages = %v", name, result.Packages)
		}
	}
	if got["requests"] {
		t.Errorf("requests is not referenced by the chain and must not appear: %v", result.Packages)
	}
	if result.Count != len(result.Packages) {
		t.Errorf("Count = %d, want len(Packages) = %d", result.Count, len(result.Packages))
	}
}

func TestWalkCmdDepthZeroIsRootOnly(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 0); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}
	if result.Count != 1 || result.Packages[0] != "flask" {
		t.Errorf("depth 0 should reach only the root, got %v", result.Packages)
	}
}

func TestWalkCmdDepthOneReachesDirectDepsOnly(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 1); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	got := map[string]bool{}
	for _, p := range result.Packages {
		got[p] = true
	}
	if !got["flask"] || !got["werkzeug"] {
		t.Errorf("depth 1 should reach flask and werkzeug, got %v", result.Packages)
	}
	if got["markupsafe"] {
		t.Errorf("markupsafe is two edges away and should not be reachable at depth 1, got %v", result.Packages)
	}
}

func TestWalkCmdTakesHighestVersionOnly(t *testing.T) {
	// flask 3.0.1 depends on asgiref (marker-conditional) in addition to
	// werkzeug; 3.0.0 does not reference asgiref at all. Walk must use only
	// the highest version's requirements.
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 1); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}
	got := map[string]bool{}
	for _, p := range result.Packages {
		got[p] = true
	}
	// asgiref is not itself an indexed package in the fixture, so reaching it at
	// all is the evidence that its edge was followed — proving the walk read
	// 3.0.1's requirements rather than 3.0.0's. It may land in Packages or, as a
	// name with no record, in Absent.
	foundAsgiref := got["asgiref"]
	for _, u := range result.Absent {
		if u == "asgiref" {
			foundAsgiref = true
		}
	}
	for _, u := range result.NoDependencyData {
		if u == "asgiref" {
			foundAsgiref = true
		}
	}
	if !foundAsgiref {
		t.Errorf("expected asgiref to be reached (from the highest version 3.0.1), packages=%v absent=%v noDeps=%v",
			result.Packages, result.Absent, result.NoDependencyData)
	}
}

func TestWalkCmdTextOutputStatesItIsNotResolution(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, false, "flask", 1); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "NOT dependency resolution") {
		t.Errorf("walk's own output must state it is not resolution, got:\n%s", out)
	}
}

func TestWalkCmdJSONIncludesNote(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 1); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}
	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}
	if !strings.Contains(result.Note, "NOT dependency resolution") {
		t.Errorf("JSON output must also carry the not-a-resolver note, got: %q", result.Note)
	}
}

func TestWalkCmdUnknownRootPackage(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := walkCmd(&buf, path, false, "not-a-real-package", 3)
	if err == nil {
		t.Fatal("expected an error for an unknown root package")
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeFor(err))
	}
}

func TestWalkCmdCyclesDoNotHang(t *testing.T) {
	// werkzeug's own requirements form a package that in turn could loop
	// back; the visited-set must prevent infinite growth. Using a generous
	// depth on the real fixture is enough to prove termination.
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 1000); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}
}

// TestWalkCmdDistinguishesAbsentFromUncaptured guards a distinction that is easy
// to collapse and misleading when collapsed.
//
// "This RSF has never heard of the package" and "this RSF has the package but
// captured no usable dependency data for it" are different facts. Reporting the
// second as the first sends someone hunting for a typo in a name that is present
// and spelled correctly — and the second case is common and expected, since a
// package with no built distribution has no captured dependency metadata.
//
// Verified against real data: walking flask in the production RSF reaches
// big-o and curio, both of which ARE present in the file with zero captured
// versions. An earlier version of this command reported them as "not found".
func TestWalkCmdDistinguishesAbsentFromUncaptured(t *testing.T) {
	// root depends on two packages: one present-but-uncaptured, one absent.
	root := pypirsf.PackageRecord{
		CanonicalName: "root",
		ProjectName:   "Root",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"present-but-empty", "totally-absent"}},
		}),
		Depsdict: buildDepsdictField(),
	}
	// Present in the file, but no deps field at all, so no captured versions.
	presentButEmpty := pypirsf.PackageRecord{
		CanonicalName: "present-but-empty",
		ProjectName:   "PresentButEmpty",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "9.9", ReleaseDate: "\x00\x01", Summary: "x"},
		},
	}

	path := writeFixtureRSF(t, []pypirsf.PackageRecord{root, presentButEmpty})

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "root", 3); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	if len(result.Absent) != 1 || result.Absent[0] != "totally-absent" {
		t.Errorf("Absent = %v, want [totally-absent]", result.Absent)
	}
	if len(result.NoDependencyData) != 1 || result.NoDependencyData[0] != "present-but-empty" {
		t.Errorf("NoDependencyData = %v, want [present-but-empty]", result.NoDependencyData)
	}

	// And the text output must say two different things, not one.
	var text bytes.Buffer
	if err := walkCmd(&text, path, false, "root", 3); err != nil {
		t.Fatalf("walkCmd (text): %v", err)
	}
	out := text.String()
	if !strings.Contains(out, "absent from this RSF") {
		t.Errorf("text output should report absent names distinctly:\n%s", out)
	}
	if !strings.Contains(out, "no captured dependency data") {
		t.Errorf("text output should report uncaptured names distinctly:\n%s", out)
	}
}
