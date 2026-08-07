// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A name with no record in this RSF must not be reported as a reachable
// package. It used to appear in BOTH Packages and Absent, which are
// contradictory claims, and Count included it -- inflating "N package(s)
// reachable" with names that cannot be installed from this file.
//
// ⚠️ TestWalkCmdDistinguishesAbsentFromUncaptured uses this same fixture but
// asserts only on Absent and NoDependencyData, never on Packages or Count.
// That is why the defect survived: the fixture had always been capable of
// exposing it and no assertion looked.
func TestWalkAbsentNamesAreNotReachable(t *testing.T) {
	path := absentAndUncapturedFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "aroot", 3); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	if !slices.Contains(result.Absent, "totally-absent") {
		t.Fatalf("fixture should report totally-absent as absent, got %v", result.Absent)
	}

	if slices.Contains(result.Packages, "totally-absent") {
		t.Errorf("an absent name must not be listed as a reachable package; "+
			"Packages = %v, Absent = %v", result.Packages, result.Absent)
	}
	if result.Count != len(result.Packages) {
		t.Errorf("Count = %d but len(Packages) = %d", result.Count, len(result.Packages))
	}

	// Absent and Packages must be disjoint, which is the invariant rather than
	// the single example.
	for _, a := range result.Absent {
		if slices.Contains(result.Packages, a) {
			t.Errorf("%q appears in both Absent and Packages", a)
		}
	}
}

// The complement: a name that IS in this RSF but could not be expanded stays
// reachable. Those are real packages, and dropping them would understate the
// closure and re-collapse "not in this file" with "in this file but unreadable".
func TestWalkUncapturedNamesStayReachable(t *testing.T) {
	path := absentAndUncapturedFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "aroot", 3); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	if !slices.Contains(result.NoDependencyData, "present-but-empty") {
		t.Fatalf("fixture should report present-but-empty as uncaptured, got %v",
			result.NoDependencyData)
	}
	if !slices.Contains(result.Packages, "present-but-empty") {
		t.Errorf("a name present in this RSF should stay reachable even when it "+
			"cannot be expanded; Packages = %v", result.Packages)
	}
}

// Unusable metadata likewise stays reachable: the record exists.
func TestWalkUnusableNamesStayReachable(t *testing.T) {
	path := unusableMidChainFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "uroot", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	if !slices.Contains(result.UnusableMetadata, "ubad") {
		t.Fatalf("fixture should report ubad as unusable, got %v", result.UnusableMetadata)
	}
	if !slices.Contains(result.Packages, "ubad") {
		t.Errorf("ubad has a record in this RSF and should stay reachable; "+
			"Packages = %v", result.Packages)
	}
}

// The human-readable total must agree with the list above it.
func TestWalkTextCountMatchesListedPackages(t *testing.T) {
	path := absentAndUncapturedFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, false, "aroot", 3); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "totally-absent\n") {
		// The absent name may legitimately appear inside the "absent from this
		// RSF" section; what it must not do is appear in the reachable listing.
		before, _, found := strings.Cut(out, "package(s) reachable")
		if found && strings.Contains(before, "totally-absent\n") {
			t.Errorf("absent name listed among reachable packages:\n%s", out)
		}
	}
}
