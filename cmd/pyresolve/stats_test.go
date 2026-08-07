// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStatsCmdText(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := statsCmd(&buf, path, false); err != nil {
		t.Fatalf("statsCmd: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Packages:               6\n") {
		t.Errorf("expected 6 packages in output, got:\n%s", out)
	}
	if !strings.Contains(out, "File size:") {
		t.Errorf("expected a file size line, got:\n%s", out)
	}
}

func TestStatsCmdJSON(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := statsCmd(&buf, path, true); err != nil {
		t.Fatalf("statsCmd: %v", err)
	}

	var result statsResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if result.Packages != 6 {
		t.Errorf("Packages = %d, want 6", result.Packages)
	}
	if result.FileSizeB <= 0 {
		t.Errorf("FileSizeB = %d, want > 0", result.FileSizeB)
	}
	if result.Path != path {
		t.Errorf("Path = %q, want %q", result.Path, path)
	}
}

func TestStatsCmdMissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := statsCmd(&buf, "/nonexistent/does-not-exist.rsf", false)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("exit code = %d, want 1", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say the file was not found, got: %v", err)
	}
}

func TestStatsCmdNoDependencyData(t *testing.T) {
	path := noDepsDataFixture(t)

	var buf bytes.Buffer
	err := statsCmd(&buf, path, false)
	if err == nil {
		t.Fatal("expected an error for a no-dependency-data RSF")
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("exit code = %d, want 1", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "no dependency data") && !strings.Contains(err.Error(), "predates") {
		t.Errorf("error should clearly explain missing dependency data, got: %v", err)
	}
}
