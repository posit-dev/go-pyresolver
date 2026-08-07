// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionsCmdSortedAscending(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, false, "Flask"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}

	out := buf.String()
	i300 := strings.Index(out, "3.0.0")
	i301 := strings.Index(out, "3.0.1")
	if i300 < 0 || i301 < 0 {
		t.Fatalf("expected both versions present, got:\n%s", out)
	}
	if i300 > i301 {
		t.Errorf("expected 3.0.0 before 3.0.1 (ascending PEP 440 order), got:\n%s", out)
	}
	// The unparseable "not-a-version" key must not appear.
	if strings.Contains(out, "not-a-version") {
		t.Errorf("unparseable version key leaked into output:\n%s", out)
	}
}

func TestVersionsCmdJSON(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, true, "flask"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}

	var result versionsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if result.Count != 2 {
		t.Fatalf("Count = %d, want 2", result.Count)
	}
	if result.Versions[0] != "3.0.0" || result.Versions[1] != "3.0.1" {
		t.Errorf("Versions = %v, want [3.0.0 3.0.1]", result.Versions)
	}
}

func TestVersionsCmdUnknownPackage(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := versionsCmd(&buf, path, false, "totally-not-a-real-package")
	if err == nil {
		t.Fatal("expected an error for an unknown package")
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "totally-not-a-real-package") {
		t.Errorf("error should name the package, got: %v", err)
	}
}

func TestVersionsCmdPackageWithNoCapturedDeps(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, false, "nodeps"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}
	if !strings.Contains(buf.String(), "no versions with captured dependency data") {
		t.Errorf("expected a clear zero-versions message, got:\n%s", buf.String())
	}
}

// TestVersionsCmdReportsUnparseableKeysRatherThanClaimingNoData is the regression
// test for the state collapse: a package whose every stored key PEP 440 rejects
// was reported as having no captured dependency data, when it HAS data recorded
// under a key the specification does not accept.
//
// Asserting the key is named is the point. "None of the keys is valid" without
// saying which one leaves the reader no better off than the old message did.
func TestVersionsCmdReportsUnparseableKeysRatherThanClaimingNoData(t *testing.T) {
	path := allKeysUnparseableFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, false, "onlybadkeys"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "no versions with captured dependency data") {
		t.Errorf("reported present data as absent; the package has a key carrying a real "+
			"dependency:\n%s", out)
	}
	if !strings.Contains(out, "0.2.1.Perceval") {
		t.Errorf("did not name the offending key, so the reader cannot act on it:\n%s", out)
	}
	if !strings.Contains(out, "PEP 440") {
		t.Errorf("did not say WHY the key was rejected:\n%s", out)
	}
}

// TestVersionsCmdJSONCarriesUnparseableKeys covers the machine-readable path; a
// message only in the text output is invisible to anything scripting this.
func TestVersionsCmdJSONCarriesUnparseableKeys(t *testing.T) {
	path := allKeysUnparseableFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, true, "onlybadkeys"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}

	var result versionsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if result.Count != 0 {
		t.Errorf("Count = %d, want 0", result.Count)
	}
	if len(result.UnparseableKeys) != 1 || result.UnparseableKeys[0] != "0.2.1.Perceval" {
		t.Errorf("UnparseableKeys = %v, want [0.2.1.Perceval]", result.UnparseableKeys)
	}
}

// TestVersionsCmdOmitsUnparseableKeysWhenVersionsExist keeps the new field from
// becoming noise. "flask" in the standard fixture has a bad key alongside good
// ones; the command answers the question asked and does not caveat a successful
// listing.
func TestVersionsCmdOmitsUnparseableKeysWhenVersionsExist(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := versionsCmd(&buf, path, true, "flask"); err != nil {
		t.Fatalf("versionsCmd: %v", err)
	}

	var result versionsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v", err)
	}
	if result.Count == 0 {
		t.Fatal("precondition: flask should have versions")
	}
	if len(result.UnparseableKeys) != 0 {
		t.Errorf("UnparseableKeys = %v, want empty when versions were reported", result.UnparseableKeys)
	}
}
