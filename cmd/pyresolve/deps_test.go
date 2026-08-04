// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDepsCmdExplicitVersion(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, false, "flask", "3.0.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "flask 3.0.0") {
		t.Errorf("expected header line, got:\n%s", out)
	}
	if !strings.Contains(out, "Requires-Python: >=3.8") {
		t.Errorf("expected Requires-Python line, got:\n%s", out)
	}
	if !strings.Contains(out, "werkzeug>=3.0") {
		t.Errorf("expected the werkzeug requirement, got:\n%s", out)
	}
	if !strings.Contains(out, "async") {
		t.Errorf("expected the normalized 'Async' extra, got:\n%s", out)
	}
}

func TestDepsCmdOmittedVersionUsesHighest(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, true, "flask", ""); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	var result depsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if result.Version != "3.0.1" {
		t.Errorf("Version = %q, want the highest captured version 3.0.1", result.Version)
	}
	// 3.0.1's second requirement carries an environment marker; String()
	// must reconstruct it so the caller does not silently lose the marker.
	found := false
	for _, r := range result.RequiresDist {
		if strings.Contains(r, "asgiref") && strings.Contains(r, "extra ==") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the marker-conditional requirement to keep its marker, got: %v", result.RequiresDist)
	}
}

func TestDepsCmdUnknownPackage(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := depsCmd(&buf, path, false, "not-a-real-package", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "not-a-real-package") {
		t.Errorf("error should name the package, got: %v", err)
	}
}

func TestDepsCmdUncapturedVersion(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := depsCmd(&buf, path, false, "flask", "99.0")
	if err == nil {
		t.Fatal("expected an error for a version with no captured metadata")
	}
	if exitCodeFor(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeFor(err))
	}
}

func TestDepsCmdInvalidVersionString(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := depsCmd(&buf, path, false, "flask", "not a version at all !!!")
	if err == nil {
		t.Fatal("expected an error for an unparseable version argument")
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("exit code = %d, want 1 (usage error, not a not-found)", exitCodeFor(err))
	}
}
