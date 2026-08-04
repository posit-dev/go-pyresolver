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
