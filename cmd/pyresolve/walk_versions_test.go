// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// walk takes ONE version of each package and its shape depends on which, but it
// reported only names -- so a reader could not tell which version produced any
// edge, and two walks over different snapshots printed identically while
// describing different graphs.
func TestWalkReportsSelectedVersions(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	// flask's highest captured version is 3.0.1, and the chain reaches werkzeug
	// 3.0.1 and markupsafe 2.1.5.
	for name, want := range map[string]string{
		"flask":      "3.0.1",
		"werkzeug":   "3.0.1",
		"markupsafe": "2.1.5",
	} {
		if got := result.SelectedVersions[name]; got != want {
			t.Errorf("SelectedVersions[%q] = %q, want %q (all: %v)",
				name, got, want, result.SelectedVersions)
		}
	}
}

// The map must never claim a version for a package the walk did not list, or the
// two outputs disagree.
func TestWalkSelectedVersionsAgreeWithPackages(t *testing.T) {
	path := absentAndUncapturedFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "aroot", 3); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	listed := make(map[string]bool, len(result.Packages))
	for _, n := range result.Packages {
		listed[n] = true
	}
	for name := range result.SelectedVersions {
		if !listed[name] {
			t.Errorf("SelectedVersions names %q, which is not in Packages %v",
				name, result.Packages)
		}
	}

	// present-but-empty has a record but nothing captured, so no version was
	// selected for it. It stays reachable (see F10) and simply has no entry --
	// which is different from claiming an unknown version.
	if v, ok := result.SelectedVersions["present-but-empty"]; ok {
		t.Errorf("no version should be selected for an uncaptured package, got %q", v)
	}
	if !listed["present-but-empty"] {
		t.Error("present-but-empty should still be reachable")
	}
}

// ⚠️ Selection moved ahead of the depth cutoff precisely so this works. With the
// cutoff first, a package AT the limit had no version reported, which with
// --depth 1 meant almost none of them did.
func TestWalkReportsVersionsAtDepthCutoff(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "flask", 1); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	// werkzeug sits at depth 1, the cutoff: reached and selected, never expanded.
	if got := result.SelectedVersions["werkzeug"]; got != "3.0.1" {
		t.Errorf("a package at the depth cutoff should still report its selected "+
			"version; SelectedVersions = %v", result.SelectedVersions)
	}
}

// A version was selected for an unusable package -- selection succeeded and
// parsing its metadata then failed -- so it does get an entry.
func TestWalkReportsVersionForUnusableMetadata(t *testing.T) {
	path := unusableMidChainFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "uroot", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}

	if got := result.SelectedVersions["ubad"]; got != "1.0" {
		t.Errorf("SelectedVersions[\"ubad\"] = %q, want 1.0: a version WAS selected "+
			"and its metadata then failed to parse (all: %v)", got, result.SelectedVersions)
	}
}

func TestWalkTextOutputShowsVersions(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, false, "flask", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "flask 3.0.1\n") {
		t.Errorf("text output should show the selected version, got:\n%s", out)
	}
	if !strings.Contains(out, "markupsafe 2.1.5\n") {
		t.Errorf("text output should show versions for transitive packages, got:\n%s", out)
	}
}
