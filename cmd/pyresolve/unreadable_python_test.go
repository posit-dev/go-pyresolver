// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// An unreadable Requires-Python must not be reported as an absent one.
//
// The old output claimed "(unconstrained)" for both, which asserts the publisher
// declared no interpreter constraint when the publisher declared one this tool
// could not read -- and hides that the version is being admitted for every
// interpreter by fallback rather than by declaration. 536 packages in a
// production snapshot are in this state.
func TestDepsCmdUnreadableRequiresPythonIsDistinct(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, false, "badpython", "1.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "(unconstrained)") {
		t.Errorf("an unreadable Requires-Python is reported as unconstrained:\n%s", out)
	}
	if !strings.Contains(out, "unreadable") {
		t.Errorf("output should say the constraint was unreadable, got:\n%s", out)
	}
	// The raw string is the only actionable detail: it is what the publisher
	// actually wrote, and what someone would have to fix.
	if !strings.Contains(out, "3.8 or whatever") {
		t.Errorf("output should quote the raw constraint, got:\n%s", out)
	}
}

func TestDepsCmdUnreadableRequiresPythonJSON(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, true, "badpython", "1.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	var result depsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if !result.RequiresPythonUnreadable {
		t.Error("requires_python_unreadable should be true")
	}
	if result.RequiresPythonRaw != ">= 3.8 or whatever" {
		t.Errorf("requires_python_raw = %q, want the raw constraint", result.RequiresPythonRaw)
	}
	// ⚠️ RequiresPython carries omitempty, so JSON consumers previously saw the
	// unreadable case and the absent case as byte-identical: the field simply
	// missing. The two new fields are what make them distinguishable.
	if result.RequiresPython != "" {
		t.Errorf("requires_python = %q, want empty for an unreadable constraint",
			result.RequiresPython)
	}
}

// The absent case must keep reporting "(unconstrained)", so the new branch is
// not over-broad. `nodeps` has no dependency field at all; `markupsafe` has a
// captured version with no Requires-Python.
func TestDepsCmdAbsentRequiresPythonStillUnconstrained(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, false, "markupsafe", "2.1.5"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "(unconstrained)") {
		t.Errorf("an absent Requires-Python should read as unconstrained, got:\n%s", out)
	}
	if strings.Contains(out, "unreadable") {
		t.Errorf("an absent Requires-Python must not be called unreadable, got:\n%s", out)
	}
}

// A readable constraint is unaffected.
func TestDepsCmdReadableRequiresPythonUnaffected(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, false, "flask", "3.0.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Requires-Python: >=3.8") {
		t.Errorf("expected the parsed constraint, got:\n%s", out)
	}
	if strings.Contains(out, "unreadable") || strings.Contains(out, "(unconstrained)") {
		t.Errorf("a readable constraint should print plainly, got:\n%s", out)
	}
}
