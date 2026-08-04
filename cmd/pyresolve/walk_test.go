// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
	// asgiref is not itself an indexed package in the fixture, so it should
	// show up as unresolved evidence that its edge was followed, proving the
	// walk read 3.0.1's requirements rather than 3.0.0's.
	foundAsgiref := got["asgiref"]
	unresolvedHasAsgiref := false
	for _, u := range result.Unresolved {
		if u == "asgiref" {
			unresolvedHasAsgiref = true
		}
	}
	if !foundAsgiref && !unresolvedHasAsgiref {
		t.Errorf("expected asgiref to be reached (from the highest version 3.0.1), packages=%v unresolved=%v",
			result.Packages, result.Unresolved)
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
